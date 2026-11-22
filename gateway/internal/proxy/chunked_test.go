package proxy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
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

// Build the wire chain independently of the verifier and its SDK stream signer.
func signedChunkRequest(t *testing.T) *http.Request {
	t.Helper()
	const marker = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	const region = "us-west-2"
	now := time.Now().UTC()
	day := now.Format("20060102")
	v, _ := verify.New(signingKey)
	creds := aws.Credentials{AccessKeyID: agentKey, SecretAccessKey: v.Secret(agentKey), SessionToken: token(t, "rw", false)}
	r := httptest.NewRequest("PUT", "http://gateway.test/agent-data/run-42/object", nil)
	r.Header.Set("Content-Encoding", "aws-chunked, gzip")
	r.Header.Set("X-Amz-Content-Sha256", marker)
	r.Header.Set("X-Amz-Decoded-Content-Length", "11")
	chunks := []string{"hello ", "world", ""}
	for _, chunk := range chunks {
		r.ContentLength += int64(len(fmt.Sprintf("%x;chunk-signature=%064x\r\n%s\r\n", len(chunk), 0, chunk)))
	}
	signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
	if err := signer.SignHTTP(context.Background(), creds, r, marker, "s3", region, now); err != nil {
		t.Fatal(err)
	}
	_, previous, _ := strings.Cut(r.Header.Get("Authorization"), "Signature=")
	mac := func(key []byte, data string) []byte {
		h := hmac.New(sha256.New, key)
		_, _ = io.WriteString(h, data)
		return h.Sum(nil)
	}
	key := mac(mac(mac(mac([]byte("AWS4"+creds.SecretAccessKey), day), region), "s3"), "aws4_request")
	var body strings.Builder
	for _, chunk := range chunks {
		message := fmt.Sprintf("AWS4-HMAC-SHA256-PAYLOAD\n%s\n%s/%s/s3/aws4_request\n%s\n%x\n%x",
			now.Format("20060102T150405Z"), day, region, previous, sha256.Sum256(nil), sha256.Sum256([]byte(chunk)))
		previous = fmt.Sprintf("%x", mac(key, message))
		fmt.Fprintf(&body, "%x;chunk-signature=%s\r\n%s\r\n", len(chunk), previous, chunk)
	}
	r.Body = io.NopCloser(strings.NewReader(body.String()))
	return r
}

// protects=full chain verification and decoded master-signed forwarding within the wire cap;
// fails_when=a bad chain reaches storage, or valid bytes, length or signature change;
// no_claim=SDK or live SeaweedFS compatibility.
func TestSignedChunkProxy(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		checkMaster(t, r)
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "hello world" || r.ContentLength != 11 ||
			r.Header.Get("Content-Encoding") != "gzip" || r.Header.Get("X-Amz-Decoded-Content-Length") != "" ||
			r.Header.Get("X-Amz-Content-Sha256") != fmt.Sprintf("%x", sha256.Sum256(body)) {
			t.Errorf("bad decoded upload: body=%q length=%d headers=%v err=%v", body, r.ContentLength, r.Header, err)
		}
	}))
	defer upstream.Close()
	for _, tc := range []struct {
		name   string
		change func(string) string
		cap    int64
		status int
	}{
		{"valid", nil, 0, 200},
		{"tampered second chunk", func(s string) string { return strings.Replace(s, "world", "worlD", 1) }, 0, 403},
		{"missing final CRLF", func(s string) string { return s[:len(s)-2] }, 0, 403},
		{"trailing data", func(s string) string { return s + "extra" }, 0, 403},
		{"over body cap", nil, 100, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls.Store(0)
			cfg := config(upstream.URL)
			cfg.MaxHashedBodyBytes = tc.cap
			handler, err := New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			r := signedChunkRequest(t)
			if tc.change != nil {
				body, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(strings.NewReader(tc.change(string(body))))
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status || (calls.Load() == 1) != (tc.status == 200) {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
			}
		})
	}
}
