package audit

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// protects=two transient 5xx/429 failures do not lose a batch, including when Close drains it;
// fails_when=delivery stops after one retry, the batch changes, or recovered events count as failed;
// no_claim=receiver-side durability or duplicate suppression after ambiguous network failures.
func TestZZRetryTwiceThenDeliver(t *testing.T) {
	for _, status := range []int{500, 429} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var hits atomic.Int32
			var rec recorder
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if hits.Add(1) <= 2 {
					_, _ = io.Copy(io.Discard, r.Body)
					w.WriteHeader(status)
					return
				}
				rec.save(r)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()
			em := New(Config{URL: srv.URL, SigningKey: testKey, BatchSize: 3, FlushInterval: time.Hour, Client: srv.Client()})
			defer em.cancel()
			want := []Event{
				{Key: "a", Method: "GET", Status: 200},
				{Key: "b", Method: "PUT", Status: 201},
				{Key: "c", Method: "DELETE", Status: 204},
			}
			for _, event := range want {
				em.Emit(event)
			}
			finish(t, em)
			if hits.Load() != 3 || em.Failed() != 0 || em.Dropped() != 0 {
				t.Fatalf("hits=%d failed=%d dropped=%d", hits.Load(), em.Failed(), em.Dropped())
			}
			rec.check(t)
			snaps := rec.list()
			if len(snaps) != 1 {
				t.Fatalf("accepted batches=%d", len(snaps))
			}
			got := verifySnap(t, testKey, snaps[0])
			if len(got) != len(want) {
				t.Fatalf("events=%d, want %d", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("event %d=%+v want=%+v", i, got[i], want[i])
				}
			}
		})
	}
}

// protects=four failed attempts account for every event independently of queue-full drops;
// fails_when=a permanent 500 is retried fewer/more than four times or its batch disappears from counters;
// no_claim=events counted as failed can be recovered after process exit.
func TestZZPermanentFailureCountsEvents(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	em := New(Config{URL: srv.URL, SigningKey: testKey, BatchSize: 2, FlushInterval: time.Hour, Client: srv.Client()})
	defer em.cancel()
	em.Emit(Event{Key: "first", Status: 200})
	em.Emit(Event{Key: "second", Status: 200})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := em.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 4 || em.Failed() != 2 || em.Dropped() != 0 {
		t.Fatalf("attempts=%d failed=%d dropped=%d, want 4/2/0", hits.Load(), em.Failed(), em.Dropped())
	}
}

// protects=non-retryable 4xx failures are counted once, and Failed on a nil emitter is safe;
// fails_when=a 400 is retried or its event is silently lost;
// no_claim=all receiver validation failures use status 400.
func TestZZRejectedBatchIsFailed(t *testing.T) {
	var nilEmitter *Emitter
	if nilEmitter.Failed() != 0 {
		t.Fatal("nil Failed must be zero")
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	em := New(Config{URL: srv.URL, SigningKey: testKey, BatchSize: 1, Client: srv.Client()})
	defer em.cancel()
	em.Emit(Event{Status: 200})
	finish(t, em)
	if hits.Load() != 1 || em.Failed() != 1 || em.Dropped() != 0 {
		t.Fatalf("attempts=%d failed=%d dropped=%d", hits.Load(), em.Failed(), em.Dropped())
	}
}

// protects=Close keeps the base context alive until the in-flight batch and queued events reach a slow receiver;
// fails_when=Close cancels early, exits before delivery, or skips queued events;
// no_claim=delivery can finish after the caller deadline or under a permanent receiver outage.
func TestZZCloseDrainsSlowReceiver(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var first sync.Once
	var rec recorder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first.Do(func() { close(entered); <-release })
		time.Sleep(20 * time.Millisecond)
		rec.save(r)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer unblock()
	em := New(Config{URL: srv.URL, SigningKey: testKey, BatchSize: 1, FlushInterval: time.Hour, Client: srv.Client()})
	defer em.cancel()
	em.Emit(Event{Key: "first", Status: 200})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first batch never started")
	}
	em.Emit(Event{Key: "second", Status: 200})
	em.Emit(Event{Key: "third", Status: 200})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- em.Close(ctx) }()
	<-em.stop
	if err := em.base.Err(); err != nil {
		t.Fatalf("Close cancelled before draining: %v", err)
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Close did not finish draining")
	}
	rec.check(t)
	snaps := rec.list()
	if len(snaps) != 3 || em.Failed() != 0 {
		t.Fatalf("batches=%d failed=%d", len(snaps), em.Failed())
	}
	for i, key := range []string{"first", "second", "third"} {
		events := verifySnap(t, testKey, snaps[i])
		if len(events) != 1 || events[0].Key != key {
			t.Fatalf("batch %d=%+v", i, events)
		}
	}
}

// protects=an expired Close returns promptly, accounts for pending events and terminates its goroutine;
// fails_when=cancellation leaves a send blocked, loses events from counters, or repeated closes grow goroutines;
// no_claim=custom transports that ignore request cancellation can be forcibly stopped.
func TestZZExpiredCloseTerminatesAndCounts(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		em := New(Config{
			URL: "http://audit.invalid/", SigningKey: testKey, FlushInterval: time.Hour,
			Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				<-r.Context().Done()
				return nil, r.Context().Err()
			})},
		})
		t.Cleanup(em.cancel)
		for n := 0; n < 3; n++ {
			em.Emit(Event{Status: 200})
		}
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		start := time.Now()
		err := em.Close(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
			t.Fatalf("expired Close: err=%v elapsed=%s", err, time.Since(start))
		}
		select {
		case <-em.done:
		case <-time.After(time.Second):
			t.Fatal("flush goroutine survived expired Close")
		}
		if em.Failed() != 3 || em.Dropped() != 0 {
			t.Fatalf("abandoned events: failed=%d dropped=%d, want 3/0", em.Failed(), em.Dropped())
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutines grew after 20 expired closes: before=%d after=%d", before, after)
	}
}
