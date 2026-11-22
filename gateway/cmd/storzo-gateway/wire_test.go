package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/imthi16/storzo/gateway/internal/audit"
	"github.com/imthi16/storzo/gateway/internal/proxy"
)

// protects=startup requires AUDIT_URL and rejects malformed destinations without exposing values;
// fails_when=an unaudited gateway starts or an invalid URL is accepted;
// no_claim=the configured receiver is reachable or stores events.
func TestStartupAuditURL(t *testing.T) {
	t.Setenv("STORAGE_ENDPOINT", "http://storage:8333")
	t.Setenv("STORAGE_ACCESS_KEY", "test-master")
	t.Setenv("STORAGE_SECRET_KEY", "test-secret")
	t.Setenv("CRED_SIGNING_KEY", "public-signing-key-32-characters-long")
	t.Setenv("GATEWAY_LISTEN", "127.0.0.1:0")
	t.Setenv("GATEWAY_MAX_HASHED_BODY_MB", "")
	for _, value := range []string{"", "secret-invalid-url", "ftp://backend/audit", "http://user:secret@backend/audit"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("AUDIT_URL", value)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := run(ctx)
			if err == nil || !strings.Contains(err.Error(), "AUDIT_URL") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("startup error=%v", err)
			}
		})
	}
}

type closingListener struct {
	net.Listener
	closed chan struct{}
	once   sync.Once
}

func (l *closingListener) Close() error {
	err := l.Listener.Close()
	l.once.Do(func() { close(l.closed) })
	return err
}

// protects=allowed and denied requests are signed with CRED_SIGNING_KEY and flushed after HTTP draining;
// fails_when=shutdown drops an in-flight event, emits duplicates, signs incorrectly, or closes before the response;
// no_claim=OS signal delivery, the real backend database, or deployment networking.
func TestShutdownDrainsBeforeAuditClose(t *testing.T) {
	const key = "public-signing-key-32-characters-long"
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		_, _ = io.WriteString(w, "finished")
	}))
	defer upstream.Close()
	defer unblock()

	var mu sync.Mutex
	var events []audit.Event
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("audit body: %v", err)
			w.WriteHeader(400)
			return
		}
		mac := hmac.New(sha256.New, []byte(key))
		_, _ = mac.Write([]byte(r.Header.Get("X-Storzo-Timestamp") + "."))
		_, _ = mac.Write(body)
		if got := r.Header.Get("X-Storzo-Signature"); got != hex.EncodeToString(mac.Sum(nil)) {
			t.Error("audit signature does not use timestamp.body and CRED_SIGNING_KEY")
		}
		if r.Method == http.MethodGet {
			if r.URL.RequestURI() != "/revoked?since=0" {
				t.Error("unexpected startup revocation URL")
			}
			_, _ = io.WriteString(w, `{"kids":[],"now":1}`)
			return
		}
		var batch struct {
			Events []audit.Event
		}
		if err := json.Unmarshal(body, &batch); err != nil {
			t.Errorf("audit JSON: %v", err)
		}
		mu.Lock()
		events = append(events, batch.Events...)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := &closingListener{Listener: raw, closed: make(chan struct{})}
	defer listener.Close()
	cfg := proxy.Config{
		StorageEndpoint: upstream.URL, StorageAccessKey: "test-master", StorageSecretKey: "test-secret",
		CredSigningKey: key, AuditURL: receiver.URL,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, listener) }()

	client := &http.Client{Timeout: 3 * time.Second}
	endpoint := "http://" + listener.Addr().String()
	resp, err := client.Get(endpoint + "/bucket/denied")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("denial status=%d", resp.StatusCode)
	}

	req, err := http.NewRequest("GET", endpoint+"/bucket/in-flight", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
	creds := aws.Credentials{AccessKeyID: cfg.StorageAccessKey, SecretAccessKey: cfg.StorageSecretKey}
	if err := v4.NewSigner().SignHTTP(ctx, creds, req, "UNSIGNED-PAYLOAD", "s3", "us-east-1", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	type result struct {
		status int
		body   string
		err    error
	}
	responses := make(chan result, 1)
	go func() {
		resp, err := client.Do(req)
		if err != nil {
			responses <- result{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		responses <- result{status: resp.StatusCode, body: string(body), err: err}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight request did not reach storage")
	}
	cancel()
	select {
	case <-listener.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP shutdown did not close the listener")
	}
	select {
	case err := <-done:
		t.Fatalf("gateway stopped before request drained: %v", err)
	default:
	}
	unblock()
	select {
	case got := <-responses:
		if got.err != nil || got.status != 200 || got.body != "finished" {
			t.Fatalf("response=%+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight request did not finish")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("gateway did not flush and stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 {
		t.Fatalf("audit events=%+v", events)
	}
	byKey := make(map[string]audit.Event)
	for _, ev := range events {
		byKey[ev.Key] = ev
	}
	if ev := byKey["denied"]; ev.Status != 403 || ev.Decision != "deny" {
		t.Errorf("denial event=%+v", ev)
	}
	if ev := byKey["in-flight"]; ev.Status != 200 || ev.Decision != "allow" || ev.Bytes != 8 || ev.Kid != cfg.StorageAccessKey {
		t.Errorf("in-flight event=%+v", ev)
	}
}
