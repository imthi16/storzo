// Package proxy authenticates S3 requests and forwards them with master credentials.
package proxy

import (
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/imthi16/storzo/gateway/internal/audit"
	"github.com/imthi16/storzo/gateway/internal/verify"
)

// Event is the shared audit ingest event.
type Event = audit.Event
type Auditor interface{ Emit(Event) }
type noop struct{}

func (noop) Emit(Event) {}

const DefaultRevocationPollInterval = 10 * time.Second

type Config struct {
	Revocations                                                                           *audit.Revocations
	RevocationPollInterval                                                                time.Duration
	RevocationMaxStale                                                                    time.Duration
	Listen, StorageEndpoint, StorageAccessKey, StorageSecretKey, CredSigningKey, AuditURL string
	MaxHashedBodyBytes                                                                    int64
	RateLimit                                                                             float64 // requests per second per verified scoped kid; zero disables
	RateBurst                                                                             int
	RateClock                                                                             func() time.Time // optional limiter clock; nil uses time.Now
}

// ConfigFromEnv validates configuration without including secret values in errors.
func ConfigFromEnv() (Config, error) {
	c := Config{RevocationPollInterval: DefaultRevocationPollInterval, RevocationMaxStale: audit.DefaultRevocationMaxStale, RateLimit: defaultRateLimit, RateBurst: defaultRateBurst, AuditURL: os.Getenv("AUDIT_URL"), Listen: os.Getenv("GATEWAY_LISTEN"), StorageEndpoint: os.Getenv("STORAGE_ENDPOINT"), StorageAccessKey: os.Getenv("STORAGE_ACCESS_KEY"), StorageSecretKey: os.Getenv("STORAGE_SECRET_KEY"), CredSigningKey: os.Getenv("CRED_SIGNING_KEY")}
	var missing []string
	for _, item := range []struct{ name, value string }{{"STORAGE_ENDPOINT", c.StorageEndpoint}, {"STORAGE_ACCESS_KEY", c.StorageAccessKey}, {"STORAGE_SECRET_KEY", c.StorageSecretKey}, {"CRED_SIGNING_KEY", c.CredSigningKey}, {"AUDIT_URL", c.AuditURL}} {
		if item.value == "" {
			missing = append(missing, item.name)
		}
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}
	u, err := url.Parse(c.AuditURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return c, errors.New("invalid AUDIT_URL: expected an HTTP URL")
	}
	if c.Listen == "" {
		c.Listen = ":8333"
	}
	mb := int64(64)
	if raw := os.Getenv("GATEWAY_MAX_HASHED_BODY_MB"); raw != "" {
		var err error
		mb, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || mb <= 0 || mb > (1<<63-1)/(1<<20) {
			return c, errors.New("invalid GATEWAY_MAX_HASHED_BODY_MB")
		}
	}
	if raw := os.Getenv("GATEWAY_RATE_LIMIT"); raw != "" {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return c, errors.New("invalid GATEWAY_RATE_LIMIT: expected a finite nonnegative number")
		}
		c.RateLimit = value
	}
	if raw := os.Getenv("GATEWAY_RATE_BURST"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return c, errors.New("invalid GATEWAY_RATE_BURST: expected an integer")
		}
		c.RateBurst = value
	}
	if raw := os.Getenv("GATEWAY_REVOCATION_POLL_INTERVAL"); raw != "" {
		interval, err := time.ParseDuration(raw)
		if err != nil || interval <= 0 {
			return c, errors.New("invalid GATEWAY_REVOCATION_POLL_INTERVAL: expected a positive duration")
		}
		c.RevocationPollInterval = interval
	}
	if raw := os.Getenv("GATEWAY_REVOCATION_MAX_STALE"); raw != "" {
		bound, err := time.ParseDuration(raw)
		if err != nil || bound <= 0 {
			return c, errors.New("invalid GATEWAY_REVOCATION_MAX_STALE: expected a positive duration")
		}
		c.RevocationMaxStale = bound
	}
	// A bound inside one healthy poll cycle would 503 scoped traffic with the backend up.
	if c.RevocationMaxStale <= c.RevocationPollInterval+audit.RevocationPollTimeout {
		return c, errors.New("invalid GATEWAY_REVOCATION_MAX_STALE: must exceed the poll interval plus the 3s poll timeout")
	}
	c.MaxHashedBodyBytes = mb * (1 << 20)
	return c, validate(c)
}

func validate(c Config) error {
	u, err := url.Parse(c.StorageEndpoint)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid STORAGE_ENDPOINT: expected an HTTP origin")
	}
	if c.StorageAccessKey == "" {
		return errors.New("missing STORAGE_ACCESS_KEY")
	}
	if c.StorageSecretKey == "" {
		return errors.New("missing STORAGE_SECRET_KEY")
	}
	if len([]rune(c.CredSigningKey)) < 32 {
		return errors.New("CRED_SIGNING_KEY must contain at least 32 characters")
	}
	if c.RateLimit < 0 || math.IsNaN(c.RateLimit) || math.IsInf(c.RateLimit, 0) {
		return errors.New("invalid GATEWAY_RATE_LIMIT: expected a finite nonnegative number")
	}
	if c.RateBurst < 0 || (c.RateLimit > 0 && c.RateBurst < 1) {
		return errors.New("invalid GATEWAY_RATE_BURST: must be at least 1 when rate limiting is enabled")
	}
	if c.MaxHashedBodyBytes < 0 {
		return errors.New("invalid GATEWAY_MAX_HASHED_BODY_MB")
	}
	return nil
}

type Handler struct {
	verifier    *verify.Verifier
	upstream    *httputil.ReverseProxy
	auditor     Auditor
	maxBody     int64
	limiter     *rateLimiter
	revocations *audit.Revocations
}

func New(c Config, auditor Auditor) (*Handler, error) {
	if err := validate(c); err != nil {
		return nil, err
	}
	v, err := verify.NewWithMaster(c.CredSigningKey, c.StorageAccessKey, c.StorageSecretKey)
	if err != nil {
		return nil, err
	}
	if auditor == nil {
		auditor = noop{}
	}
	if c.MaxHashedBodyBytes == 0 {
		c.MaxHashedBodyBytes = 64 << 20
	}
	endpoint, _ := url.Parse(c.StorageEndpoint)
	// Disable transparent response decompression and environment proxy routing.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	transport.Proxy = nil
	signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
	rp := &httputil.ReverseProxy{
		Rewrite: func(p *httputil.ProxyRequest) {
			p.Out.URL.Scheme, p.Out.URL.Host = endpoint.Scheme, endpoint.Host
			p.Out.Host = endpoint.Host
			p.Out.Header.Del("Authorization")
			p.Out.Header.Del("X-Amz-Security-Token")
			p.Out.Header.Del("X-Amz-Date")
			p.Out.Header.Del("X-Amz-Content-Sha256")
			// Verify the client's signature first, then remove controls that would
			// otherwise exercise master-key ACL privileges upstream.
			for name := range p.Out.Header {
				lower := strings.ToLower(name)
				if lower == "x-amz-acl" || strings.HasPrefix(lower, "x-amz-grant-") || lower == "x-amz-tagging-directive" {
					delete(p.Out.Header, name)
				}
			}
			// Preserve the exact bytes/order/escaping of non-auth query components.
			var parts []string
			for _, part := range strings.Split(p.In.URL.RawQuery, "&") {
				name, _, _ := strings.Cut(part, "=")
				decoded, _ := url.QueryUnescape(name)
				if !strings.HasPrefix(strings.ToLower(decoded), "x-amz-") {
					parts = append(parts, part)
				}
			}
			p.Out.URL.RawQuery = strings.Join(parts, "&")
			p.Out.Header.Set("X-Amz-Content-Sha256", p.In.Header.Get("X-Amz-Content-Sha256"))
		},
		Transport: signingTransport{transport, signer, aws.Credentials{AccessKeyID: c.StorageAccessKey, SecretAccessKey: c.StorageSecretKey}},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			(&verify.Error{Status: 502, Code: "BadGateway", Message: "Storage request failed."}).Write(w)
		},
		ErrorLog:      log.New(io.Discard, "", 0),
		FlushInterval: -1,
	}
	return &Handler{revocations: c.Revocations, verifier: v, upstream: rp, auditor: auditor, maxBody: c.MaxHashedBodyBytes, limiter: newRateLimiter(c.RateLimit, c.RateBurst, c.RateClock)}, nil
}

type signingTransport struct {
	base        http.RoundTripper
	signer      *v4.Signer
	credentials aws.Credentials
}

func (s signingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	raw := r.URL.RawQuery
	err := s.signer.SignHTTP(r.Context(), s.credentials, r, r.Header.Get("X-Amz-Content-Sha256"), "s3", "us-east-1", time.Now().UTC())
	r.URL.RawQuery = raw
	if err != nil {
		return nil, errors.New("upstream signing failed")
	}
	return s.base.RoundTrip(r)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rw := &responseWriter{ResponseWriter: w}
	var requestBytes atomic.Int64
	if r.Body != nil {
		// Count the original input, not the verifier's in-memory replay.
		r.Body = &countingBody{ReadCloser: r.Body, bytes: &requestBytes}
	}
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	event := Event{Method: r.Method, Bucket: bucket, Key: key, Decision: "deny"}
	defer func() {
		event.Status, event.Bytes = rw.status, requestBytes.Load()+rw.bytes
		if event.Status == 0 {
			event.Status = 200
		}
		h.auditor.Emit(event)
	}()
	payload := r.Header.Get("X-Amz-Content-Sha256")
	q := r.URL.Query()
	if q.Has("X-Amz-Content-Sha256") {
		payload = q.Get("X-Amz-Content-Sha256")
	}
	unsigned := payload == "UNSIGNED-PAYLOAD" || (payload == "" && q.Has("X-Amz-Algorithm"))
	if !unsigned && r.Body != nil {
		r.Body = http.MaxBytesReader(rw, r.Body, h.maxBody)
	}
	claims, err := h.verifier.Verify(r, time.Now().UTC())
	if err != nil {
		// Nonempty error claims authenticate identity only; they never allow forwarding.
		if claims.Kid != "" {
			event.Kid = claims.Kid
		}
		var s3err *verify.Error
		if !errors.As(err, &s3err) {
			s3err = &verify.Error{Status: 403, Code: "AccessDenied", Message: "Request denied."}
		}
		s3err.Write(rw)
		return
	}
	event.Kid = claims.Kid
	if !claims.Master && h.revocations != nil {
		if h.revocations.Contains(claims.Kid) {
			(&verify.Error{Status: 403, Code: "AccessDenied", Message: "Credential revoked."}).Write(rw)
			return
		}
		// Fail closed once the revocation snapshot is stale; master keys bypass it.
		if h.revocations.Stale(time.Now()) {
			(&verify.Error{Status: 503, Code: "ServiceUnavailable", Message: "Revocation status unavailable."}).Write(rw)
			return
		}
	}
	// Only a successfully verified scoped identity may spend its bucket.
	if !claims.Master && !h.limiter.allow(claims.Kid) {
		rw.Header().Set("Retry-After", "1")
		(&verify.Error{Status: 503, Code: "SlowDown", Message: "Please reduce your request rate."}).Write(rw)
		return
	}
	event.Decision = "allow"
	if !claims.Master {
		for name := range r.Header {
			lower := strings.ToLower(name)
			if strings.HasPrefix(lower, "x-amz-") && !allowedScopedHeader(lower) {
				delete(r.Header, name)
			}
		}
	}
	h.upstream.ServeHTTP(rw, r)
}

// Only these S3 controls may be promoted from scoped requests to master signing.
func allowedScopedHeader(name string) bool {
	return strings.HasPrefix(name, "x-amz-meta-") || strings.HasPrefix(name, "x-amz-checksum-") ||
		name == "x-amz-sdk-checksum-algorithm" || name == "x-amz-content-sha256" || name == "x-amz-date"
}

type countingBody struct {
	io.ReadCloser
	bytes *atomic.Int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.bytes.Add(int64(n))
	return n, err
}

type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *responseWriter) WriteHeader(code int) {
	if code >= 100 && code < 200 {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.status == 0 {
		w.status = code
		w.ResponseWriter.WriteHeader(code)
	}
}
func (w *responseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}
func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
