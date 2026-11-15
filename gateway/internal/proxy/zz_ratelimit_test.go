package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/imthi16/storzo/gateway/internal/verify"
)

type rateFakeClock struct{ nanos atomic.Int64 }

func newRateFakeClock() *rateFakeClock {
	c := &rateFakeClock{}
	c.nanos.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	return c
}
func (c *rateFakeClock) now() time.Time          { return time.Unix(0, c.nanos.Load()) }
func (c *rateFakeClock) advance(d time.Duration) { c.nanos.Add(int64(d)) }

func rateRequest(t *testing.T, kid, method, path string, master bool) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, "http://gateway.test"+path, nil)
	r.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
	var creds aws.Credentials
	if master {
		creds = aws.Credentials{AccessKeyID: masterKey, SecretAccessKey: masterSecret}
	} else {
		now := time.Now().Unix()
		claims := verify.Claims{Kid: kid, Bucket: "agent-data", Prefix: "run-42/", Mode: "rw", Iat: now - 1, Exp: now + 900, Jti: "rate-fixture"}
		raw, err := json.Marshal(claims)
		if err != nil {
			t.Fatal(err)
		}
		body := base64.RawURLEncoding.EncodeToString(raw)
		mac := hmac.New(sha256.New, []byte(signingKey))
		_, _ = mac.Write([]byte(body))
		v, err := verify.New(signingKey)
		if err != nil {
			t.Fatal(err)
		}
		creds = aws.Credentials{AccessKeyID: kid, SecretAccessKey: v.Secret(kid), SessionToken: body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))}
	}
	signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
	if err := signer.SignHTTP(r.Context(), creds, r, "UNSIGNED-PAYLOAD", "s3", "us-east-1", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	return r
}

func rateFixture(t *testing.T, rate float64, burst int) (*Handler, *rateFakeClock, *atomic.Int64, *auditLog) {
	t.Helper()
	calls := &atomic.Int64{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		checkMaster(t, r)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	clock := newRateFakeClock()
	cfg := config(upstream.URL)
	cfg.RateLimit, cfg.RateBurst, cfg.RateClock = rate, burst, clock.now
	log := &auditLog{}
	h, err := New(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	return h, clock, calls, log
}

func rateServe(h *Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func requireSlowDown(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" ||
		!strings.Contains(w.Body.String(), "<Code>SlowDown</Code>") ||
		!strings.Contains(w.Body.String(), "<Message>Please reduce your request rate.</Message>") {
		t.Fatalf("expected 503 SlowDown: status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
	}
}

// protects=HEAD/multipart requests spend one token each, exhausted buckets emit one denial, and fractional refill restores service;
// fails_when=burst/refill counts differ, SlowDown lacks XML/Retry-After, a blocked request forwards, or audit counts/status/decision differ;
// no_claim=distributed quotas across gateway replicas or byte-throughput limits.
func TestRateBurstRefillAndAudit(t *testing.T) {
	h, clock, calls, log := rateFixture(t, 2, 2)
	for _, request := range []struct{ method, path string }{
		{"HEAD", "/agent-data/run-42/key"},
		{"PUT", "/agent-data/run-42/key?uploadId=part-upload&partNumber=1"},
	} {
		w := rateServe(h, rateRequest(t, agentKey, request.method, request.path, false))
		if w.Code != 200 {
			t.Fatalf("initial burst status=%d body=%s", w.Code, w.Body.String())
		}
	}
	w := rateServe(h, rateRequest(t, agentKey, "GET", "/agent-data/run-42/key", false))
	requireSlowDown(t, w)
	if calls.Load() != 2 {
		t.Fatalf("blocked request forwarded: calls=%d", calls.Load())
	}
	log.mu.Lock()
	if len(log.events) != 3 || log.events[2].Status != 503 || log.events[2].Decision != "deny" || log.events[2].Kid != agentKey {
		t.Errorf("rate-limit audit=%+v", log.events)
	}
	log.mu.Unlock()
	clock.advance(250 * time.Millisecond)
	requireSlowDown(t, rateServe(h, rateRequest(t, agentKey, "GET", "/agent-data/run-42/key", false)))
	clock.advance(250 * time.Millisecond)
	if w := rateServe(h, rateRequest(t, agentKey, "GET", "/agent-data/run-42/key", false)); w.Code != 200 {
		t.Fatalf("refill did not restore service: %d", w.Code)
	}
	requireSlowDown(t, rateServe(h, rateRequest(t, agentKey, "GET", "/agent-data/run-42/key", false)))
	if calls.Load() != 3 {
		t.Fatalf("refill allowed wrong request count: %d", calls.Load())
	}
}

// protects=verified credential kids have independent buckets;
// fails_when=exhausting one kid rejects another or grants either an extra token;
// no_claim=quotas shared by separate credentials belonging to the same operator.
func TestRateKidsIndependent(t *testing.T) {
	h, _, calls, _ := rateFixture(t, 1, 1)
	for _, kid := range []string{agentKey, "STZBBBBBBBBBBBBBBBB"} {
		if w := rateServe(h, rateRequest(t, kid, "GET", "/agent-data/run-42/key", false)); w.Code != 200 {
			t.Fatalf("new kid %s rejected: %d", kid, w.Code)
		}
		requireSlowDown(t, rateServe(h, rateRequest(t, kid, "GET", "/agent-data/run-42/key", false)))
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d, want 2", calls.Load())
	}
}

// protects=verified master requests bypass the scoped limiter and allocate no bucket;
// fails_when=any master request is throttled or creates rate state;
// no_claim=the master key is safe to distribute to untrusted agents.
func TestRateMasterExempt(t *testing.T) {
	h, _, calls, _ := rateFixture(t, 1, 1)
	for i := 0; i < 10; i++ {
		w := rateServe(h, rateRequest(t, masterKey, "GET", "/outside/key", true))
		if w.Code != 200 {
			t.Fatalf("master request %d throttled: status=%d body=%s", i, w.Code, w.Body.String())
		}
	}
	if calls.Load() != 10 || len(h.limiter.buckets) != 0 {
		t.Fatalf("master calls=%d buckets=%d", calls.Load(), len(h.limiter.buckets))
	}
}

// protects=forged, unauthenticated and out-of-scope requests cannot spend a victim's tokens;
// fails_when=claimed identity is limited before verification, or the victim's first legitimate request is throttled;
// no_claim=rate limiting protects signature-verification CPU or limits unauthenticated audit events.
func TestRateForgeryCannotDrainVictim(t *testing.T) {
	h, _, calls, _ := rateFixture(t, 1, 1)
	for i := 0; i < 30; i++ {
		r := rateRequest(t, agentKey, "GET", "/agent-data/run-42/key", false)
		switch i % 3 {
		case 0:
			auth, _, _ := strings.Cut(r.Header.Get("Authorization"), ", Signature=")
			r.Header.Set("Authorization", auth+", Signature="+strings.Repeat("0", 64))
		case 1:
			r.Header.Del("Authorization")
		case 2:
			r = rateRequest(t, agentKey, "GET", "/agent-data/other-prefix/key", false)
		}
		if w := rateServe(h, r); w.Code != 403 {
			t.Errorf("invalid request %d should fail Verify: status=%d", i, w.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid requests forwarded: %d", calls.Load())
	}
	if len(h.limiter.buckets) != 0 {
		t.Errorf("unverified requests allocated rate state: %d buckets", len(h.limiter.buckets))
	}
	if w := rateServe(h, rateRequest(t, agentKey, "GET", "/agent-data/run-42/key", false)); w.Code != 200 {
		t.Fatalf("victim bucket drained by invalid requests: status=%d body=%s", w.Code, w.Body.String())
	}
}

// protects=rate zero disables limiting and allocates no state;
// fails_when=disabled mode emits SlowDown or retains a bucket map;
// no_claim=disabled gateways have any request-rate bound.
func TestRateDisabled(t *testing.T) {
	h, _, calls, _ := rateFixture(t, 0, 1)
	for i := 0; i < 10; i++ {
		if w := rateServe(h, rateRequest(t, agentKey, "GET", "/agent-data/run-42/key", false)); w.Code != 200 {
			t.Fatalf("disabled limit rejected request %d: %d", i, w.Code)
		}
	}
	if calls.Load() != 10 || h.limiter != nil {
		t.Fatalf("disabled mode: calls=%d limiter=%v", calls.Load(), h.limiter)
	}
}

// protects=defaults are 50/s with burst 100, zero disables, and invalid rate settings fail configuration before startup;
// fails_when=invalid/NaN/infinite settings pass or an error fails to name its variable;
// no_claim=deployment-provided environment values match the intended capacity.
func TestRateEnvironment(t *testing.T) {
	for key, value := range map[string]string{
		"STORAGE_ENDPOINT": "http://storage:8333", "STORAGE_ACCESS_KEY": masterKey,
		"STORAGE_SECRET_KEY": masterSecret, "CRED_SIGNING_KEY": signingKey,
		"AUDIT_URL": "http://backend:8000/api/internal/audit", "GATEWAY_MAX_HASHED_BODY_MB": "",
		"GATEWAY_RATE_LIMIT": "", "GATEWAY_RATE_BURST": "",
	} {
		t.Setenv(key, value)
	}
	cfg, err := ConfigFromEnv()
	if err != nil || cfg.RateLimit != 50 || cfg.RateBurst != 100 {
		t.Fatalf("default rate config: rate=%v burst=%d err=%v", cfg.RateLimit, cfg.RateBurst, err)
	}
	for _, tc := range []struct{ rate, burst, variable string }{
		{"-1", "1", "GATEWAY_RATE_LIMIT"}, {"bad", "1", "GATEWAY_RATE_LIMIT"},
		{"NaN", "1", "GATEWAY_RATE_LIMIT"}, {"Inf", "1", "GATEWAY_RATE_LIMIT"},
		{"1e999", "1", "GATEWAY_RATE_LIMIT"}, {"1", "0", "GATEWAY_RATE_BURST"},
		{"1", "-1", "GATEWAY_RATE_BURST"}, {"1", "bad", "GATEWAY_RATE_BURST"},
		{"1", "1.5", "GATEWAY_RATE_BURST"}, {"0", "-1", "GATEWAY_RATE_BURST"},
		{"1", "999999999999999999999", "GATEWAY_RATE_BURST"},
	} {
		t.Run(tc.rate+"/"+tc.burst, func(t *testing.T) {
			t.Setenv("GATEWAY_RATE_LIMIT", tc.rate)
			t.Setenv("GATEWAY_RATE_BURST", tc.burst)
			if _, err := ConfigFromEnv(); err == nil || !strings.Contains(err.Error(), tc.variable) {
				t.Fatalf("expected %s configuration error, got %v", tc.variable, err)
			}
		})
	}
	t.Setenv("GATEWAY_RATE_LIMIT", "0")
	t.Setenv("GATEWAY_RATE_BURST", "0")
	cfg, err = ConfigFromEnv()
	if err != nil || cfg.RateLimit != 0 {
		t.Fatalf("disabled environment: %+v err=%v", cfg, err)
	}
	t.Setenv("GATEWAY_RATE_LIMIT", "0.5")
	t.Setenv("GATEWAY_RATE_BURST", "1")
	if _, err := ConfigFromEnv(); err != nil {
		t.Fatalf("valid fractional rate rejected: %v", err)
	}
}

// protects=idle expiry starts when a bucket becomes full, and expiry order can differ from LRU order;
// fails_when=state is evicted before five full-idle minutes or an eligible entry remains;
// no_claim=expiry runs without incoming requests (eviction is lazy).
func TestRateIdleEviction(t *testing.T) {
	clock := newRateFakeClock()
	l := newRateLimiter(1, 10, clock.now)
	for i := 0; i < 10; i++ {
		if !l.allow("older-empty") {
			t.Fatal("fixture burst rejected")
		}
	}
	clock.advance(2 * time.Second)
	l.allow("newer-nearly-full")
	clock.advance(5 * time.Minute)
	l.allow("sweep-before")
	if l.buckets["newer-nearly-full"] == nil || l.buckets["older-empty"] == nil {
		t.Fatal("evicted before a full five minutes at capacity")
	}
	clock.advance(time.Second)
	l.allow("sweep-after")
	if l.buckets["newer-nearly-full"] != nil || l.buckets["older-empty"] == nil {
		t.Fatalf("wrong expiry order: buckets=%v", l.buckets)
	}
	clock.advance(7 * time.Second)
	l.allow("sweep-older")
	if l.buckets["older-empty"] != nil {
		t.Fatal("full and idle entry was not evicted")
	}
}

// protects=10,001 distinct kids cannot grow any bookkeeping past 10,000 and capacity eviction uses LRU;
// fails_when=map/heap/list grow past the cap or a recently touched kid is evicted instead of the oldest;
// no_claim=evicted credentials retain their previous token debt.
func TestRateCapacityAndLRU(t *testing.T) {
	clock := newRateFakeClock()
	l := newRateLimiter(1, 2, clock.now)
	for i := 0; i < 10000; i++ {
		l.allow(fmt.Sprintf("kid-%d", i))
	}
	l.allow("kid-0")
	l.allow("kid-10000")
	if len(l.buckets) > 10000 || len(l.expiry) > 10000 || l.recent.Len() > 10000 {
		t.Fatalf("rate state exceeds cap: map=%d heap=%d LRU=%d", len(l.buckets), len(l.expiry), l.recent.Len())
	}
	if l.buckets["kid-0"] == nil || l.buckets["kid-10000"] == nil || l.buckets["kid-1"] != nil {
		t.Fatal("capacity eviction did not preserve the most recently used kids")
	}
}

// protects=concurrent verified requests atomically consume the burst and each emit one audit event;
// fails_when=the race detector fires, too many requests forward, or allowed/denied/audit totals differ;
// no_claim=multi-process rate coordination or request ordering.
func TestRateConcurrentRequests(t *testing.T) {
	h, _, calls, log := rateFixture(t, 1, 25)
	const total = 200
	requests := make([]*http.Request, total)
	for i := range requests {
		requests[i] = rateRequest(t, agentKey, "GET", "/agent-data/run-42/key", false)
	}
	var allowed, denied atomic.Int64
	var wg sync.WaitGroup
	for _, r := range requests {
		wg.Add(1)
		go func(r *http.Request) {
			defer wg.Done()
			w := rateServe(h, r)
			switch w.Code {
			case 200:
				allowed.Add(1)
			case 503:
				denied.Add(1)
			default:
				t.Errorf("unexpected concurrent status=%d", w.Code)
			}
		}(r)
	}
	wg.Wait()
	log.mu.Lock()
	defer log.mu.Unlock()
	if allowed.Load() != 25 || denied.Load() != total-25 || calls.Load() != 25 || len(log.events) != total {
		t.Fatalf("allowed=%d denied=%d upstream=%d audits=%d", allowed.Load(), denied.Load(), calls.Load(), len(log.events))
	}
}
