package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/imthi16/storzo/gateway/internal/verify"
)

// protects=only allowlisted x-amz controls are master-signed for scoped PUT/DELETE, whether signed or attached later;
// fails_when=any forbidden control reaches storage or metadata/checksum headers disappear;
// no_claim=SeaweedFS applies object retention or validates the fixture checksums.
func TestZZScopedHeaderAllowlist(t *testing.T) {
	dropped := []string{
		"X-Amz-Acl", "X-Amz-Grant-Read", "X-Amz-Grant-Write", "X-Amz-Grant-Read-Acp",
		"X-Amz-Grant-Write-Acp", "X-Amz-Grant-Full-Control", "X-Amz-Tagging", "X-Amz-Tagging-Directive",
		"X-Amz-Object-Lock-Mode", "X-Amz-Object-Lock-Retain-Until-Date", "X-Amz-Object-Lock-Legal-Hold",
		"X-Amz-Bypass-Governance-Retention", "X-Amz-Server-Side-Encryption",
		"X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id", "X-Amz-Server-Side-Encryption-Customer-Key",
		"X-Amz-Storage-Class", "X-Amz-Website-Redirect-Location", "X-Amz-Future-Privileged-Control",
	}
	kept := []string{"X-Amz-Meta-Label", "X-Amz-Checksum-Sha256", "X-Amz-Checksum-Crc32", "X-Amz-Sdk-Checksum-Algorithm"}
	for _, method := range []string{"PUT", "DELETE"} {
		for _, signed := range []bool{true, false} {
			name := method + "/unsigned"
			if signed {
				name = method + "/signed"
			}
			t.Run(name, func(t *testing.T) {
				seen := make(chan http.Header, 1)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					checkMaster(t, r)
					_, _ = io.Copy(io.Discard, r.Body)
					seen <- r.Header.Clone()
					w.WriteHeader(200)
				}))
				defer upstream.Close()
				h, err := New(config(upstream.URL), nil)
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest(method, "http://gateway.test/agent-data/run-42/a", strings.NewReader("body"))
				setControls := func() {
					for _, name := range append(append([]string{}, dropped...), kept...) {
						r.Header.Set(name, "fixture")
					}
				}
				if signed {
					setControls()
				}
				r.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
				v, _ := verify.New(signingKey)
				creds := aws.Credentials{AccessKeyID: agentKey, SecretAccessKey: v.Secret(agentKey), SessionToken: token(t, "rw", false)}
				if err := v4.NewSigner().SignHTTP(r.Context(), creds, r, "UNSIGNED-PAYLOAD", "s3", "us-east-1", time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				if !signed {
					setControls()
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 200 {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
				headers := <-seen
				for _, name := range dropped {
					if value := headers.Get(name); value != "" {
						t.Errorf("forbidden header forwarded: %s=%q", name, value)
					}
				}
				for _, name := range kept {
					if headers.Get(name) != "fixture" {
						t.Errorf("allowed header lost: %s", name)
					}
				}
				if headers.Get("X-Amz-Content-Sha256") != "UNSIGNED-PAYLOAD" || headers.Get("X-Amz-Date") == "" {
					t.Error("verified payload/date lost")
				}
			})
		}
	}
}

// protects=authenticated master requests retain object-lock controls and existing ACL stripping;
// fails_when=master object-lock headers disappear or ACL headers reach storage;
// no_claim=the storage server enforces retention.
func TestZZMasterObjectLockPassThrough(t *testing.T) {
	seen := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkMaster(t, r)
		_, _ = io.Copy(io.Discard, r.Body)
		seen <- r.Header.Clone()
	}))
	defer upstream.Close()
	h, err := New(config(upstream.URL), nil)
	if err != nil {
		t.Fatal(err)
	}
	extra := http.Header{"X-Amz-Object-Lock-Mode": {"COMPLIANCE"}, "X-Amz-Acl": {"public-read"}}
	r := sign(t, "PUT", "/outside/key", "", "UNSIGNED-PAYLOAD", true, false, false, extra)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	headers := <-seen
	if headers.Get("X-Amz-Object-Lock-Mode") != "COMPLIANCE" || headers.Get("X-Amz-Acl") != "" {
		t.Fatalf("master controls=%v", headers)
	}
}

// protects=denials cannot impersonate a victim kid while attempted targets remain visible;
// fails_when=unauthenticated header/query credentials populate Kid or a denial forwards;
// no_claim=the attempted bucket/key were authorized.
func TestZZDeniedAuditHasNoKid(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("denial forwarded")
	}))
	defer upstream.Close()
	for _, presign := range []bool{false, true} {
		log := &auditLog{}
		h, err := New(config(upstream.URL), log)
		if err != nil {
			t.Fatal(err)
		}
		r := sign(t, "GET", "/victim-bucket/secret-key", "", "UNSIGNED-PAYLOAD", true, presign, false, nil)
		if presign {
			q := r.URL.Query()
			q.Set("X-Amz-Signature", strings.Repeat("0", 64))
			r.URL.RawQuery = q.Encode()
		} else {
			auth, _, _ := strings.Cut(r.Header.Get("Authorization"), ", Signature=")
			r.Header.Set("Authorization", auth+", Signature="+strings.Repeat("0", 64))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 || len(log.events) != 1 {
			t.Fatalf("status=%d events=%+v", w.Code, log.events)
		}
		event := log.events[0]
		if event.Kid != "" || event.Bucket != "victim-bucket" || event.Key != "secret-key" || event.Decision != "deny" {
			t.Fatalf("unauthenticated attribution=%+v", event)
		}
	}
}
