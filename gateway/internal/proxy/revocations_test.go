package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imthi16/storzo/gateway/internal/audit"
)

// protects=only scoped revoked keys are denied, with kid in audit even after poll failure;
// fails_when=a revoked key reaches storage or a healthy key is blocked;
// no_claim=live backend networking.
func TestRevocationSnapshotAndAuditedDenial(t *testing.T) {
	var state atomic.Int32
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.RequestURI() != "/api/internal/revoked?since=0" {
			t.Error("unexpected poll request")
		}
		switch state.Load() {
		case 0:
			_, _ = io.WriteString(w, `{"kids":[],"now":1}`)
		case 1:
			_, _ = io.WriteString(w, `{"kids":["`+agentKey+`","`+masterKey+`"],"now":2}`)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer feed.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	cache, err := audit.NewRevocations(feed.URL+"/api/internal/audit", signingKey, 0)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config(upstream.URL)
	cfg.Revocations = cache
	log := &auditLog{}
	handler, err := New(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		state  int32
		master bool
		status int
	}{{0, false, 200}, {1, false, 403}, {2, false, 403}, {2, true, 200}} {
		state.Store(tc.state)
		cache.Refresh(context.Background())
		r := sign(t, "GET", "/agent-data/run-42/a", "", "", tc.master, false, false, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("state %d master %t: got %d, want %d", tc.state, tc.master, w.Code, tc.status)
		}
		if tc.status == 403 {
			ev := log.events[len(log.events)-1]
			if ev.Kid != agentKey || ev.Status != 403 || ev.Decision != "deny" || !strings.Contains(w.Body.String(), "AccessDenied") {
				t.Fatalf("revocation denial: %+v, %s", ev, w.Body.String())
			}
		}
	}
}

// protects=stale or missing revocation snapshots fail closed for scoped keys with
// 503 and an audit event, master keys keep working, revocation beats staleness,
// and a brief outage within the bound still serves;
// fails_when=a stale snapshot serves scoped requests, a denial reaches storage or
// misses its audit, or master requests are blocked;
// no_claim=live backend networking.
func TestRevocationStalenessFailsClosed(t *testing.T) {
	var down, revoked atomic.Bool
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		kids := "[]"
		if revoked.Load() {
			kids = `["` + agentKey + `"]`
		}
		_, _ = io.WriteString(w, `{"kids":`+kids+`,"now":1}`)
	}))
	defer feed.Close()
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	ctx := context.Background()
	stale, err := audit.NewRevocations(feed.URL+"/api/internal/audit", signingKey, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := audit.NewRevocations(feed.URL+"/api/internal/audit", signingKey, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		cache  *audit.Revocations
		setup  func()
		master bool
		status int
		code   string
	}{
		{name: "never-refreshed", cache: stale, status: 503, code: "ServiceUnavailable"},
		{name: "never-refreshed-master", cache: stale, master: true, status: 200},
		{name: "refreshed-but-stale", cache: stale, setup: func() { stale.Refresh(ctx) }, status: 503, code: "ServiceUnavailable"},
		{name: "revoked-beats-stale", cache: stale, setup: func() { revoked.Store(true); stale.Refresh(ctx) }, status: 403, code: "AccessDenied"},
		{name: "fresh-snapshot", cache: fresh, setup: func() { revoked.Store(false); fresh.Refresh(ctx) }, status: 200},
		{name: "brief-outage-within-bound", cache: fresh, setup: func() { down.Store(true); fresh.Refresh(ctx) }, status: 200},
		{name: "brief-outage-master", cache: fresh, master: true, status: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setup != nil {
				tc.setup()
			}
			cfg := config(upstream.URL)
			cfg.Revocations = tc.cache
			log := &auditLog{}
			handler, err := New(cfg, log)
			if err != nil {
				t.Fatal(err)
			}
			before := calls.Load()
			r := sign(t, "GET", "/agent-data/run-42/a", "", "", tc.master, false, false, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.code != "" && !strings.Contains(w.Body.String(), "<Code>"+tc.code+"</Code>") {
				t.Fatalf("XML error=%s", w.Body.String())
			}
			wantCalls := int32(0)
			decision := "deny"
			if tc.status == 200 {
				wantCalls = 1
				decision = "allow"
			}
			if calls.Load()-before != wantCalls {
				t.Fatalf("upstream calls=%d, want %d", calls.Load()-before, wantCalls)
			}
			kid := agentKey
			if tc.master {
				kid = masterKey
			}
			if len(log.events) != 1 || log.events[0].Kid != kid || log.events[0].Status != tc.status || log.events[0].Decision != decision {
				t.Fatalf("audit=%+v", log.events)
			}
		})
	}
}
