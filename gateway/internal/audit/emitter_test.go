package audit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

const testKey = "0123456789abcdef0123456789abcdef"

type snap struct {
	body           []byte
	timestamp, sig string
	contentType    string
	method         string
}

type recorder struct {
	mu    sync.Mutex
	snaps []snap
	err   string
}

func (r *recorder) save(req *http.Request) {
	body, err := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.err = err.Error()
		return
	}
	r.snaps = append(r.snaps, snap{
		body:        append([]byte(nil), body...),
		timestamp:   req.Header.Get(headerTimestamp),
		sig:         req.Header.Get(headerSignature),
		contentType: req.Header.Get("Content-Type"),
		method:      req.Method,
	})
}

func (r *recorder) list() []snap {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]snap, len(r.snaps))
	copy(out, r.snaps)
	return out
}

func (r *recorder) check(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	err := r.err
	r.mu.Unlock()
	if err != "" {
		t.Fatal(err)
	}
}

func verifySnap(t *testing.T, key string, s snap) []Event {
	t.Helper()
	if s.method != http.MethodPost {
		t.Fatalf("method %s", s.method)
	}
	if s.contentType != contentTypeJSON {
		t.Fatalf("content-type %q", s.contentType)
	}
	if s.timestamp == "" || s.sig == "" {
		t.Fatal("missing signature headers")
	}
	if s.sig != hex.EncodeToString(mustDecode(t, s.sig)) {
		t.Fatalf("signature is not lowercase hex: %s", s.sig)
	}
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(s.timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(s.body)
	want := mac.Sum(nil)
	got := mustDecode(t, s.sig)
	if !hmac.Equal(want, got) {
		t.Fatalf("signature mismatch")
	}
	ts, err := strconv.ParseInt(s.timestamp, 10, 64)
	if err != nil {
		t.Fatalf("timestamp %q: %v", s.timestamp, err)
	}
	if d := time.Now().Unix() - ts; d > 30 || d < -30 {
		t.Fatalf("timestamp %d is not unix seconds", ts)
	}
	var payload batchBody
	if err := json.Unmarshal(s.body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Events) > maxBatch {
		t.Fatalf("batch length %d", len(payload.Events))
	}
	return payload.Events
}

func mustDecode(t *testing.T, sig string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(sig)
	if err != nil {
		t.Fatalf("sig hex: %v", err)
	}
	return raw
}

func finish(t *testing.T, em *Emitter) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := em.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func testClient(srv *httptest.Server) *http.Client {
	return srv.Client()
}

// protects=HMAC-SHA256 over "<unix seconds>.<raw body>" matches x-storzo-signature, and the timestamp is current unix seconds; fails_when=the header, body, or key would not verify; no_claim=the backend stored the batch or that a forged key is rejected in production
func TestSignatureAndTimestamp(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.save(r)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	em := New(Config{
		URL:           srv.URL,
		SigningKey:    testKey,
		BatchSize:     2,
		FlushInterval: time.Hour,
		Client:        testClient(srv),
	})
	want := []Event{
		{Kid: "kid-1", Method: "GET", Bucket: "photos", Key: "a/b.txt", Status: 200, Bytes: 12, Decision: "allow"},
		{Kid: "kid-1", Method: "PUT", Bucket: "photos", Key: "a/c.txt", Status: 403, Bytes: 0, Decision: "deny:prefix"},
	}
	for _, ev := range want {
		em.Emit(ev)
	}
	finish(t, em)
	rec.check(t)

	snaps := rec.list()
	if len(snaps) != 1 {
		t.Fatalf("posts = %d, want 1", len(snaps))
	}
	got := verifySnap(t, testKey, snaps[0])
	if len(got) != len(want) {
		t.Fatalf("events = %d", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	mac := hmac.New(sha256.New, []byte("different-key-32-characters-long"))
	_, _ = mac.Write([]byte(snaps[0].timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(snaps[0].body)
	if hmac.Equal(mac.Sum(nil), mustDecode(t, snaps[0].sig)) {
		t.Fatal("signature verified under the wrong key")
	}
}

// protects=each POST carries at most 500 events and a full batch flushes before the interval; fails_when=a body has more than 500 events or 500 events stay buffered; no_claim=the receiver accepts the batch or that every status was stored
func TestBatchNeverExceeds500(t *testing.T) {
	rec := &recorder{}
	first := make(chan int, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.save(r)
		var payload batchBody
		_ = json.Unmarshal(rec.list()[len(rec.list())-1].body, &payload)
		select {
		case first <- len(payload.Events):
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	const total = 1100
	em := New(Config{
		URL:           srv.URL,
		SigningKey:    testKey,
		QueueSize:     defaultQueue,
		BatchSize:     5000,
		FlushInterval: time.Hour,
		Client:        testClient(srv),
	})
	for i := 0; i < total; i++ {
		em.Emit(Event{Kid: "k", Method: "GET", Bucket: "b", Key: strconv.Itoa(i), Status: 200, Bytes: int64(i), Decision: "allow"})
	}
	select {
	case n := <-first:
		if n != maxBatch {
			t.Fatalf("first batch = %d, want %d", n, maxBatch)
		}
	case <-time.After(800 * time.Millisecond):
		t.Fatal("full batch did not flush before the interval")
	}
	finish(t, em)
	rec.check(t)
	if em.Dropped() != 0 {
		t.Fatalf("dropped %d", em.Dropped())
	}

	var got []Event
	for _, s := range rec.list() {
		ev := verifySnap(t, testKey, s)
		if len(ev) > maxBatch {
			t.Fatalf("batch %d", len(ev))
		}
		got = append(got, ev...)
	}
	if len(got) != total {
		t.Fatalf("delivered %d, want %d", len(got), total)
	}
	for i, ev := range got {
		if ev.Key != strconv.Itoa(i) || ev.Bytes != int64(i) {
			t.Fatalf("event %d = %+v", i, ev)
		}
	}
}

// protects=one buffered event is posted about one second after Emit, not immediately; fails_when=the event is sent early or is still buffered well after the interval; no_claim=the interval stays accurate under load or that the receiver was up
func TestFlushEverySecond(t *testing.T) {
	rec := &recorder{}
	got := make(chan time.Time, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.save(r)
		got <- time.Now()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	em := New(Config{
		URL:        srv.URL,
		SigningKey: testKey,
		Client:     testClient(srv),
	})
	start := time.Now()
	em.Emit(Event{Kid: "k", Method: "HEAD", Bucket: "b", Key: "one", Status: 204, Decision: "allow"})
	time.Sleep(200 * time.Millisecond)
	if len(rec.list()) != 0 {
		t.Fatal("flushed before 1s")
	}
	select {
	case at := <-got:
		elapsed := at.Sub(start)
		if elapsed < 800*time.Millisecond {
			t.Fatalf("flushed too soon: %s", elapsed)
		}
		if elapsed > 1800*time.Millisecond {
			t.Fatalf("flushed too late: %s", elapsed)
		}
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("event was not flushed")
	}
	finish(t, em)
	rec.check(t)
	snaps := rec.list()
	if len(snaps) != 1 {
		t.Fatalf("posts = %d", len(snaps))
	}
	ev := verifySnap(t, testKey, snaps[0])
	if len(ev) != 1 || ev[0].Key != "one" {
		t.Fatalf("events = %+v", ev)
	}
}

// protects=Emit drops and counts events once the queue is full, and returns while a flush is stuck; fails_when=Emit blocks or Dropped stays at zero; no_claim=dropped events are retried later or that the in-flight batch was accepted
func TestFullQueueDropsWithoutBlocking(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	const queueSize = 8
	const extra = 100
	em := New(Config{
		URL:           srv.URL,
		SigningKey:    testKey,
		QueueSize:     queueSize,
		BatchSize:     1,
		FlushInterval: time.Hour,
		RetryBackoff:  time.Millisecond,
		Client:        testClient(srv),
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = em.Close(ctx)
	})
	t.Cleanup(func() { close(release) })

	em.Emit(Event{Method: "GET", Key: "held"})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("flusher did not send")
	}
	start := time.Now()
	for i := 0; i < extra; i++ {
		em.Emit(Event{Method: "PUT", Status: i})
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("Emit blocked for %s", elapsed)
	}
	if got, want := em.Dropped(), uint64(extra-queueSize); got != want {
		t.Fatalf("dropped = %d, want %d", got, want)
	}
}

// protects=a 5xx and a network error are each tried exactly twice, and 401 and 422 are tried once; fails_when=a 5xx is not retried, is retried more than once, or a 401/422 is retried; no_claim=the accepted retry was written to the audit log
func TestRetryPolicy(t *testing.T) {
	t.Run("5xx", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			if hits.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()
		em := New(Config{
			URL:           srv.URL,
			SigningKey:    testKey,
			BatchSize:     1,
			FlushInterval: time.Hour,
			RetryBackoff:  5 * time.Millisecond,
			Client:        testClient(srv),
		})
		em.Emit(Event{Method: "GET", Key: "retry", Status: 200, Decision: "allow"})
		finish(t, em)
		if got := hits.Load(); got != 2 {
			t.Fatalf("attempts = %d, want 2", got)
		}
	})

	t.Run("5xx gives up after four attempts", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			hits.Add(1)
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer srv.Close()
		em := New(Config{
			URL:           srv.URL,
			SigningKey:    testKey,
			BatchSize:     1,
			FlushInterval: time.Hour,
			RetryBackoff:  5 * time.Millisecond,
			Client:        testClient(srv),
		})
		em.Emit(Event{Method: "GET", Key: "give-up"})
		finish(t, em)
		if got := hits.Load(); got != 4 {
			t.Fatalf("attempts = %d, want 4", got)
		}
	})

	for _, code := range []int{http.StatusUnauthorized, http.StatusUnprocessableEntity} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				hits.Add(1)
				w.WriteHeader(code)
			}))
			defer srv.Close()
			em := New(Config{
				URL:           srv.URL,
				SigningKey:    testKey,
				BatchSize:     1,
				FlushInterval: time.Hour,
				RetryBackoff:  5 * time.Millisecond,
				Client:        testClient(srv),
			})
			em.Emit(Event{Method: "PUT", Key: "no-retry"})
			finish(t, em)
			if got := hits.Load(); got != 1 {
				t.Fatalf("status %d attempts = %d, want 1", code, got)
			}
		})
	}

	t.Run("network", func(t *testing.T) {
		var trips atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		base := testClient(srv).Transport
		if base == nil {
			base = http.DefaultTransport
		}
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if trips.Add(1) == 1 {
				return nil, errors.New("connection reset")
			}
			return base.RoundTrip(r)
		})}
		em := New(Config{
			URL:           srv.URL,
			SigningKey:    testKey,
			BatchSize:     1,
			FlushInterval: time.Hour,
			RetryBackoff:  5 * time.Millisecond,
			Client:        client,
		})
		em.Emit(Event{Method: "GET", Key: "net"})
		finish(t, em)
		if got := trips.Load(); got != 2 {
			t.Fatalf("round trips = %d, want 2", got)
		}
	})
}

// protects=a client timeout retries the same batch once while the emitter base context is alive;
// fails_when=a timed-out batch is dropped, retried more than once, or changed on retry;
// no_claim=the receiver durably stores or deduplicates the batch.
func TestClientTimeoutRetriesBatch(t *testing.T) {
	var hits atomic.Int32
	var delivered recorder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			_, _ = io.Copy(io.Discard, r.Body)
			time.Sleep(300 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			return
		}
		delivered.save(r)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	client := testClient(srv)
	client.Timeout = 100 * time.Millisecond
	em := New(Config{
		URL:           srv.URL,
		SigningKey:    testKey,
		BatchSize:     2,
		FlushInterval: time.Hour,
		RetryBackoff:  5 * time.Millisecond,
		Client:        client,
	})
	want := []Event{
		{Method: "GET", Key: "timeout-a", Status: 200, Decision: "allow"},
		{Method: "PUT", Key: "timeout-b", Status: 201, Decision: "allow"},
	}
	for _, ev := range want {
		em.Emit(ev)
	}
	finish(t, em)
	if got := hits.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
	delivered.check(t)
	snaps := delivered.list()
	if len(snaps) != 1 {
		t.Fatalf("delivered batches = %d, want 1", len(snaps))
	}
	got := verifySnap(t, testKey, snaps[0])
	if len(got) != len(want) {
		t.Fatalf("delivered events = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// protects=Close sends events that have not reached the interval yet, and returns when the deadline passes during a stuck send; fails_when=pending events wait for the ticker or Close waits out a blocked POST; no_claim=events still queued after a cancelled Close are delivered later
func TestCloseFlushesPending(t *testing.T) {
	t.Run("pending", func(t *testing.T) {
		rec := &recorder{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec.save(r)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		em := New(Config{
			URL:           srv.URL,
			SigningKey:    testKey,
			FlushInterval: time.Hour,
			Client:        testClient(srv),
		})
		want := []Event{
			{Kid: "k", Method: "GET", Bucket: "b", Key: "p1", Status: 200, Bytes: 1, Decision: "allow"},
			{Kid: "k", Method: "HEAD", Bucket: "b", Key: "p2", Status: 200, Bytes: 2, Decision: "allow"},
			{Kid: "k", Method: "DELETE", Bucket: "b", Key: "p3", Status: 204, Bytes: 3, Decision: "deny:method"},
		}
		for _, ev := range want {
			em.Emit(ev)
		}
		if len(rec.list()) != 0 {
			t.Fatal("flushed before Close")
		}
		start := time.Now()
		finish(t, em)
		if time.Since(start) > 500*time.Millisecond {
			t.Fatalf("Close waited %s", time.Since(start))
		}
		rec.check(t)
		var got []Event
		for _, s := range rec.list() {
			got = append(got, verifySnap(t, testKey, s)...)
		}
		if len(got) != len(want) {
			t.Fatalf("delivered %d", len(got))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("event %d = %+v", i, got[i])
			}
		}
	})

	t.Run("deadline", func(t *testing.T) {
		release := make(chan struct{})
		entered := make(chan struct{})
		var hits atomic.Int32
		var once sync.Once
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			once.Do(func() { close(entered) })
			<-release
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()
		defer close(release)
		em := New(Config{
			URL:           srv.URL,
			SigningKey:    testKey,
			BatchSize:     1,
			FlushInterval: time.Hour,
			RetryBackoff:  20 * time.Millisecond,
			Client:        &http.Client{Timeout: 30 * time.Second, Transport: testClient(srv).Transport},
		})
		em.Emit(Event{Method: "GET", Key: "blocked"})
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("request did not start")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := em.Close(ctx)
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("Close succeeded while the send was blocked")
		}
		if elapsed > time.Second {
			t.Fatalf("Close took %s", elapsed)
		}
		time.Sleep(80 * time.Millisecond)
		if got := hits.Load(); got != 1 {
			t.Fatalf("attempts after cancel = %d", got)
		}
	})
}

// protects=Emit after Close returns without panicking; fails_when=Emit panics once the flush loop has stopped; no_claim=the late event was queued or delivered
func TestEmitAfterClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	em := New(Config{
		URL:           srv.URL,
		SigningKey:    testKey,
		FlushInterval: time.Hour,
		Client:        testClient(srv),
	})
	em.Emit(Event{Method: "GET", Key: "before"})
	finish(t, em)
	defer func() {
		if recover() != nil {
			t.Fatal("Emit panicked after Close")
		}
	}()
	em.Emit(Event{Method: "PUT", Key: "after"})
	em.Emit(Event{})
	if em.Dropped() != 0 {
		t.Fatalf("dropped = %d", em.Dropped())
	}
}

// protects=concurrent Emit does not race and every event is sent or counted as dropped; fails_when=the race detector reports a race or events are lost; no_claim=delivery order matches call order or that the receiver deduplicated events
func TestConcurrentEmit(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.save(r)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	const workers = 8
	const each = 200
	em := New(Config{
		URL:          srv.URL,
		SigningKey:   testKey,
		QueueSize:    defaultQueue,
		RetryBackoff: time.Millisecond,
		Client:       testClient(srv),
	})
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(id int) {
			defer wg.Done()
			for n := 0; n < each; n++ {
				em.Emit(Event{Kid: strconv.Itoa(id), Method: "GET", Bucket: "b", Key: strconv.Itoa(n), Status: 200, Bytes: 1, Decision: "allow"})
			}
		}(w)
	}
	wg.Wait()
	finish(t, em)
	rec.check(t)

	var got int
	for _, s := range rec.list() {
		ev := verifySnap(t, testKey, s)
		if len(ev) > maxBatch {
			t.Fatalf("batch %d", len(ev))
		}
		got += len(ev)
	}
	total := workers * each
	if got+int(em.Dropped()) != total {
		t.Fatalf("sent %d + dropped %d != %d", got, em.Dropped(), total)
	}
}

// protects=Emit truncates oversized string fields on a UTF-8 boundary and still sends sibling events; fails_when=a 3000-byte field is queued whole, a multi-byte rune is split, or an oversized event drops the rest of the batch; no_claim=the backend's 422 limits match these caps or that a truncated key is the caller's original key
func TestTruncateBeforeQueue(t *testing.T) {
	limits := []struct {
		name  string
		limit int
		set   func(*Event, string)
		get   func(Event) string
	}{
		{"kid", maxKid, func(e *Event, s string) { e.Kid = s }, func(e Event) string { return e.Kid }},
		{"method", maxMethod, func(e *Event, s string) { e.Method = s }, func(e Event) string { return e.Method }},
		{"bucket", maxBucket, func(e *Event, s string) { e.Bucket = s }, func(e Event) string { return e.Bucket }},
		{"key", maxKey, func(e *Event, s string) { e.Key = s }, func(e Event) string { return e.Key }},
		{"decision", maxDecision, func(e *Event, s string) { e.Decision = s }, func(e Event) string { return e.Decision }},
	}

	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.save(r)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	em := New(Config{
		URL:           srv.URL,
		SigningKey:    testKey,
		FlushInterval: time.Hour,
		Client:        testClient(srv),
	})

	for _, field := range limits {
		ev := Event{Kid: "k", Method: "GET", Bucket: "b", Key: "obj", Status: 200, Bytes: 1, Decision: "allow"}
		ev = withField(field.set, ev, strings.Repeat("a", 3000))
		em.Emit(ev)
		// A 3-byte rune straddles the cap: limit-1 ASCII bytes, then '€'.
		ev = Event{Kid: "k", Method: "GET", Bucket: "b", Key: "obj", Status: 200, Bytes: 1, Decision: "allow"}
		ev = withField(field.set, ev, strings.Repeat("a", field.limit-1)+"€")
		em.Emit(ev)
	}
	em.Emit(Event{Kid: "low", Method: "GET", Bucket: "b", Key: "s", Status: 99, Bytes: -5, Decision: "allow"})
	em.Emit(Event{Kid: "high", Method: "GET", Bucket: "b", Key: "s", Status: 600, Bytes: 0, Decision: "allow"})
	em.Emit(Event{Kid: "ok", Method: "GET", Bucket: "b", Key: "s", Status: 100, Bytes: 4, Decision: "allow"})
	em.Emit(Event{Kid: "top", Method: "GET", Bucket: "b", Key: "s", Status: 599, Bytes: 8, Decision: "allow"})

	short := Event{Kid: "one", Method: "GET", Bucket: "pics", Key: "a.txt", Status: 200, Bytes: 3, Decision: "allow"}
	huge := Event{Kid: strings.Repeat("k", 3000), Method: strings.Repeat("m", 3000), Bucket: strings.Repeat("b", 3000), Key: strings.Repeat("x", 3000), Status: 201, Bytes: 9, Decision: strings.Repeat("d", 3000)}
	tail := Event{Kid: "three", Method: "HEAD", Bucket: "pics", Key: "c.txt", Status: 204, Bytes: 1, Decision: "deny:scope"}
	em.Emit(short)
	em.Emit(huge)
	em.Emit(tail)

	finish(t, em)
	rec.check(t)

	var got []Event
	for _, s := range rec.list() {
		got = append(got, verifySnap(t, testKey, s)...)
	}
	wantN := len(limits)*2 + 4 + 3
	if len(got) != wantN {
		t.Fatalf("delivered %d, want %d", len(got), wantN)
	}

	i := 0
	for _, field := range limits {
		long := field.get(got[i])
		i++
		if len(long) != field.limit || long != strings.Repeat("a", field.limit) {
			t.Fatalf("%s 3000-char len=%d value=%q", field.name, len(long), long)
		}
		cut := field.get(got[i])
		i++
		if cut != strings.Repeat("a", field.limit-1) || !utf8.ValidString(cut) {
			t.Fatalf("%s rune cut = %q (%d bytes)", field.name, cut, len(cut))
		}
	}

	if got[i].Status != 599 || got[i].Bytes != 0 || got[i].Kid != "low" {
		t.Fatalf("below range = %+v", got[i])
	}
	i++
	if got[i].Status != 599 || got[i].Bytes != 0 || got[i].Kid != "high" {
		t.Fatalf("above range = %+v", got[i])
	}
	i++
	if got[i].Status != 100 || got[i].Bytes != 4 {
		t.Fatalf("lower edge = %+v", got[i])
	}
	i++
	if got[i].Status != 599 || got[i].Bytes != 8 {
		t.Fatalf("upper edge = %+v", got[i])
	}
	i++

	if got[i] != short {
		t.Fatalf("sibling before = %+v", got[i])
	}
	mid := got[i+1]
	if mid.Kid != strings.Repeat("k", maxKid) || mid.Method != strings.Repeat("m", maxMethod) || mid.Bucket != strings.Repeat("b", maxBucket) || mid.Key != strings.Repeat("x", maxKey) || mid.Decision != strings.Repeat("d", maxDecision) {
		t.Fatalf("oversized event = %+v", mid)
	}
	if got[i+2] != tail {
		t.Fatalf("sibling after = %+v", got[i+2])
	}
}

func withField(set func(*Event, string), ev Event, value string) Event {
	set(&ev, value)
	return ev
}
