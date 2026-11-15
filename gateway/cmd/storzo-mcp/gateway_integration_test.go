package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/imthi16/storzo/gateway/internal/proxy"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	gwSigningKey   = "public-test-signing-key-32-characters"
	gwMasterKey    = "master-test-key"
	gwMasterSecret = "public-master-test-secret"
	scopeBucket    = "agent-data"
	scopePrefix    = "run-42/"
	inScopeBody    = "scoped-body"
)

type minted struct {
	kid, secret, token string
}

// mintScoped builds a Python-compatible credential (backend/app/credentials.py):
// secret = hex(HMAC(key, "secret|"+kid)), token = b64url(claims) + "." + b64url(HMAC(key, that segment)).
func mintScoped(t *testing.T, mode string) minted {
	t.Helper()
	kid := "STZAAAAAAAAAAAAAAAA"
	now := time.Now().Unix()
	raw, err := json.Marshal(map[string]any{
		"kid": kid, "bucket": scopeBucket, "prefix": scopePrefix, "mode": mode,
		"iat": now - 10, "exp": now + 900, "jti": "mcp-gateway-jti",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(gwSigningKey))
	mac.Write([]byte(body))
	token := body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	sec := hmac.New(sha256.New, []byte(gwSigningKey))
	sec.Write([]byte("secret|" + kid))
	return minted{kid: kid, secret: hex.EncodeToString(sec.Sum(nil)), token: token}
}

type hit struct {
	method, path, prefix, auth, sessionToken string
}

type upstreamLog struct {
	mu   sync.Mutex
	hits []hit
}

func (u *upstreamLog) snapshot() []hit {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]hit, len(u.hits))
	copy(out, u.hits)
	return out
}

func (u *upstreamLog) serve(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	u.hits = append(u.hits, hit{
		method: r.Method, path: r.URL.EscapedPath(), prefix: r.URL.Query().Get("prefix"),
		auth: r.Header.Get("Authorization"), sessionToken: r.Header.Get("X-Amz-Security-Token"),
	})
	u.mu.Unlock()
	if r.URL.Query().Get("list-type") == "2" {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`+
			`<Name>%s</Name><Prefix>%s</Prefix><KeyCount>1</KeyCount><MaxKeys>100</MaxKeys>`+
			`<IsTruncated>false</IsTruncated><Contents><Key>%sa.txt</Key><Size>%d</Size>`+
			`<LastModified>2026-01-01T00:00:00.000Z</LastModified></Contents></ListBucketResult>`,
			scopeBucket, scopePrefix, scopePrefix, len(inScopeBody))
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, inScopeBody)
	case http.MethodPut:
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

type auditLog struct {
	mu     sync.Mutex
	events []proxy.Event
}

func (a *auditLog) Emit(e proxy.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

func (a *auditLog) snapshot() []proxy.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]proxy.Event, len(a.events))
	copy(out, a.events)
	return out
}

// saysAccessDenied reports whether an SDK or tool error names the S3 AccessDenied code.
func saysAccessDenied(s string) bool {
	folded := strings.ToLower(strings.ReplaceAll(s, " ", ""))
	return strings.Contains(folded, "accessdenied")
}

type gatewayRig struct {
	upstream *upstreamLog
	audit    *auditLog
	hits     atomic.Int32
	url      string
}

func newGateway(t *testing.T) *gatewayRig {
	t.Helper()
	up := &upstreamLog{}
	store := httptest.NewServer(http.HandlerFunc(up.serve))
	t.Cleanup(store.Close)
	aud := &auditLog{}
	handler, err := proxy.New(proxy.Config{
		StorageEndpoint:    store.URL,
		StorageAccessKey:   gwMasterKey,
		StorageSecretKey:   gwMasterSecret,
		CredSigningKey:     gwSigningKey,
		AuditURL:           "http://127.0.0.1:9/audit",
		MaxHashedBodyBytes: 1 << 20,
	}, aud)
	if err != nil {
		t.Fatal(err)
	}
	rig := &gatewayRig{upstream: up, audit: aud}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rig.hits.Add(1)
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(gw.Close)
	rig.url = gw.URL
	return rig
}

func openSession(t *testing.T, endpoint, mode string, cred minted) (*mcp.ClientSession, context.Context, *s3.Client) {
	t.Helper()
	cfg := Config{
		Endpoint: endpoint, AccessKeyID: cred.kid, SecretAccessKey: cred.secret,
		SessionToken: cred.token, Bucket: scopeBucket, Prefix: scopePrefix, Mode: mode,
	}
	client := newS3Client(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	session := connectPair(t, ctx, buildServer(&app{cfg: cfg, s3: client}))
	t.Cleanup(func() { session.Close() })
	return session, ctx, client
}

func toolText(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	text, isErr, errText := callToolText(t, ctx, session, name, args)
	if errText != "" {
		text += errText
		isErr = true
	}
	return text, isErr
}

func assertAudit(t *testing.T, rig *gatewayRig, denies int) {
	t.Helper()
	events := rig.audit.snapshot()
	if len(events) != int(rig.hits.Load()) {
		t.Fatalf("audit events=%d gateway requests=%d", len(events), rig.hits.Load())
	}
	got := 0
	for _, ev := range events {
		if ev.Decision != "deny" {
			continue
		}
		got++
		if ev.Kid != "STZAAAAAAAAAAAAAAAA" {
			t.Errorf("denial kid=%q, want verified fixture kid %q", ev.Kid, "STZAAAAAAAAAAAAAAAA")
		}
	}
	if got != denies {
		t.Errorf("denials=%d, want %d", got, denies)
	}
}

// protects=ro tools/list hides writes, in-scope get returns the body, an outside get is access-denied with zero upstream calls, list defaults to the scope prefix, and the same credential's raw PutObject is 403;
// fails_when=put_object is registered for ro, an outside get or the raw put reaches storage, or list omits the scope prefix;
// no_claim=live SeaweedFS, presign, or that the MCP process itself checks scope.
func TestGatewayROCredential(t *testing.T) {
	rig := newGateway(t)
	cred := mintScoped(t, "ro")
	session, ctx, client := openSession(t, rig.url, "ro", cred)

	res, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := toolNames(res.Tools)
	for _, want := range []string{"list_objects", "get_object"} {
		if !names[want] {
			t.Errorf("missing %s", want)
		}
	}
	for _, banned := range []string{"put_object", "delete_object"} {
		if names[banned] {
			t.Errorf("ro tools/list contains %s", banned)
		}
	}

	before := len(rig.upstream.snapshot())
	text, isErr := toolText(t, ctx, session, "get_object", map[string]any{"key": scopePrefix + "a.txt"})
	if isErr || text != inScopeBody {
		t.Fatalf("in-scope get: err=%v text=%q", isErr, text)
	}
	if len(rig.upstream.snapshot()) != before+1 {
		t.Fatalf("in-scope get did not reach upstream once")
	}

	before = len(rig.upstream.snapshot())
	text, isErr = toolText(t, ctx, session, "get_object", map[string]any{"key": "outside/a.txt"})
	if !isErr || !saysAccessDenied(text) {
		t.Fatalf("outside get: err=%v text=%q", isErr, text)
	}
	if len(rig.upstream.snapshot()) != before {
		t.Fatalf("outside get reached upstream %d extra times", len(rig.upstream.snapshot())-before)
	}

	text, isErr = toolText(t, ctx, session, "list_objects", map[string]any{})
	if isErr || !strings.Contains(text, scopePrefix+"a.txt") {
		t.Fatalf("list: err=%v text=%q", isErr, text)
	}
	hits := rig.upstream.snapshot()
	if hits[len(hits)-1].prefix != scopePrefix {
		t.Fatalf("list prefix=%q, want %s", hits[len(hits)-1].prefix, scopePrefix)
	}

	before = len(hits)
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(scopeBucket),
		Key:    aws.String(scopePrefix + "blocked.txt"),
		Body:   strings.NewReader("nope"),
	})
	if err == nil || !strings.Contains(err.Error(), "StatusCode: 403") || !saysAccessDenied(err.Error()) {
		t.Fatalf("raw PutObject: %v", err)
	}
	if len(rig.upstream.snapshot()) != before {
		t.Fatal("raw ro PutObject reached upstream")
	}
	assertAudit(t, rig, 2)
}

// protects=rw registers all four tools, an in-scope put is re-signed with the master key and no session token, an outside put is denied with zero upstream calls, and an in-scope delete reaches storage;
// fails_when=the upstream sees the agent credential or session token, or an outside put is forwarded;
// no_claim=live SeaweedFS or multipart uploads.
func TestGatewayRWCredential(t *testing.T) {
	rig := newGateway(t)
	cred := mintScoped(t, "rw")
	session, ctx, _ := openSession(t, rig.url, "rw", cred)

	res, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := toolNames(res.Tools)
	for _, want := range []string{"list_objects", "get_object", "put_object", "delete_object"} {
		if !names[want] {
			t.Errorf("missing %s", want)
		}
	}

	text, isErr := toolText(t, ctx, session, "put_object", map[string]any{
		"key": scopePrefix + "note.txt", "text": "hello-rw",
	})
	if isErr || !strings.Contains(text, "wrote "+scopePrefix+"note.txt") {
		t.Fatalf("put: err=%v text=%q", isErr, text)
	}
	hits := rig.upstream.snapshot()
	if len(hits) != 1 {
		t.Fatalf("put upstream calls=%d", len(hits))
	}
	got := hits[0]
	if got.method != http.MethodPut || !strings.HasSuffix(got.path, "/"+scopePrefix+"note.txt") {
		t.Fatalf("upstream put method=%s path=%s", got.method, got.path)
	}
	if !strings.Contains(got.auth, "Credential="+gwMasterKey+"/") {
		t.Fatalf("upstream credential is not the master key: %q", got.auth)
	}
	if strings.Contains(got.auth, cred.kid) || got.sessionToken != "" {
		t.Fatal("agent kid or session token reached upstream")
	}

	before := len(hits)
	text, isErr = toolText(t, ctx, session, "put_object", map[string]any{
		"key": "outside/nope.txt", "text": "nope",
	})
	if !isErr || !saysAccessDenied(text) {
		t.Fatalf("outside put: err=%v text=%q", isErr, text)
	}
	if len(rig.upstream.snapshot()) != before {
		t.Fatal("outside put reached upstream")
	}

	text, isErr = toolText(t, ctx, session, "delete_object", map[string]any{"key": scopePrefix + "note.txt"})
	if isErr || !strings.Contains(text, "deleted "+scopePrefix+"note.txt") {
		t.Fatalf("delete: err=%v text=%q", isErr, text)
	}
	hits = rig.upstream.snapshot()
	last := hits[len(hits)-1]
	if last.method != http.MethodDelete || !strings.HasSuffix(last.path, "/"+scopePrefix+"note.txt") {
		t.Fatalf("upstream delete method=%s path=%s", last.method, last.path)
	}
	assertAudit(t, rig, 1)
}
