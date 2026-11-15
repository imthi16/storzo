package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/imthi16/storzo/gateway/internal/audit"
	"github.com/imthi16/storzo/gateway/internal/verify"
)

// protects=audit bytes sum consumed request and written response bytes, counting hashed replay once;
// fails_when=uploads are omitted, replay is counted twice, unread bodies are counted, or an event is duplicated;
// no_claim=TCP framing bytes or bytes the HTTP server drains after the handler returns.
func TestAuditTransferredBytes(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		authenticated bool
		wantRead      int
		wantStatus    int
	}{
		{"hashed", "hash", true, 7, 200},
		{"unsigned", "UNSIGNED-PAYLOAD", true, 7, 200},
		{"bad-hash", strings.Repeat("0", 64), true, 7, 403},
		{"unread-denial", "UNSIGNED-PAYLOAD", false, 0, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != "request" {
					t.Errorf("request body=%q err=%v", body, err)
				}
				_, _ = io.WriteString(w, "reply")
			}))
			defer upstream.Close()
			log := &auditLog{}
			h, err := New(config(upstream.URL), log)
			if err != nil {
				t.Fatal(err)
			}
			payload := tc.payload
			if payload == "hash" {
				sum := sha256.Sum256([]byte("request"))
				payload = hex.EncodeToString(sum[:])
			}
			r := sign(t, "PUT", "/anything/key", "request", payload, true, false, false, nil)
			if !tc.authenticated {
				r.Header.Del("Authorization")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.wantStatus || len(log.events) != 1 {
				t.Fatalf("status=%d events=%+v", w.Code, log.events)
			}
			if got, want := log.events[0].Bytes, int64(tc.wantRead+w.Body.Len()); got != want {
				t.Fatalf("audit bytes=%d want=%d", got, want)
			}
		})
	}
}

// protects=scoped uploads cannot forward ACL/grant/tagging-directive controls under master credentials;
// fails_when=a forbidden header reaches storage, the upload fails, or ordinary metadata is stripped;
// no_claim=bucket policies or live SeaweedFS ACL semantics.
func TestStripACLHeaders(t *testing.T) {
	names := []string{
		"x-amz-acl", "x-amz-grant-read", "x-amz-grant-write",
		"x-amz-grant-read-acp", "x-amz-grant-write-acp",
		"x-amz-grant-full-control", "x-amz-tagging-directive",
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkMaster(t, r)
		for _, name := range names {
			if values := r.Header.Values(name); len(values) != 0 {
				t.Errorf("forwarded %s=%v", name, values)
			}
		}
		if r.Header.Get("X-Amz-Meta-Label") != "keep" {
			t.Error("ordinary metadata lost")
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer upstream.Close()
	log := &auditLog{}
	h, err := New(config(upstream.URL), log)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("PUT", "http://gateway.test/agent-data/run-42/key", strings.NewReader("upload"))
	for _, name := range names {
		r.Header.Set(name, "untrusted")
	}
	r.Header.Set("X-Amz-Meta-Label", "keep")
	r.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
	v, _ := verify.New(signingKey)
	creds := aws.Credentials{AccessKeyID: agentKey, SecretAccessKey: v.Secret(agentKey), SessionToken: token(t, "rw", false)}
	if err := v4.NewSigner().SignHTTP(r.Context(), creds, r, "UNSIGNED-PAYLOAD", "s3", "us-east-1", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated || len(log.events) != 1 || log.events[0].Decision != "allow" {
		t.Fatalf("status=%d body=%s events=%+v", w.Code, w.Body.String(), log.events)
	}
}

type auditRoundTripper func(*http.Request) (*http.Response, error)

func (f auditRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// protects=an unavailable audit receiver and a pending audit failure do not block storage requests;
// fails_when=an audit network error changes the storage response or holds a request until released;
// no_claim=delivery of events while the receiver is down or under queue overflow.
func TestAuditReceiverDownDoesNotBlockRequests(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	receiver.Close()
	failed := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	base := receiver.Client().Transport
	em := audit.New(audit.Config{
		URL: receiver.URL, SigningKey: signingKey, BatchSize: 1, RetryBackoff: time.Millisecond,
		Client: &http.Client{Transport: auditRoundTripper(func(r *http.Request) (*http.Response, error) {
			resp, err := base.RoundTrip(r)
			if err == nil {
				t.Error("closed receiver unexpectedly answered")
			}
			once.Do(func() { close(failed) })
			// Hold the failed audit write open to prove requests do not wait for it.
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return resp, err
		})},
	})
	defer func() {
		close(release)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := em.Close(ctx); err != nil {
			t.Errorf("close emitter: %v", err)
		}
	}()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "storage-ok")
	}))
	defer upstream.Close()
	h, err := New(config(upstream.URL), em)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		done := make(chan *httptest.ResponseRecorder, 1)
		r := sign(t, "GET", "/anything/key", "", "UNSIGNED-PAYLOAD", true, false, false, nil)
		go func() {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			done <- w
		}()
		select {
		case w := <-done:
			if w.Code != 200 || w.Body.String() != "storage-ok" {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		case <-time.After(time.Second):
			t.Fatal("request waited on audit failure")
		}
		if i == 0 {
			select {
			case <-failed:
			case <-time.After(time.Second):
				t.Fatal("audit receiver was not attempted")
			}
		}
	}
}
