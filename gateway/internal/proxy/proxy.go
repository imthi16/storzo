// Package proxy authenticates S3 requests and forwards them with master credentials.
package proxy

import (
	"errors"
	"fmt"
	"io"
	"log"
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

type Config struct {
	Listen, StorageEndpoint, StorageAccessKey, StorageSecretKey, CredSigningKey, AuditURL string
	MaxHashedBodyBytes                                                                    int64
}

// ConfigFromEnv validates configuration without including secret values in errors.
func ConfigFromEnv() (Config, error) {
	c := Config{AuditURL: os.Getenv("AUDIT_URL"), Listen: os.Getenv("GATEWAY_LISTEN"), StorageEndpoint: os.Getenv("STORAGE_ENDPOINT"), StorageAccessKey: os.Getenv("STORAGE_ACCESS_KEY"), StorageSecretKey: os.Getenv("STORAGE_SECRET_KEY"), CredSigningKey: os.Getenv("CRED_SIGNING_KEY")}
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
	if c.MaxHashedBodyBytes < 0 {
		return errors.New("invalid GATEWAY_MAX_HASHED_BODY_MB")
	}
	return nil
}

type Handler struct {
	verifier *verify.Verifier
	upstream *httputil.ReverseProxy
	auditor  Auditor
	maxBody  int64
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
	return &Handler{v, rp, auditor, c.MaxHashedBodyBytes}, nil
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
	auth := r.Header.Get("Authorization")
	_, auth, _ = strings.Cut(auth, "Credential=")
	kid, _, _ := strings.Cut(auth, "/")
	if kid == "" {
		kid, _, _ = strings.Cut(r.URL.Query().Get("X-Amz-Credential"), "/")
	}
	event := Event{Kid: kid, Method: r.Method, Bucket: bucket, Key: key, Decision: "deny"}
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
		var s3err *verify.Error
		if !errors.As(err, &s3err) {
			s3err = &verify.Error{Status: 403, Code: "AccessDenied", Message: "Request denied."}
		}
		s3err.Write(rw)
		return
	}
	event.Kid, event.Decision = claims.Kid, "allow"
	h.upstream.ServeHTTP(rw, r)
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
