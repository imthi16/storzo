package audit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// protects=the staleness bound is measured from the last successful snapshot,
// a never-refreshed cache is stale, and the default bound is 60s;
// fails_when=a failed poll resets the clock or Stale ignores the bound;
// no_claim=live backend networking.
func TestRevocationStaleBound(t *testing.T) {
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.RequestURI() != "/revoked?since=0" {
			t.Errorf("unexpected poll request: %s %s", r.Method, r.URL.RequestURI())
		}
		if r.Header.Get(headerSignature) != signature([]byte(testKey), r.Header.Get(headerTimestamp), nil) {
			t.Error("poll request is not HMAC-signed")
		}
		_, _ = w.Write([]byte(`{"kids":["revoked-kid"],"now":1}`))
	}))
	defer feed.Close()
	r, err := NewRevocations(feed.URL+"/audit", testKey, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Stale(time.Now()) || r.Contains("revoked-kid") {
		t.Fatal("never-refreshed cache must be stale and empty")
	}
	r.Refresh(context.Background())
	now := time.Now()
	if r.Stale(now) || !r.Contains("revoked-kid") {
		t.Fatal("successful refresh must clear staleness and load the snapshot")
	}
	if r.Stale(now.Add(30*time.Minute)) || !r.Stale(now.Add(90*time.Minute)) {
		t.Fatal("staleness must follow the configured bound")
	}
	def, err := NewRevocations(feed.URL+"/audit", testKey, 0)
	if err != nil {
		t.Fatal(err)
	}
	if def.maxStale != DefaultRevocationMaxStale || DefaultRevocationMaxStale != 60*time.Second {
		t.Fatalf("default bound = %v", def.maxStale)
	}
}

// protects=failed and invalid polls keep the last snapshot without resetting
// the freshness clock, and a later healthy poll clears staleness;
// fails_when=a failed poll refreshes lastSuccess (fail-open forever) or drops the snapshot;
// no_claim=poll scheduling or live backend networking.
func TestRevocationFailureKeepsSnapshotAndClock(t *testing.T) {
	var state atomic.Int32 // 0 healthy, 1 HTTP 503, 2 invalid snapshot
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch state.Load() {
		case 0:
			_, _ = w.Write([]byte(`{"kids":["revoked-kid"],"now":1}`))
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			_, _ = w.Write([]byte(`{"now":1}`))
		}
	}))
	defer feed.Close()
	r, err := NewRevocations(feed.URL+"/audit", testKey, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	r.Refresh(context.Background())
	if r.Stale(time.Now()) || !r.Contains("revoked-kid") {
		t.Fatal("healthy refresh must be fresh")
	}
	time.Sleep(30 * time.Millisecond)
	if !r.Stale(time.Now()) {
		t.Fatal("snapshot older than the bound must be stale")
	}
	for _, failure := range []int32{1, 2} {
		state.Store(failure)
		r.Refresh(context.Background())
		if !r.Stale(time.Now()) || !r.Contains("revoked-kid") {
			t.Fatalf("failed poll %d reset the clock or dropped the snapshot", failure)
		}
	}
	state.Store(0)
	r.Refresh(context.Background())
	if r.Stale(time.Now()) {
		t.Fatal("recovered poll must clear staleness")
	}
}
