package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

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
	cache, err := audit.NewRevocations(feed.URL+"/api/internal/audit", signingKey)
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
