package proxy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/imthi16/storzo/gateway/internal/verify"
)

const signingKey = "public-test-signing-key-32-characters"
const masterKey = "master-test-key"
const masterSecret = "public-master-test-secret"
const agentKey = "STZABCDEFGHIJKLMNOP"

type auditLog struct {
	mu     sync.Mutex
	events []Event
}

func (a *auditLog) Emit(e Event) { a.mu.Lock(); defer a.mu.Unlock(); a.events = append(a.events, e) }
func token(t *testing.T, mode string, expired bool) string {
	t.Helper()
	now := time.Now().Unix()
	c := verify.Claims{Kid: agentKey, Bucket: "agent-data", Prefix: "run-42/", Mode: mode, Iat: now - 10, Exp: now + 900, Jti: "test-id"}
	if expired {
		c.Iat = now - 1000
		c.Exp = now - 61
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(signingKey))
	mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func sign(t *testing.T, method, path, body, payload string, master, presign, expired bool, extra http.Header) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, "http://gateway.test"+path, strings.NewReader(body))
	for k, v := range extra {
		r.Header[k] = v
	}
	if payload != "" {
		r.Header.Set("X-Amz-Content-Sha256", payload)
	} else {
		sum := sha256.Sum256([]byte(body))
		payload = hex.EncodeToString(sum[:])
	}
	v, _ := verify.New(signingKey)
	creds := aws.Credentials{AccessKeyID: agentKey, SecretAccessKey: v.Secret(agentKey), SessionToken: token(t, "ro", expired)}
	if master {
		creds = aws.Credentials{AccessKeyID: masterKey, SecretAccessKey: masterSecret}
	}
	signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true; o.DisableHeaderHoisting = true })
	if presign {
		q := r.URL.Query()
		q.Set("X-Amz-Expires", "300")
		r.URL.RawQuery = q.Encode()
		raw, _, err := signer.PresignHTTP(r.Context(), creds, r, payload, "s3", "us-east-1", time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		r.URL, _ = url.Parse(raw)
	} else if err := signer.SignHTTP(r.Context(), creds, r, payload, "s3", "us-east-1", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	return r
}

// checkMaster independently reconstructs the declared signed headers and asks the
// SDK to sign them, rather than trusting the key ID in Authorization.
func checkMaster(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get("X-Amz-Security-Token") != "" {
		t.Error("session token reached upstream")
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential="+masterKey+"/") {
		t.Errorf("wrong credentials: %q", auth)
	}
	_, tail, _ := strings.Cut(auth, "SignedHeaders=")
	declared, _, _ := strings.Cut(tail, ",")
	clone := r.Clone(context.Background())
	clone.Header = make(http.Header)
	clone.ContentLength = 0
	for _, name := range strings.Split(declared, ";") {
		if name == "content-length" {
			clone.ContentLength = r.ContentLength
		} else if name != "host" {
			clone.Header[http.CanonicalHeaderKey(name)] = r.Header.Values(name)
		}
	}
	at, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		t.Error(err)
		return
	}
	signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
	err = signer.SignHTTP(context.Background(), aws.Credentials{AccessKeyID: masterKey, SecretAccessKey: masterSecret}, clone, r.Header.Get("X-Amz-Content-Sha256"), "s3", "us-east-1", at)
	if err != nil || clone.Header.Get("Authorization") != auth {
		t.Errorf("invalid upstream signature: %v", err)
	}
	for name := range r.URL.Query() {
		if strings.HasPrefix(strings.ToLower(name), "x-amz-") {
			t.Errorf("presign auth forwarded: %s", name)
		}
	}
}
func config(endpoint string) Config {
	return Config{StorageEndpoint: endpoint, StorageAccessKey: masterKey, StorageSecretKey: masterSecret, CredSigningKey: signingKey}
}

// protects=scope and expiry errors never reach storage, accepted requests are signed
// with the master secret, paths remain escaped, and every request emits one audit;
// fails_when=status/XML/signature/path/token/audit count differs or a denial forwards;
// no_claim=live SeaweedFS, AWS CLI, throughput, or credential revocation.
func TestProxyRequests(t *testing.T) {
	cases := []struct {
		name, method, path, code string
		status                   int
		master, presign, expired bool
		headers                  http.Header
	}{
		{name: "ro-get", method: "GET", path: "/agent-data/run-42/a", status: 200},
		{name: "ro-put", method: "PUT", path: "/agent-data/run-42/a", status: 403, code: "AccessDenied"},
		{name: "other-prefix", method: "GET", path: "/agent-data/run-43/a", status: 403, code: "AccessDenied"},
		{name: "other-bucket", method: "GET", path: "/other-data/run-42/a", status: 403, code: "AccessDenied"},
		{name: "missing-list-prefix", method: "GET", path: "/agent-data?list-type=2", status: 403, code: "AccessDenied"},
		{name: "short-list-prefix", method: "GET", path: "/agent-data?list-type=2&prefix=run-4", status: 403, code: "AccessDenied"},
		{name: "valid-list", method: "GET", path: "/agent-data?list-type=2&prefix=run-42%2F", status: 200},
		{name: "dotdot", method: "GET", path: "/agent-data/run-42/../other/a", status: 400, code: "InvalidArgument"},
		{name: "encoded-dotdot", method: "GET", path: "/agent-data/run-42/%2e%2e/other/a", status: 400, code: "InvalidArgument"},
		{name: "copy", method: "PUT", path: "/agent-data/run-42/a", status: 501, code: "NotImplemented", headers: http.Header{"X-Amz-Copy-Source": []string{"/agent-data/run-42/b"}}},
		{name: "expired", method: "GET", path: "/agent-data/run-42/a", status: 403, code: "ExpiredToken", expired: true},
		{name: "client-token-stripped", method: "GET", path: "/agent-data/run-42/a", status: 200},
		{name: "space", method: "GET", path: "/agent-data/run-42/a%20b", status: 200},
		{name: "escaped-slash", method: "GET", path: "/agent-data/run-42/a%2Fb", status: 200},
		{name: "unicode", method: "GET", path: "/agent-data/run-42/%E6%97%A5", status: 200},
		{name: "presign", method: "GET", path: "/agent-data/run-42/a%2Fb", status: 200, presign: true},
		{name: "presign-list", method: "GET", path: "/agent-data?list-type=2&prefix=run-42%2F", status: 200, presign: true},
		{name: "master", method: "DELETE", path: "/outside-bucket/arbitrary", status: 200, master: true},
		{name: "master-presign", method: "GET", path: "/outside-bucket/arbitrary", status: 200, master: true, presign: true},
		{name: "master-copy", method: "PUT", path: "/outside-bucket/arbitrary", status: 501, code: "NotImplemented", master: true, headers: http.Header{"X-Amz-Copy-Source": []string{"/some/key"}}},
		{name: "aws-chunked", method: "PUT", path: "/agent-data/run-42/a", status: 501, code: "NotImplemented", headers: http.Header{"Content-Encoding": []string{"aws-chunked"}}},
		{name: "master-chunked", method: "PUT", path: "/outside-bucket/key", status: 501, code: "NotImplemented", master: true, headers: http.Header{"Content-Encoding": []string{"aws-chunked"}}},
		{name: "create-bucket", method: "PUT", path: "/agent-data", status: 403, code: "AccessDenied"},
		{name: "delete-bucket", method: "DELETE", path: "/agent-data", status: 403, code: "AccessDenied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			expected, _ := url.Parse(tc.path)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				checkMaster(t, r)
				if r.URL.EscapedPath() != expected.EscapedPath() {
					t.Errorf("path = %q, want %q", r.URL.EscapedPath(), expected.EscapedPath())
				}
				if r.URL.RawQuery != expected.RawQuery {
					t.Errorf("query=%q want=%q", r.URL.RawQuery, expected.RawQuery)
				}
				w.Write([]byte("hello"))
			}))
			defer upstream.Close()
			audit := &auditLog{}
			h, err := New(config(upstream.URL), audit)
			if err != nil {
				t.Fatal(err)
			}
			r := sign(t, tc.method, tc.path, "", "UNSIGNED-PAYLOAD", tc.master, tc.presign, tc.expired, tc.headers)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.code != "" && !strings.Contains(w.Body.String(), "<Code>"+tc.code+"</Code>") {
				t.Errorf("XML error=%s", w.Body.String())
			}
			wantCalls := 0
			decision := "deny"
			if tc.status == 200 {
				wantCalls = 1
				decision = "allow"
			}
			if calls.Load() != int32(wantCalls) {
				t.Errorf("upstream calls=%d", calls.Load())
			}
			if len(audit.events) != 1 {
				t.Fatalf("audit count=%d", len(audit.events))
			}
			e := audit.events[0]
			if e.Status != tc.status || e.Decision != decision || e.Method != tc.method || ((tc.expired || tc.headers.Get("Content-Encoding") == "aws-chunked") && e.Kid != "") || (!tc.expired && tc.headers.Get("Content-Encoding") != "aws-chunked" && e.Kid != map[bool]string{true: masterKey, false: agentKey}[tc.master]) || e.Bytes != int64(w.Body.Len()) {
				t.Errorf("audit=%+v", e)
			}
		})
	}
}

// protects=verified hashes/bodies forward intact and only hashed bodies have a cap;
// fails_when=oversized hashes do not return EntityTooLarge, a bad/oversized hash reaches storage, unsigned bodies are capped,
// connection headers leak, or storage response headers/body/status are lost;
// no_claim=bounded unsigned upload size or live multipart interoperability.
func TestBodiesAndHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, payload, body string
		status              int
		cap                 int64
	}{
		{"hashed", "hash", "hello", 201, 5}, {"implicit-hash", "", "hello", 201, 5},
		{"oversized", "hash", "hello!", 413, 5}, {"unsigned", "UNSIGNED-PAYLOAD", "hello!", 201, 1},
		{"tampered", strings.Repeat("0", 64), "hello", 403, 5},
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
					t.Errorf("body=%q err=%v", body, err)
				}
				if r.Header.Get("X-Hop") != "" || r.Header.Get("Connection") != "" {
					t.Error("request hop header leaked")
				}
				w.Header().Set("Connection", "X-Response-Hop")
				w.Header().Set("X-Response-Hop", "hidden")
				w.Header().Set("ETag", "test-etag")
				w.WriteHeader(201)
				w.Write(body)
			}))
			defer upstream.Close()
			cfg := config(upstream.URL)
			cfg.MaxHashedBodyBytes = tc.cap
			audit := &auditLog{}
			h, err := New(cfg, audit)
			if err != nil {
				t.Fatal(err)
			}
			r := sign(t, "PUT", "/anything/key", tc.body, payload, true, false, false, nil)
			r.Header.Set("Connection", "X-Hop")
			r.Header.Set("X-Hop", "hidden")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.status == http.StatusRequestEntityTooLarge && !strings.Contains(w.Body.String(), "<Code>EntityTooLarge</Code>") {
				t.Errorf("expected EntityTooLarge XML, body=%s", w.Body.String())
			}
			if tc.status == 201 {
				if calls.Load() != 1 || w.Body.String() != tc.body || w.Header().Get("ETag") != "test-etag" || w.Header().Get("X-Response-Hop") != "" {
					t.Errorf("response=%v %s calls=%d", w.Header(), w.Body.String(), calls.Load())
				}
			} else if calls.Load() != 0 {
				t.Error("denial reached storage")
			}
			if len(audit.events) != 1 || audit.events[0].Status != tc.status {
				t.Errorf("audit=%+v", audit.events)
			}
		})
	}
}

// protects=missing/invalid config fails closed, default listen/body limits are set;
// fails_when=missing names are omitted, secret values leak, or invalid limits pass;
// no_claim=deployment environment configuration or network listener reachability.
func TestConfigFromEnv(t *testing.T) {
	for _, name := range []string{"STORAGE_ENDPOINT", "STORAGE_ACCESS_KEY", "STORAGE_SECRET_KEY", "CRED_SIGNING_KEY", "AUDIT_URL", "GATEWAY_LISTEN", "GATEWAY_MAX_HASHED_BODY_MB", "GATEWAY_REVOCATION_MAX_STALE"} {
		t.Setenv(name, "")
	}
	_, err := ConfigFromEnv()
	if err == nil {
		t.Fatal("missing config accepted")
	}
	for _, name := range []string{"STORAGE_ENDPOINT", "STORAGE_ACCESS_KEY", "STORAGE_SECRET_KEY", "CRED_SIGNING_KEY", "AUDIT_URL"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("missing name %s: %v", name, err)
		}
	}
	t.Setenv("STORAGE_ENDPOINT", "http://storage:8333")
	t.Setenv("STORAGE_ACCESS_KEY", masterKey)
	t.Setenv("STORAGE_SECRET_KEY", masterSecret)
	t.Setenv("CRED_SIGNING_KEY", signingKey)
	t.Setenv("AUDIT_URL", "http://backend:8000/api/internal/audit")
	c, err := ConfigFromEnv()
	if err != nil || c.Listen != ":8333" || c.MaxHashedBodyBytes != 64<<20 || c.AuditURL != "http://backend:8000/api/internal/audit" || c.RevocationMaxStale != 60*time.Second {
		t.Fatalf("config=%+v err=%v", c, err)
	}
	for _, limit := range []string{"0", "-1", "bad", "9223372036854775807"} {
		t.Setenv("GATEWAY_MAX_HASHED_BODY_MB", limit)
		if _, err := ConfigFromEnv(); err == nil {
			t.Errorf("accepted limit %s", limit)
		}
	}
	t.Setenv("GATEWAY_MAX_HASHED_BODY_MB", "1")
	for _, raw := range []string{"0", "-1s", "bad", "5s", "13s"} {
		t.Setenv("GATEWAY_REVOCATION_MAX_STALE", raw)
		if _, err := ConfigFromEnv(); err == nil {
			t.Errorf("accepted max stale %s", raw)
		}
	}
	t.Setenv("GATEWAY_REVOCATION_MAX_STALE", "90s")
	if c, err := ConfigFromEnv(); err != nil || c.RevocationMaxStale != 90*time.Second {
		t.Errorf("max stale override: %+v err=%v", c, err)
	}
	for _, endpoint := range []string{"secret-invalid-url", "ftp://storage", "http://user:secret@storage", "http://storage/path", "http://storage?secret"} {
		t.Setenv("STORAGE_ENDPOINT", endpoint)
		_, err := ConfigFromEnv()
		if err == nil || strings.Contains(err.Error(), "secret-invalid") || strings.Contains(err.Error(), "user:secret") {
			t.Errorf("unsafe endpoint validation: %v", err)
		}
	}
}

// protects=master authentication still rejects wrong secrets, session tokens and stale signatures;
// fails_when=invalid master authentication reaches storage or misses its audit;
// no_claim=key rotation without a process restart.
func TestMasterAuthenticationFailsClosed(t *testing.T) {
	for _, kind := range []string{"signature", "session-token", "stale", "unsigned"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
			defer upstream.Close()
			audit := &auditLog{}
			h, _ := New(config(upstream.URL), audit)
			r := sign(t, "GET", "/outside/key", "", "UNSIGNED-PAYLOAD", true, false, false, nil)
			switch kind {
			case "signature":
				r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), "Signature=", "Signature=0", 1))
			case "session-token":
				r.Header.Set("X-Amz-Security-Token", "arbitrary-token")
			case "stale":
				r.Header.Del("Authorization")
				r.Header.Del("X-Amz-Date")
				signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
				if err := signer.SignHTTP(r.Context(), aws.Credentials{AccessKeyID: masterKey, SecretAccessKey: masterSecret}, r, "UNSIGNED-PAYLOAD", "s3", "us-east-1", time.Now().Add(-time.Hour)); err != nil {
					t.Fatal(err)
				}
			case "unsigned":
				r.Header.Del("Authorization")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 403 || calls.Load() != 0 || len(audit.events) != 1 || audit.events[0].Decision != "deny" {
				t.Errorf("code=%d calls=%d audit=%+v", w.Code, calls.Load(), audit.events)
			}
		})
	}
}

// protects=unsigned uploads reach storage before EOF, response chunks reach the
// client before storage finishes, and scoped rw writes work over real HTTP;
// fails_when=either direction buffers the complete body or re-signing fails;
// no_claim=throughput, aws-chunked support, or live SeaweedFS multipart behavior.
func TestStreaming(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstUpload := make(chan struct{})
	finishResponse := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkMaster(t, r)
		first := make([]byte, 4)
		if _, err := io.ReadFull(r.Body, first); err != nil || string(first) != "part" {
			t.Errorf("upload prefix=%q err=%v", first, err)
			return
		}
		close(firstUpload)
		rest, err := io.ReadAll(r.Body)
		if err != nil || string(rest) != "-end" {
			t.Errorf("upload suffix=%q err=%v", rest, err)
			return
		}
		w.Write([]byte("early"))
		w.(http.Flusher).Flush()
		select {
		case <-finishResponse:
			w.Write([]byte("-end"))
		case <-ctx.Done():
		}
	}))
	defer upstream.Close()
	cfg := config(upstream.URL)
	cfg.MaxHashedBodyBytes = 1
	h, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(h)
	defer gateway.Close()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	r, err := http.NewRequestWithContext(ctx, "PUT", gateway.URL+"/agent-data/run-42/stream", reader)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
	v, _ := verify.New(signingKey)
	creds := aws.Credentials{AccessKeyID: agentKey, SecretAccessKey: v.Secret(agentKey), SessionToken: token(t, "rw", false)}
	signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
	if err := signer.SignHTTP(ctx, creds, r, "UNSIGNED-PAYLOAD", "s3", "us-east-1", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	type result struct {
		response *http.Response
		err      error
	}
	results := make(chan result, 1)
	go func() { response, err := gateway.Client().Do(r); results <- result{response, err} }()
	if _, err := writer.Write([]byte("part")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstUpload:
	case <-ctx.Done():
		t.Fatal("upload buffered before EOF")
	}
	if _, err := writer.Write([]byte("-end")); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	var response *http.Response
	select {
	case got := <-results:
		if got.err != nil {
			t.Fatal(got.err)
		}
		response = got.response
	case <-ctx.Done():
		t.Fatal("response buffered")
	}
	defer response.Body.Close()
	prefix := make([]byte, 5)
	if _, err := io.ReadFull(response.Body, prefix); err != nil || string(prefix) != "early" {
		t.Fatalf("response prefix=%q err=%v", prefix, err)
	}
	close(finishResponse)
	suffix, err := io.ReadAll(response.Body)
	if err != nil || string(suffix) != "-end" || response.StatusCode != 200 {
		t.Fatalf("response suffix=%q code=%d err=%v", suffix, response.StatusCode, err)
	}
}
