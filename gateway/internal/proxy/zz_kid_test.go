package proxy

import (
	"net/http"
	"strings"
	"testing"
)

// protects=post-authentication scope and master CopyObject denials audit the verified kid without forwarding or spending rate tokens;
// fails_when=identity is lost, a denial forwards, its audit count/decision/status changes, or a scope denial spends a token;
// no_claim=the attempted target was authorized.
func TestProxyKidAuthenticatedDenials(t *testing.T) {
	h, _, calls, log := rateFixture(t, 1, 1)
	for _, tc := range []struct {
		name, method, path, kid string
		status                  int
		master                  bool
		copy                    bool
	}{
		{"prefix", "GET", "/agent-data/outside/key", agentKey, 403, false, false},
		{"bucket", "GET", "/other-bucket/run-42/key", agentKey, 403, false, false},
		{"list", "GET", "/agent-data?list-type=2", agentKey, 403, false, false},
		{"read-only-put", "PUT", "/agent-data/run-42/key", agentKey, 403, false, false},
		{"copy", "PUT", "/agent-data/run-42/key", agentKey, 501, false, true},
		{"master-copy", "PUT", "/outside/key", masterKey, 501, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := rateRequest(t, tc.kid, tc.method, tc.path, tc.master)
			if tc.name == "read-only-put" {
				r = sign(t, tc.method, tc.path, "", "UNSIGNED-PAYLOAD", false, false, false, nil)
			}
			if tc.copy {
				r.Header.Set("X-Amz-Copy-Source", "/agent-data/run-42/source")
			}
			log.mu.Lock()
			before := len(log.events)
			log.mu.Unlock()
			w := rateServe(h, r)
			log.mu.Lock()
			defer log.mu.Unlock()
			if w.Code != tc.status || calls.Load() != 0 || len(log.events) != before+1 {
				t.Fatalf("status=%d upstream=%d events=%+v", w.Code, calls.Load(), log.events)
			}
			event := log.events[before]
			if event.Kid != tc.kid || event.Decision != "deny" || event.Status != tc.status {
				t.Fatalf("authenticated denial audit=%+v", event)
			}
		})
	}
	if len(h.limiter.buckets) != 0 {
		t.Fatalf("scope denials allocated rate state: %d", len(h.limiter.buckets))
	}
}

// protects=forged and missing authentication never attribute denials to a claimed or placeholder identity;
// fails_when=Credential input or unconditional denial recording populates Kid or reaches storage;
// no_claim=unauthenticated request headers identify any principal.
func TestProxyKidAuthenticationFailures(t *testing.T) {
	h, _, calls, log := rateFixture(t, 1, 1)
	for _, name := range []string{"forged-victim", "missing-auth", "expired", "missing-token", "duplicate-token", "tampered-token"} {
		t.Run(name, func(t *testing.T) {
			r := sign(t, "GET", "/agent-data/run-42/key", "", "UNSIGNED-PAYLOAD", false, false, name == "expired", nil)
			switch name {
			case "forged-victim":
				auth, _, _ := strings.Cut(r.Header.Get("Authorization"), ", Signature=")
				r.Header.Set("Authorization", auth+", Signature="+strings.Repeat("0", 64))
			case "missing-auth":
				r.Header.Del("Authorization")
			case "missing-token":
				r.Header.Del("X-Amz-Security-Token")
			case "duplicate-token":
				r.Header.Add("X-Amz-Security-Token", r.Header.Get("X-Amz-Security-Token"))
			case "tampered-token":
				r.Header.Set("X-Amz-Security-Token", "forged.token")
			}
			log.mu.Lock()
			before := len(log.events)
			log.mu.Unlock()
			w := rateServe(h, r)
			log.mu.Lock()
			defer log.mu.Unlock()
			if w.Code != http.StatusForbidden || calls.Load() != 0 || len(log.events) != before+1 {
				t.Fatalf("status=%d calls=%d events=%+v", w.Code, calls.Load(), log.events)
			}
			event := log.events[before]
			if event.Kid != "" || event.Status != 403 || event.Decision != "deny" {
				t.Fatalf("unauthenticated denial audit=%+v", event)
			}
		})
	}
}

// protects=allowed requests and SlowDown continue auditing the verified identity, with exactly one token spent per allowed request;
// fails_when=success loses its kid, rate-limit denial loses its kid, or SlowDown forwards;
// no_claim=rate state shared across replicas.
func TestProxyKidAllowedAndRateLimited(t *testing.T) {
	h, _, calls, log := rateFixture(t, 1, 1)
	for i := 0; i < 2; i++ {
		w := rateServe(h, rateRequest(t, agentKey, "GET", "/agent-data/run-42/key", false))
		if i == 0 && w.Code != 200 {
			t.Fatalf("allowed status=%d", w.Code)
		}
		if i == 1 {
			requireSlowDown(t, w)
		}
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if calls.Load() != 1 || len(log.events) != 2 {
		t.Fatalf("upstream=%d events=%+v", calls.Load(), log.events)
	}
	if a, d := log.events[0], log.events[1]; a.Kid != agentKey || a.Status != 200 || a.Decision != "allow" ||
		d.Kid != agentKey || d.Status != 503 || d.Decision != "deny" {
		t.Fatalf("allow/rate-limit audit=%+v", log.events)
	}
}
