package verify

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// protects=every scope matrix row; fails_when=an operation crosses scope or mode;
// no_claim=proxy behavior or authentication.
func TestScopeMatrix(t *testing.T) {
	tests := []struct {
		name, method, path string
		ro, rw             int
		copy               bool
	}{
		{"get", "GET", "/agent-data/run-42/a", 0, 0, false},
		{"head", "HEAD", "/agent-data/run-42/a", 0, 0, false},
		{"list", "GET", "/agent-data?list-type=2&prefix=run-42/", 0, 0, false},
		{"list-child", "GET", "/agent-data?list-type=2&prefix=run-42/sub/", 0, 0, false},
		{"list-missing", "GET", "/agent-data?list-type=2", 403, 403, false},
		{"list-outside", "GET", "/agent-data?list-type=2&prefix=run-4", 403, 403, false},
		{"put", "PUT", "/agent-data/run-42/a", 403, 0, false},
		{"create-multipart", "POST", "/agent-data/run-42/a?uploads", 403, 0, false},
		{"upload-part", "PUT", "/agent-data/run-42/a?uploadId=x&partNumber=1", 403, 0, false},
		{"complete", "POST", "/agent-data/run-42/a?uploadId=x", 403, 0, false},
		{"abort", "DELETE", "/agent-data/run-42/a?uploadId=x", 403, 0, false},
		{"delete", "DELETE", "/agent-data/run-42/a", 403, 0, false},
		{"copy", "PUT", "/agent-data/run-42/a", 501, 501, true},
		{"create-bucket", "PUT", "/agent-data", 403, 403, false},
		{"delete-bucket", "DELETE", "/agent-data", 403, 403, false},
		{"bucket-acl", "GET", "/agent-data?acl", 403, 403, false},
		{"bucket-policy", "PUT", "/agent-data?policy", 403, 403, false},
		{"bucket-versioning", "PUT", "/agent-data?versioning", 403, 403, false},
		{"object-acl", "PUT", "/agent-data/run-42/a?acl", 403, 403, false},
		{"other-bucket", "GET", "/other/run-42/a", 403, 403, false},
		{"outside-prefix", "GET", "/agent-data/run-43/a", 403, 403, false},
		{"invalid-key", "GET", "/agent-data/run-42/../a", 400, 400, false},
		{"empty-upload-id", "PUT", "/agent-data/run-42/a?uploadId=", 403, 403, false},
		{"object-response-override", "GET", "/agent-data/run-42/a?response-content-type=text%2Fplain", 0, 0, false},
		{"sdk-operation-id", "GET", "/agent-data/run-42/a?x-id=GetObject", 0, 0, false},
		{"bucket-head", "HEAD", "/agent-data", 403, 403, false},
		{"bucket-list-v1", "GET", "/agent-data?prefix=run-42/", 403, 403, false},
		{"bucket-multipart", "GET", "/agent-data?uploads&prefix=run-42/", 403, 403, false},
		{"list-plus-acl", "GET", "/agent-data?list-type=2&prefix=run-42/&acl", 403, 403, false},
		{"unknown-method", "PATCH", "/agent-data/run-42/a", 403, 403, false},
		{"duplicate-prefix", "GET", "/agent-data?list-type=2&prefix=run-42/&prefix=other", 403, 403, false},
	}
	for _, tt := range tests {
		for _, mode := range []string{"ro", "rw"} {
			t.Run(tt.name+"/"+mode, func(t *testing.T) {
				r := httptest.NewRequest(tt.method, tt.path, nil)
				if tt.copy {
					r.Header.Set("X-Amz-Copy-Source", "/agent-data/run-42/b")
				}
				want := tt.ro
				if mode == "rw" {
					want = tt.rw
				}
				err := Scope(r, Claims{Bucket: "agent-data", Prefix: "run-42/", Mode: mode})
				got := 0
				if err != nil {
					got = err.(*Error).Status
				}
				if got != want {
					t.Fatalf("got %v; want %d", err, want)
				}
			})
		}
	}
}

// protects=safe_key rules on decoded paths and byte length; fails_when=unsafe keys pass;
// no_claim=upstream decoding or HTTP routing configuration.
func TestKeyNormalization(t *testing.T) {
	for _, key := range []string{"", "../a", "run-42/../a", "/run-42/a", `run-42\a`, "run-42/\x00", strings.Repeat("a", 1025), strings.Repeat("é", 513)} {
		t.Run(url.PathEscape(key), func(t *testing.T) {
			if SafeKey(key) == nil {
				t.Fatal("unsafe key accepted")
			}
		})
	}
	for _, path := range []string{"/agent-data/run-42/%2e%2e/a", "/agent-data/run-42/%2E./a", "/agent-data/%2Frun-42/a", "/agent-data/run-42/a%5Cb", "/agent-data/run-42/%00", "/agent-data/" + strings.Repeat("a", 1025)} {
		r := httptest.NewRequest("GET", path, nil)
		err := Scope(r, Claims{Bucket: "agent-data", Prefix: "run-42/", Mode: "rw"})
		if err == nil || err.(*Error).Status != 400 {
			t.Fatalf("%q: %v", path, err)
		}
	}
	for _, key := range []string{"run-42/a..b", "run-42/./a", "run-42//a", strings.Repeat("é", 512)} {
		if err := SafeKey(key); err != nil {
			t.Fatalf("valid key rejected: %v", err)
		}
	}
}
