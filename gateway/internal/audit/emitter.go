// Package audit batches gateway request events and POSTs them to the backend.
// Emit never waits on the network. The signing key and event contents are not logged.
package audit

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	maxBatch        = 500
	defaultQueue    = 10000
	defaultInterval = time.Second
	defaultBackoff  = 100 * time.Millisecond
	defaultTimeout  = 5 * time.Second
	headerTimestamp = "x-storzo-timestamp"
	headerSignature = "x-storzo-signature"
	contentTypeJSON = "application/json"

	// Field caps match the audit receiver. A longer value makes it reject the whole batch.
	maxKid      = 64
	maxMethod   = 16
	maxBucket   = 63
	maxKey      = 1024
	maxDecision = 16
)

// Event is one gateway request. JSON names match the audit ingest contract.
type Event struct {
	Kid      string `json:"kid"`
	Method   string `json:"method"`
	Bucket   string `json:"bucket"`
	Key      string `json:"key"`
	Status   int    `json:"status"`
	Bytes    int64  `json:"bytes"`
	Decision string `json:"decision"`
}

// Config configures an Emitter. Zero values use the defaults noted on each field.
type Config struct {
	// URL is the audit endpoint (AUDIT_URL).
	URL string
	// SigningKey is CRED_SIGNING_KEY. It is never logged.
	SigningKey string
	// QueueSize is the bounded queue length. Default 10000.
	// A full queue drops the event instead of blocking Emit.
	QueueSize int
	// BatchSize flushes early when this many events are waiting.
	// Default 500. Values above 500 are capped because the receiver rejects them.
	BatchSize int
	// FlushInterval is the longest an event waits before a flush. Default 1s.
	FlushInterval time.Duration
	// RetryBackoff sets the first retry delay (default 100ms).
	// The next delays are 5x and 20x: by default 500ms and 2s.
	RetryBackoff time.Duration
	// Client is optional. Tests inject one; nil uses a client with a 5s timeout.
	Client *http.Client
}

// Emitter queues events and POSTs them on a background goroutine.
type Emitter struct {
	url       string
	key       []byte
	queue     chan Event
	batchSize int
	interval  time.Duration
	backoff   time.Duration
	client    *http.Client

	base   context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	closed bool
	stop   chan struct{}
	done   chan struct{}

	dropped atomic.Uint64
	failed  atomic.Uint64
}

// New starts the flush loop. The caller must Close the emitter.
func New(cfg Config) *Emitter {
	queueSize := cfg.QueueSize
	if queueSize <= 0 {
		queueSize = defaultQueue
	}
	batchSize := cfg.BatchSize
	if batchSize <= 0 || batchSize > maxBatch {
		batchSize = maxBatch
	}
	interval := cfg.FlushInterval
	if interval <= 0 {
		interval = defaultInterval
	}
	backoff := cfg.RetryBackoff
	if backoff <= 0 {
		backoff = defaultBackoff
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Emitter{
		url:       cfg.URL,
		key:       append([]byte(nil), cfg.SigningKey...),
		queue:     make(chan Event, queueSize),
		batchSize: batchSize,
		interval:  interval,
		backoff:   backoff,
		client:    client,
		base:      ctx,
		cancel:    cancel,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	go func() {
		e.loop()
		close(e.done)
	}()
	return e
}

// Emit queues one event. It never blocks and never panics.
// A full queue or an emitter that is already closed drops the event.
// Only a full queue increments Dropped.
func (e *Emitter) Emit(ev Event) {
	if e == nil {
		return
	}
	// Truncate before the queue so one oversized field cannot 422 the whole batch.
	ev = sanitize(ev)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	select {
	case e.queue <- ev:
	default:
		e.dropped.Add(1)
	}
}

// Dropped reports events discarded because the queue was full.
func (e *Emitter) Dropped() uint64 {
	if e == nil {
		return 0
	}
	return e.dropped.Load()
}

// Failed reports events abandoned after send failures or a shutdown deadline.
// Queue-full drops are counted separately by Dropped.
func (e *Emitter) Failed() uint64 {
	if e == nil {
		return 0
	}
	return e.failed.Load()
}

// Close stops Emit and flushes events already queued. It returns when the
// flush finishes or when ctx ends, whichever is first. The base context stays
// alive during draining; only an ended caller context interrupts pending sends.
func (e *Emitter) Close(ctx context.Context) error {
	if e == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	if !e.closed {
		e.closed = true
		close(e.stop)
	}
	e.mu.Unlock()

	select {
	case <-e.done:
		e.cancel()
		return nil
	case <-ctx.Done():
		e.cancel()
		return ctx.Err()
	}
}

func (e *Emitter) loop() {
	timer := time.NewTimer(e.interval)
	if !timer.Stop() {
		<-timer.C
	}
	armed := false
	arm := func() {
		if armed {
			return
		}
		timer.Reset(e.interval)
		armed = true
	}
	disarm := func() {
		if !armed {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		armed = false
	}

	var batch []Event
	for {
		select {
		case ev := <-e.queue:
			batch = append(batch, ev)
			if len(batch) >= e.batchSize {
				disarm()
				e.flush(batch)
				batch = nil
			} else {
				arm()
			}
		case <-timer.C:
			armed = false
			e.flush(batch)
			batch = nil
		case <-e.stop:
			disarm()
			batch = drain(e.queue, batch)
			e.flush(batch)
			return
		case <-e.base.Done():
			disarm()
			e.failed.Add(uint64(len(drain(e.queue, batch))))
			return
		}
	}
}

func drain(ch <-chan Event, batch []Event) []Event {
	for {
		select {
		case ev := <-ch:
			batch = append(batch, ev)
		default:
			return batch
		}
	}
}

func (e *Emitter) flush(events []Event) {
	for len(events) > 0 {
		if e.base.Err() != nil {
			e.failed.Add(uint64(len(events)))
			return
		}
		n := min(len(events), e.batchSize)
		e.send(events[:n])
		events = events[n:]
	}
}

type batchBody struct {
	Events []Event `json:"events"`
}

func (e *Emitter) send(events []Event) {
	if len(events) == 0 {
		return
	}
	delivered := false
	defer func() {
		if !delivered {
			e.failed.Add(uint64(len(events)))
		}
	}()
	body, err := json.Marshal(batchBody{Events: events})
	if err != nil {
		return
	}
	delays := [...]time.Duration{e.backoff, 5 * e.backoff, 20 * e.backoff}
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(delays[attempt-1])
			select {
			case <-timer.C:
			case <-e.base.Done():
				timer.Stop()
				return
			}
		}
		err = e.post(body)
		if err == nil {
			delivered = true
			return
		}
		if !e.retryable(err) {
			return
		}
	}
}

func (e *Emitter) post(body []byte) error {
	ctx := e.base
	if err := ctx.Err(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentTypeJSON)
	signRequest(req, e.key, body)
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return statusError(resp.StatusCode)
}

func sanitize(ev Event) Event {
	ev.Kid = truncateUTF8(ev.Kid, maxKid)
	ev.Method = truncateUTF8(ev.Method, maxMethod)
	ev.Bucket = truncateUTF8(ev.Bucket, maxBucket)
	ev.Key = truncateUTF8(ev.Key, maxKey)
	ev.Decision = truncateUTF8(ev.Decision, maxDecision)
	if ev.Status < 100 || ev.Status > 599 {
		ev.Status = 599
	}
	if ev.Bytes < 0 {
		ev.Bytes = 0
	}
	return ev
}

// truncateUTF8 cuts s to at most maxBytes without splitting a rune.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func signature(key []byte, ts string, body []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(ts))
	_, _ = mac.Write([]byte{'.'})
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

type statusError int

func (s statusError) Error() string {
	return "audit endpoint status " + strconv.Itoa(int(s))
}

func (e *Emitter) retryable(err error) bool {
	if err == nil {
		return false
	}
	// A client timeout can wrap DeadlineExceeded while the emitter is still alive.
	// Only cancellation of our base context means shutdown.
	if e.base.Err() != nil {
		return false
	}
	var status statusError
	if errors.As(err, &status) {
		return status == http.StatusTooManyRequests || status >= 500
	}
	return true
}
