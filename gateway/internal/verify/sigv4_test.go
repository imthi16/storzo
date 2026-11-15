package verify

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got allow", code)
	}
	e, ok := err.(*Error)
	if !ok || e.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

// protects=interoperability with independent botocore header/presigned GET/list/PUT signatures;
// fails_when=a valid signature fails or URL/signature/token tampering passes;
// no_claim=live boto3 transport, AWS CLI or SeaweedFS behavior.
func TestBotocoreRequests(t *testing.T) {
	f := loadFixture(t)
	v, _ := New(f.SigningKey)
	now := time.Unix(f.Now, 0)
	for _, vec := range f.Vectors {
		for _, sample := range vec.Requests {
			t.Run(vec.Claims.Mode+"/"+sample.Method+"/"+sample.URL, func(t *testing.T) {
				makeRequest := func() *http.Request {
					r := httptest.NewRequest(sample.Method, sample.URL, strings.NewReader(sample.Body))
					for k, value := range sample.Headers {
						r.Header.Set(k, value)
					}
					return r
				}
				r := makeRequest()
				c, err := v.Verify(r, now)
				if sample.Method == "PUT" && vec.Claims.Mode == "ro" {
					requireCode(t, err, "AccessDenied")
				} else if err != nil || c != vec.Claims {
					t.Fatalf("valid botocore request: %v", err)
				}
				body, e := io.ReadAll(r.Body)
				if e != nil || string(body) != sample.Body {
					t.Fatalf("body was not restored: %q %v", body, e)
				}
				r = makeRequest()
				r.URL.Path += "tampered"
				r.URL.RawPath = ""
				_, err = v.Verify(r, now)
				requireCode(t, err, "SignatureDoesNotMatch")
				r = makeRequest()
				if r.Header.Get("Authorization") != "" {
					a := r.Header.Get("Authorization")
					last := "0"
					if strings.HasSuffix(a, "0") {
						last = "1"
					}
					r.Header.Set("Authorization", a[:len(a)-1]+last)
				} else {
					q := r.URL.Query()
					sig := q.Get("X-Amz-Signature")
					q.Set("X-Amz-Signature", "f"+sig[1:])
					if q.Get("X-Amz-Signature") == sig {
						q.Set("X-Amz-Signature", "0"+sig[1:])
					}
					r.URL.RawQuery = q.Encode()
				}
				_, err = v.Verify(r, now)
				requireCode(t, err, "SignatureDoesNotMatch")
			})
		}
	}
}

func signedRequest(t *testing.T, v *Verifier, token, kid, method, path, body, payload string, now time.Time, presign bool) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, "https://s3.example.test"+path, strings.NewReader(body))
	r.Header.Set("X-Amz-Content-Sha256", payload)
	creds := aws.Credentials{AccessKeyID: kid, SecretAccessKey: v.Secret(kid), SessionToken: token}
	signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true; o.DisableHeaderHoisting = true })
	if presign {
		q := r.URL.Query()
		q.Set("X-Amz-Expires", "300")
		r.URL.RawQuery = q.Encode()
		signed, _, err := signer.PresignHTTP(r.Context(), creds, r, payload, "s3", "us-east-1", now)
		if err != nil {
			t.Fatal(err)
		}
		r.URL, _ = url.Parse(signed)
	} else if err := signer.SignHTTP(r.Context(), creds, r, payload, "s3", "us-east-1", now); err != nil {
		t.Fatal(err)
	}
	return r
}

// protects=hashed/unsigned payload validation, preserved bodies and signed-header selection;
// fails_when=changed payloads pass or valid SDK signatures fail;
// no_claim=streaming chunk verification, intentionally unsupported.
func TestSDKPayloads(t *testing.T) {
	f := loadFixture(t)
	v, _ := New(f.SigningKey)
	vec := f.Vectors[1]
	now := time.Unix(f.Now, 0)
	sum := sha256.Sum256([]byte("hello"))
	hash := hex.EncodeToString(sum[:])
	for _, presign := range []bool{false, true} {
		for _, payload := range []string{hash, "UNSIGNED-PAYLOAD"} {
			r := signedRequest(t, v, vec.Token, vec.AccessKey, "PUT", "/agent-data/run-42/a%20b", "hello", payload, now, presign)
			r.Header.Set("User-Agent", "ordinary unsigned transport header")
			if _, err := v.Verify(r, now); err != nil {
				t.Fatal(err)
			}
			r.Body = io.NopCloser(strings.NewReader("changed"))
			_, err := v.Verify(r, now)
			if payload != "UNSIGNED-PAYLOAD" {
				requireCode(t, err, "SignatureDoesNotMatch")
			} else if err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, payload := range []string{"STREAMING-AWS4-HMAC-SHA256-PAYLOAD", "STREAMING-UNSIGNED-PAYLOAD-TRAILER", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"} {
		r := signedRequest(t, v, vec.Token, vec.AccessKey, "PUT", "/agent-data/run-42/a", "hello", payload, now, false)
		_, err := v.Verify(r, now)
		requireCode(t, err, "NotImplemented")
	}
	r := signedRequest(t, v, vec.Token, vec.AccessKey, "PUT", "/agent-data/run-42/a", "hello", hash, now, false)
	r.Header.Set("Content-Encoding", "aws-chunked")
	_, err := v.Verify(r, now)
	requireCode(t, err, "NotImplemented")
}

// protects=credential binding, expiry, malformed auth rejection and authenticated scope denials;
// fails_when=an expired, mismatched, unsupported or out-of-scope request passes;
// no_claim=proxying, audit emission, credential revocation.
func TestRequestDenials(t *testing.T) {
	f := loadFixture(t)
	v, _ := New(f.SigningKey)
	vec := f.Vectors[0]
	now := time.Unix(f.Now, 0)
	tests := []struct {
		name   string
		change func(*http.Request)
		at     time.Time
		code   string
	}{
		{"other-bucket", func(r *http.Request) {}, now, "AccessDenied"},
		{"missing-auth", func(r *http.Request) { r.Header.Del("Authorization") }, now, "SignatureDoesNotMatch"},
		{"bad-token", func(r *http.Request) { r.Header.Set("X-Amz-Security-Token", vec.Token+"x") }, now, "SignatureDoesNotMatch"},
		{"missing-token", func(r *http.Request) { r.Header.Del("X-Amz-Security-Token") }, now, "SignatureDoesNotMatch"},
		{"duplicate-date", func(r *http.Request) { r.Header.Add("X-Amz-Date", r.Header.Get("X-Amz-Date")) }, now, "SignatureDoesNotMatch"},
		{"future-signature", func(r *http.Request) {}, now.Add(-61 * time.Second), "SignatureDoesNotMatch"},
		{"expired", func(r *http.Request) {}, time.Unix(vec.Claims.Exp+61, 0), "ExpiredToken"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := "/agent-data/run-42/a"
			if tt.name == "other-bucket" {
				path = "/other/run-42/a"
			}
			r := signedRequest(t, v, vec.Token, vec.AccessKey, "GET", path, "", "UNSIGNED-PAYLOAD", now, false)
			tt.change(r)
			_, err := v.Verify(r, tt.at)
			requireCode(t, err, tt.code)
		})
	}
	// A valid token belonging to a different kid cannot authenticate this request.
	r := signedRequest(t, v, vec.Token, f.Vectors[1].AccessKey, "GET", "/agent-data/run-42/a", "", "UNSIGNED-PAYLOAD", now, false)
	_, err := v.Verify(r, now)
	requireCode(t, err, "SignatureDoesNotMatch")
	for _, path := range []string{"/agent-data/run-42/%2e%2e/a", "/agent-data/%2Frun-42/a", "/agent-data/run-42/a%5Cb", "/agent-data/run-42/%00"} {
		r := signedRequest(t, v, vec.Token, vec.AccessKey, "GET", path, "", "UNSIGNED-PAYLOAD", now, false)
		_, err := v.Verify(r, now)
		requireCode(t, err, "InvalidArgument")
	}
	r = signedRequest(t, v, vec.Token, vec.AccessKey, "PUT", "/agent-data/run-42/a", "", "UNSIGNED-PAYLOAD", now, false)
	r.Header.Set("X-Amz-Copy-Source", "/agent-data/run-42/b")
	_, err = v.Verify(r, now)
	requireCode(t, err, "NotImplemented")
	// Reject mixed header/query authentication instead of choosing one.
	r = signedRequest(t, v, vec.Token, vec.AccessKey, "GET", "/agent-data/run-42/a", "", "UNSIGNED-PAYLOAD", now, true)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 anything")
	_, err = v.Verify(r, now)
	requireCode(t, err, "SignatureDoesNotMatch")
	_, err = v.Verify(nil, now)
	requireCode(t, err, "SignatureDoesNotMatch")
}

// protects=presigned lifetime and authenticated query values;
// fails_when=expired presigns or altered query values pass; no_claim=network clock synchronization.
func TestPresignLifetimeAndQuery(t *testing.T) {
	f := loadFixture(t)
	v, _ := New(f.SigningKey)
	vec := f.Vectors[0]
	now := time.Unix(f.Now, 0)
	r := signedRequest(t, v, vec.Token, vec.AccessKey, "GET", "/agent-data?list-type=2&prefix=run-42%2F", "", "UNSIGNED-PAYLOAD", now, true)
	if _, err := v.Verify(r, now.Add(360*time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err := v.Verify(r, now.Add(361*time.Second))
	requireCode(t, err, "SignatureDoesNotMatch")
	q := r.URL.Query()
	q.Set("prefix", "other/")
	r.URL.RawQuery = q.Encode()
	_, err = v.Verify(r, now)
	requireCode(t, err, "SignatureDoesNotMatch")
}

// protects=payload read failures fail closed; fails_when=unreadable bodies authenticate;
// no_claim=transport retry behavior.
func TestBodyReadFailure(t *testing.T) {
	f := loadFixture(t)
	v, _ := New(f.SigningKey)
	vec := f.Vectors[1]
	now := time.Unix(f.Now, 0)
	r := signedRequest(t, v, vec.Token, vec.AccessKey, "PUT", "/agent-data/run-42/a", "", "bad-hash", now, false)
	r.Body = io.NopCloser(&failingReader{})
	_, err := v.Verify(r, now)
	requireCode(t, err, "SignatureDoesNotMatch")
}

type failingReader struct{}

func (*failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// protects=canonical headers/paths are compared without mutating request metadata;
// fails_when=verification rewrites original auth, URL or headers; no_claim=concurrent reuse of request bodies.
func TestRequestMetadataPreserved(t *testing.T) {
	f := loadFixture(t)
	v, _ := New(f.SigningKey)
	vec := f.Vectors[0]
	now := time.Unix(f.Now, 0)
	r := signedRequest(t, v, vec.Token, vec.AccessKey, "GET", "/agent-data/run-42/a%2Bb", "", "UNSIGNED-PAYLOAD", now, false)
	before := r.URL.String()
	auth := r.Header.Get("Authorization")
	if _, err := v.Verify(r, now); err != nil {
		t.Fatal(err)
	}
	if r.URL.String() != before || r.Header.Get("Authorization") != auth {
		t.Fatal("metadata mutated")
	}
	b, _ := io.ReadAll(r.Body)
	if !bytes.Equal(b, []byte{}) {
		t.Fatal("body changed")
	}
}

// presignQueryPayload signs like signedRequest, with the payload hash only in the query.
func presignQueryPayload(t *testing.T, v *Verifier, token, kid, method, path, body, payload string, now time.Time) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, "https://s3.example.test"+path, strings.NewReader(body))
	q := r.URL.Query()
	q.Set("X-Amz-Expires", "300")
	q.Set("X-Amz-Content-Sha256", payload)
	r.URL.RawQuery = q.Encode()
	creds := aws.Credentials{AccessKeyID: kid, SecretAccessKey: v.Secret(kid), SessionToken: token}
	signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true; o.DisableHeaderHoisting = true })
	signed, _, err := signer.PresignHTTP(r.Context(), creds, r, payload, "s3", "us-east-1", now)
	if err != nil {
		t.Fatal(err)
	}
	r.URL, err = url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	if r.Header.Get("X-Amz-Content-Sha256") != "" || r.URL.Query().Get("X-Amz-Content-Sha256") != payload {
		t.Fatalf("payload hash was not kept only in the query: header %q query %q", r.Header.Get("X-Amz-Content-Sha256"), r.URL.RawQuery)
	}
	return r
}

// protects=presigned GET whose payload hash is a query parameter, inside scope;
// fails_when=a valid query hash is denied or a wrong hash is accepted;
// no_claim=header-carried hashes, which the other payload tests cover.
func TestPresignedQueryContentHash(t *testing.T) {
	f := loadFixture(t)
	v, _ := New(f.SigningKey)
	vec := f.Vectors[0]
	now := time.Unix(f.Now, 0)
	sum := sha256.Sum256(nil)
	hash := hex.EncodeToString(sum[:])
	r := presignQueryPayload(t, v, vec.Token, vec.AccessKey, "GET", "/agent-data/run-42/a", "", hash, now)
	c, err := v.Verify(r, now)
	if err != nil || c != vec.Claims {
		t.Fatalf("presigned query hash: %v", err)
	}
	q := r.URL.Query()
	q.Set("X-Amz-Content-Sha256", strings.Repeat("0", 64))
	r.URL.RawQuery = q.Encode()
	_, err = v.Verify(r, now)
	requireCode(t, err, "SignatureDoesNotMatch")
}
