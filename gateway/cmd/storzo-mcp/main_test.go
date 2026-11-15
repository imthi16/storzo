package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// testConfig returns a Config pointing at the fake S3 endpoint.
// protects=env wiring; fails_when=endpoint/bucket/prefix/mode are wrong; no_claim=does not prove SigV4 against real SeaweedFS.
func testConfig(endpoint, mode string) Config {
	return Config{
		Endpoint:        endpoint,
		AccessKeyID:     "test-key",
		SecretAccessKey: "test-secret",
		SessionToken:    "test-token",
		Bucket:          "test-bucket",
		Prefix:          "run-42/",
		Mode:            mode,
	}
}

// fakeS3 returns an httptest server speaking just enough path-style S3 for
// list/get/put/delete. GET on /test-bucket/big.bin returns 2 MiB; other GET
// object keys return "hello". ListObjectsV2 returns one key.
// protects=fake data plane; fails_when=XML shape or routing is wrong (SDK parse errors); no_claim=does not prove gateway scope enforcement.
func fakeS3(t *testing.T) *httptest.Server {
	t.Helper()
	big := strings.Repeat("x", 2*1048576)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") == "2" {
			w.Header().Set("Content-Type", "application/xml")
			if r.URL.Query().Get("continuation-token") == "tok-2" {
				fmt.Fprint(w, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`+
					`<Name>test-bucket</Name><Prefix>run-42/</Prefix><KeyCount>1</KeyCount>`+
					`<MaxKeys>100</MaxKeys><IsTruncated>false</IsTruncated>`+
					`<Contents><Key>run-42/b.txt</Key><Size>5</Size>`+
					`<LastModified>2026-01-01T00:00:00.000Z</LastModified></Contents>`+
					`</ListBucketResult>`)
				return
			}
			fmt.Fprint(w, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`+
				`<Name>test-bucket</Name><Prefix>run-42/</Prefix><KeyCount>1</KeyCount>`+
				`<MaxKeys>100</MaxKeys><IsTruncated>true</IsTruncated>`+
				`<NextContinuationToken>tok-2</NextContinuationToken>`+
				`<Contents><Key>run-42/a.txt</Key><Size>5</Size>`+
				`<LastModified>2026-01-01T00:00:00.000Z</LastModified></Contents>`+
				`</ListBucketResult>`)
			return
		}
		switch r.Method {
		case http.MethodGet:
			if strings.HasSuffix(r.URL.Path, "/big.bin") {
				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("Content-Length", fmt.Sprint(len(big)))
				fmt.Fprint(w, big)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/bin.dat") {
				// Invalid UTF-8 binary payload (small but non-text).
				payload := []byte{0xff, 0xfe, 0x00, 0x01}
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
				w.Write(payload)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/file.wasm") {
				payload := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
				w.Header().Set("Content-Type", "application/wasm")
				w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
				w.Write(payload)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/nul.txt") {
				payload := []byte("hello\x00world")
				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
				w.Write(payload)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/tabs.txt") {
				w.Header().Set("Content-Type", "text/plain")
				fmt.Fprint(w, "a\tb\nc\r\nd")
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "hello")
		case http.MethodPut:
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	return httptest.NewServer(handler)
}

// connectPair runs srv on one in-memory transport and connects a client on the
// other, returning the session. Caller must cancel ctx and close the session.
func connectPair(t *testing.T, ctx context.Context, srv *mcp.Server) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, serverTransport) }()
	// Give the server goroutine a moment to start (in-memory connect still
	// requires the server side to be running first).
	time.Sleep(50 * time.Millisecond)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	return session
}

func toolNames(tools []*mcp.Tool) map[string]bool {
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	return names
}

// TestToolsListROHidesWriteTools: ro sessions must not see put/delete.
// protects=mode-gated registration; fails_when=put_object/delete_object appear for ro; no_claim=does not prove the gateway would 403 a raw S3 write.
func TestToolsListROHidesWriteTools(t *testing.T) {
	fake := fakeS3(t)
	defer fake.Close()
	a := &app{cfg: testConfig(fake.URL, "ro"), s3: newS3Client(testConfig(fake.URL, "ro"))}
	srv := buildServer(a)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := connectPair(t, ctx, srv)
	defer session.Close()
	res, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := toolNames(res.Tools)
	for _, want := range []string{"list_objects", "get_object"} {
		if !names[want] {
			t.Errorf("ro tools/list missing %q, got %v", want, names)
		}
	}
	for _, banned := range []string{"put_object", "delete_object"} {
		if names[banned] {
			t.Errorf("ro tools/list must not contain %q", banned)
		}
	}
}

// TestToolsListRWHasAllFour: rw sessions see all four tools.
// protects=full registration in rw; fails_when=any of the four is missing; no_claim=does not prove writes succeed against the gateway.
func TestToolsListRWHasAllFour(t *testing.T) {
	fake := fakeS3(t)
	defer fake.Close()
	cfg := testConfig(fake.URL, "rw")
	a := &app{cfg: cfg, s3: newS3Client(cfg)}
	srv := buildServer(a)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := connectPair(t, ctx, srv)
	defer session.Close()
	res, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := toolNames(res.Tools)
	for _, want := range []string{"list_objects", "get_object", "put_object", "delete_object"} {
		if !names[want] {
			t.Errorf("rw tools/list missing %q, got %v", want, names)
		}
	}
}

// TestGetObjectOver1MiBTooLarge: default get_object on a 2 MiB object fails
// with too_large instead of inlining.
// protects=1 MiB inline cap; fails_when=2 MiB body is inlined without error; no_claim=does not prove streaming or range reads.
func TestGetObjectOver1MiBTooLarge(t *testing.T) {
	fake := fakeS3(t)
	defer fake.Close()
	cfg := testConfig(fake.URL, "rw")
	a := &app{cfg: cfg, s3: newS3Client(cfg)}
	srv := buildServer(a)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := connectPair(t, ctx, srv)
	defer session.Close()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_object",
		Arguments: map[string]any{"key": "run-42/big.bin"},
	})
	text := ""
	if res != nil {
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				text += tc.Text
			}
		}
		if !res.IsError {
			// A non-error result inlining 2 MiB is the failure mode.
			t.Fatalf("expected too_large error, got success with %d chars", len(text))
		}
	}
	combined := text
	if err != nil {
		combined += err.Error()
	}
	if !strings.Contains(combined, "too_large") {
		t.Fatalf("expected too_large in result or error, got result=%q err=%v", text, err)
	}
}

// TestGetObjectBinaryTooLarge: non-UTF8 content is not inlined; metadata +
// too_large is returned instead.
// protects=binary guard; fails_when=binary bytes are inlined as text; no_claim=does not prove content-type sniffing beyond UTF-8 validity.
func TestGetObjectBinaryTooLarge(t *testing.T) {
	fake := fakeS3(t)
	defer fake.Close()
	cfg := testConfig(fake.URL, "rw")
	a := &app{cfg: cfg, s3: newS3Client(cfg)}
	srv := buildServer(a)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := connectPair(t, ctx, srv)
	defer session.Close()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_object",
		Arguments: map[string]any{"key": "run-42/bin.dat"},
	})
	text := ""
	if res != nil {
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				text += tc.Text
			}
		}
		if !res.IsError {
			t.Fatalf("expected too_large error for binary, got success %q", text)
		}
	}
	combined := text
	if err != nil {
		combined += err.Error()
	}
	if !strings.Contains(combined, "too_large") {
		t.Fatalf("expected too_large for binary, got result=%q err=%v", text, err)
	}
}

// TestMissingEnvFailsStartup: empty env fails with a message naming the vars
// and never echoes secret values.
// protects=fail-closed config; fails_when=startup succeeds without env; no_claim=does not prove per-var validation beyond presence + mode shape.
func TestMissingEnvFailsStartup(t *testing.T) {
	_, err := loadConfigFromEnv(func(string) string { return "" })
	if err == nil {
		t.Fatal("expected error for empty env, got nil")
	}
	msg := err.Error()
	for _, name := range []string{"STORZO_ENDPOINT", "STORZO_ACCESS_KEY_ID", "STORZO_SECRET_ACCESS_KEY", "STORZO_SESSION_TOKEN", "STORZO_BUCKET", "STORZO_PREFIX", "STORZO_MODE"} {
		if !strings.Contains(msg, name) {
			t.Errorf("error should name %s, got %q", name, msg)
		}
	}
}

// TestMissingEnvNoSecretLeak: a configured secret must never appear in the
// startup error.
// protects=no secret logging; fails_when=error echoes STORZO_SECRET_ACCESS_KEY value; no_claim=does not audit S3 request logging.
func TestMissingEnvNoSecretLeak(t *testing.T) {
	const secret = "super-secret-value-abc123"
	_, err := loadConfigFromEnv(func(k string) string {
		if k == "STORZO_SECRET_ACCESS_KEY" {
			return secret
		}
		return ""
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("startup error leaked secret value: %q", err.Error())
	}
}

// TestInvalidModeFails: STORZO_MODE other than ro/rw is rejected.
// protects=mode gate; fails_when=unknown mode loads; no_claim=does not prove gateway-side mode agreement.
func TestInvalidModeFails(t *testing.T) {
	_, err := loadConfigFromEnv(func(k string) string {
		if k == "STORZO_MODE" {
			return "admin"
		}
		return "x"
	})
	if err == nil {
		t.Fatal("expected error for bad mode, got nil")
	}
	if !strings.Contains(err.Error(), "STORZO_MODE") {
		t.Errorf("error should mention STORZO_MODE, got %q", err.Error())
	}
}

// callToolText calls a tool and returns its combined text, error flag and call error text.
// protects=test plumbing; fails_when=text extraction misses TextContent; no_claim=does not prove MCP wire framing beyond the in-memory transport.
func callToolText(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args map[string]any) (string, bool, string) {
	t.Helper()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	text := ""
	isErr := false
	if res != nil {
		isErr = res.IsError
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				text += tc.Text
			}
		}
	}
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	return text, isErr, errText
}

func newTestSession(t *testing.T) (*mcp.ClientSession, context.CancelFunc) {
	t.Helper()
	fake := fakeS3(t)
	t.Cleanup(fake.Close)
	cfg := testConfig(fake.URL, "rw")
	a := &app{cfg: cfg, s3: newS3Client(cfg)}
	ctx, cancel := context.WithCancel(context.Background())
	session := connectPair(t, ctx, buildServer(a))
	t.Cleanup(func() { session.Close() })
	return session, cancel
}

// TestListObjectsPagination: truncated listing surfaces token and next page is fetchable.
// protects=pagination contract; fails_when=first page drops IsTruncated/token or token arg is ignored; no_claim=does not prove real S3 pagination.
func TestListObjectsPagination(t *testing.T) {
	session, cancel := newTestSession(t)
	defer cancel()
	ctx := context.Background()
	text, isErr, errText := callToolText(t, ctx, session, "list_objects", map[string]any{})
	if isErr || errText != "" {
		t.Fatalf("first page: isErr=%v err=%q text=%q", isErr, errText, text)
	}
	if !strings.Contains(text, "run-42/a.txt") || !strings.Contains(text, "is_truncated=true") || !strings.Contains(text, "next_continuation_token=tok-2") {
		t.Fatalf("first page missing key/truncation/token: %q", text)
	}
	text2, isErr, errText := callToolText(t, ctx, session, "list_objects", map[string]any{"continuation_token": "tok-2"})
	if isErr || errText != "" {
		t.Fatalf("second page: isErr=%v err=%q text=%q", isErr, errText, text2)
	}
	if !strings.Contains(text2, "run-42/b.txt") || !strings.Contains(text2, "is_truncated=false") {
		t.Fatalf("second page missing key/truncation: %q", text2)
	}
}

// TestGetObjectValidUTF8WithNULTooLarge: valid UTF-8 containing NUL is binary.
// protects=NUL/control guard; fails_when=NUL text is inlined; no_claim=does not prove all binary formats.
func TestGetObjectValidUTF8WithNULTooLarge(t *testing.T) {
	session, cancel := newTestSession(t)
	defer cancel()
	for _, key := range []string{"run-42/nul.txt", "run-42/file.wasm"} {
		text, isErr, errText := callToolText(t, context.Background(), session, "get_object", map[string]any{"key": key})
		combined := text + errText
		if !isErr && errText == "" {
			t.Fatalf("%s: expected too_large error, got success %q", key, text)
		}
		if !strings.Contains(combined, "too_large") {
			t.Fatalf("%s: expected too_large, got text=%q err=%q", key, text, errText)
		}
	}
}

// TestGetObjectTextStillInline: plain text and UTF-8 with tabs/newlines return inline.
// protects=text path; fails_when=allowed whitespace is treated as binary; no_claim=does not prove large text handling.
func TestGetObjectTextStillInline(t *testing.T) {
	session, cancel := newTestSession(t)
	defer cancel()
	text, isErr, errText := callToolText(t, context.Background(), session, "get_object", map[string]any{"key": "run-42/a.txt"})
	if isErr || errText != "" || text != "hello" {
		t.Fatalf("plain text: isErr=%v err=%q text=%q", isErr, errText, text)
	}
	text, isErr, errText = callToolText(t, context.Background(), session, "get_object", map[string]any{"key": "run-42/tabs.txt"})
	if isErr || errText != "" || !strings.Contains(text, "\t") || !strings.Contains(text, "\n") {
		t.Fatalf("tabs/newlines text: isErr=%v err=%q text=%q", isErr, errText, text)
	}
}
