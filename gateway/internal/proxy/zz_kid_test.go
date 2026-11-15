package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/imthi16/storzo/gateway/internal/verify"
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

// protects=stale/future dates with invalid signatures cannot name a scoped or master principal in audit;
// fails_when=denials have a kid, wrong status/code, duplicate events, or any upstream call;
// no_claim=wall-clock boundary precision; dates use margins outside the validity windows.
func TestProxyKidDateWindowBadSignatureEmpty(t *testing.T) {
	h, _, calls, log := rateFixture(t, 1, 1)
	for _, master := range []bool{false, true} {
		name := "scoped"
		kid := agentKey
		if master {
			name, kid = "master", masterKey
		}
		for _, tc := range []struct {
			name   string
			offset time.Duration
		}{{"stale", -time.Hour}, {"future", verify.ExpirySkew + time.Minute}} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				r := rateRequest(t, kid, "GET", "/agent-data/run-42/key", master)
				creds := aws.Credentials{AccessKeyID: kid, SecretAccessKey: "incorrect-signing-secret", SessionToken: r.Header.Get("X-Amz-Security-Token")}
				if err := v4.NewSigner().SignHTTP(r.Context(), creds, r, "UNSIGNED-PAYLOAD", "s3", "us-east-1", time.Now().UTC().Add(tc.offset)); err != nil {
					t.Fatal(err)
				}
				log.mu.Lock()
				before := len(log.events)
				log.mu.Unlock()
				w := rateServe(h, r)
				log.mu.Lock()
				defer log.mu.Unlock()
				if w.Code != 403 || !strings.Contains(w.Body.String(), "<Code>SignatureDoesNotMatch</Code>") ||
					calls.Load() != 0 || len(log.events) != before+1 {
					t.Fatalf("status=%d upstream=%d events=%+v", w.Code, calls.Load(), log.events)
				}
				event := log.events[before]
				if event.Kid != "" || event.Status != 403 || event.Decision != "deny" {
					t.Fatalf("date-window denial audit=%+v", event)
				}
			})
		}
	}
}

type kidFailingBody struct{ reads int }

func (r *kidFailingBody) Read([]byte) (int, error) {
	r.reads++
	return 0, io.ErrUnexpectedEOF
}

// protects=implicit payload-read failures leave audit Kid empty for both credential classes;
// fails_when=an implicit read error attributes identity, never reads, changes status/code, or forwards;
// no_claim=body reads are bounded without the proxy's configured cap.
func TestProxyKidImplicitReadFailureEmpty(t *testing.T) {
	h, _, calls, log := rateFixture(t, 1, 1)
	sum := sha256.Sum256([]byte("hello"))
	hash := hex.EncodeToString(sum[:])
	for _, master := range []bool{false, true} {
		name := "scoped"
		creds := aws.Credentials{AccessKeyID: agentKey, SessionToken: token(t, "rw", false)}
		v, err := verify.New(signingKey)
		if err != nil {
			t.Fatal(err)
		}
		creds.SecretAccessKey = v.Secret(agentKey)
		if master {
			name = "master"
			creds = aws.Credentials{AccessKeyID: masterKey, SecretAccessKey: masterSecret}
		}
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest("PUT", "http://gateway.test/agent-data/run-42/key", strings.NewReader("hello"))
			r.Header.Del("X-Amz-Content-Sha256")
			if err := v4.NewSigner().SignHTTP(r.Context(), creds, r, hash, "s3", "us-east-1", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if r.Header.Get("X-Amz-Content-Sha256") != "" || strings.Contains(r.Header.Get("Authorization"), "x-amz-content-sha256") {
				t.Fatal("fixture must use an implicit hash")
			}
			reader := &kidFailingBody{}
			r.Body = io.NopCloser(reader)
			log.mu.Lock()
			before := len(log.events)
			log.mu.Unlock()
			w := rateServe(h, r)
			log.mu.Lock()
			defer log.mu.Unlock()
			if reader.reads == 0 || w.Code != 403 || !strings.Contains(w.Body.String(), "<Code>SignatureDoesNotMatch</Code>") ||
				calls.Load() != 0 || len(log.events) != before+1 {
				t.Fatalf("reads=%d status=%d upstream=%d events=%+v", reader.reads, w.Code, calls.Load(), log.events)
			}
			event := log.events[before]
			if event.Kid != "" || event.Status != 403 || event.Decision != "deny" {
				t.Fatalf("implicit-read denial audit=%+v", event)
			}
		})
	}
}

// protects=master hashed/implicit/unsigned success is unchanged and every non-201 body failure has empty audit Kid;
// fails_when=a tampered declared hash/body or oversized body forwards or acquires an authenticated audit identity;
// no_claim=live storage transport or unsigned body-size bounds.
func TestProxyKidMasterBodiesAndTampering(t *testing.T) {
	for _, tc := range []struct {
		name, payload, body, replacement string
		status                           int
		cap                              int64
	}{
		{"hashed", "hash", "hello", "", 201, 5},
		{"implicit-hash", "", "hello", "", 201, 5},
		{"unsigned", "UNSIGNED-PAYLOAD", "hello!", "", 201, 1},
		{"tampered-declared-hash", strings.Repeat("0", 64), "hello", "", 403, 5},
		{"tampered-body", "hash", "hello", "jello", 403, 5},
		{"oversized", "hash", "hello!", "", 413, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := tc.payload
			if payload == "hash" {
				sum := sha256.Sum256([]byte(tc.body))
				payload = hex.EncodeToString(sum[:])
			}
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				checkMaster(t, r)
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != tc.body {
					t.Errorf("upstream body=%q err=%v", body, err)
				}
				w.WriteHeader(201)
				_, _ = w.Write(body)
			}))
			defer upstream.Close()
			cfg := config(upstream.URL)
			cfg.MaxHashedBodyBytes = tc.cap
			log := &auditLog{}
			h, err := New(cfg, log)
			if err != nil {
				t.Fatal(err)
			}
			r := sign(t, "PUT", "/anything/key", tc.body, payload, true, false, false, nil)
			if tc.replacement != "" {
				r.Body = io.NopCloser(strings.NewReader(tc.replacement))
			}
			w := rateServe(h, r)
			if w.Code != tc.status || len(log.events) != 1 || log.events[0].Status != tc.status {
				t.Fatalf("status=%d events=%+v", w.Code, log.events)
			}
			event := log.events[0]
			if w.Code == 201 {
				if calls.Load() != 1 || event.Kid != masterKey || event.Decision != "allow" || w.Body.String() != tc.body {
					t.Fatalf("successful control: calls=%d event=%+v body=%s", calls.Load(), event, w.Body.String())
				}
			} else {
				if calls.Load() != 0 || event.Kid != "" || event.Decision != "deny" {
					t.Fatalf("master body denial: calls=%d event=%+v", calls.Load(), event)
				}
				code := "SignatureDoesNotMatch"
				if tc.status == 413 {
					code = "EntityTooLarge"
				}
				if !strings.Contains(w.Body.String(), "<Code>"+code+"</Code>") {
					t.Fatalf("unexpected error: %s", w.Body.String())
				}
			}
		})
	}
}
