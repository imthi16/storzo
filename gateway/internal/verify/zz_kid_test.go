package verify

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

func kidSign(t *testing.T, creds aws.Credentials, method, path, body, payload string, headers http.Header, now time.Time) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, "http://gateway.test"+path, strings.NewReader(body))
	for name, values := range headers {
		r.Header[name] = append([]string(nil), values...)
	}
	r.Header.Set("X-Amz-Content-Sha256", payload)
	signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
	if err := signer.SignHTTP(r.Context(), creds, r, payload, "s3", "us-east-1", now); err != nil {
		t.Fatal(err)
	}
	return r
}

func kidMint(t *testing.T, v *Verifier, c Claims) string {
	t.Helper()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	return body + "." + base64.RawURLEncoding.EncodeToString(v.mac(body))
}

// protects=authenticated scope/unsupported-operation denials return the verified claims without granting access;
// fails_when=denials lose identity, return altered claims, or become successful;
// no_claim=nonempty claims authorize any request when err is nonnil.
func TestKidAuthenticatedDenials(t *testing.T) {
	f := loadFixture(t)
	v, err := NewWithMaster(f.SigningKey, "kid-master", "kid-master-secret")
	if err != nil {
		t.Fatal(err)
	}
	vec := f.Vectors[0]
	now := time.Unix(f.Now, 0)
	for _, tc := range []struct {
		name, method, path, code string
		headers                  http.Header
		master                   bool
	}{
		{name: "other-prefix", method: "GET", path: "/agent-data/run-43/a", code: "AccessDenied"},
		{name: "other-bucket", method: "GET", path: "/other-data/run-42/a", code: "AccessDenied"},
		{name: "list-no-prefix", method: "GET", path: "/agent-data?list-type=2", code: "AccessDenied"},
		{name: "ro-put", method: "PUT", path: "/agent-data/run-42/a", code: "AccessDenied"},
		{name: "invalid-key", method: "GET", path: "/agent-data/run-42/../a", code: "InvalidArgument"},
		{name: "copy", method: "PUT", path: "/agent-data/run-42/a", code: "NotImplemented", headers: http.Header{"X-Amz-Copy-Source": {"/agent-data/run-42/b"}}},
		{name: "master-copy", method: "PUT", path: "/outside/a", code: "NotImplemented", headers: http.Header{"X-Amz-Copy-Source": {"/outside/b"}}, master: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creds := aws.Credentials{AccessKeyID: vec.AccessKey, SecretAccessKey: vec.Secret, SessionToken: vec.Token}
			want := vec.Claims
			if tc.master {
				creds = aws.Credentials{AccessKeyID: "kid-master", SecretAccessKey: "kid-master-secret"}
				want = Claims{Master: true, Kid: "kid-master"}
			}
			r := kidSign(t, creds, tc.method, tc.path, "", "UNSIGNED-PAYLOAD", tc.headers, now)
			got, err := v.Verify(r, now)
			requireCode(t, err, tc.code)
			if got != want {
				t.Fatalf("authenticated denial claims=%+v want=%+v", got, want)
			}
		})
	}
}

// protects=every tested authentication failure returns zero claims despite a claimed or MAC-valid identity;
// fails_when=any failed auth check exposes a kid, master bit, or partial token claims;
// no_claim=exhaustive fuzzing of every possible malformed HTTP request.
func TestKidAuthenticationFailuresAreEmpty(t *testing.T) {
	f := loadFixture(t)
	v, err := NewWithMaster(f.SigningKey, "kid-master", "kid-master-secret")
	if err != nil {
		t.Fatal(err)
	}
	vec := f.Vectors[0]
	now := time.Unix(f.Now, 0)
	baseCreds := aws.Credentials{AccessKeyID: vec.AccessKey, SecretAccessKey: vec.Secret, SessionToken: vec.Token}
	sum := sha256.Sum256([]byte("body"))
	payload := hex.EncodeToString(sum[:])
	sign := func(creds aws.Credentials) *http.Request {
		return kidSign(t, creds, "PUT", "/agent-data/run-42/a", "body", payload, nil, now)
	}
	flipSignature := func(r *http.Request) {
		prefix, sig, _ := strings.Cut(r.Header.Get("Authorization"), ", Signature=")
		raw, err := hex.DecodeString(sig)
		if err != nil {
			t.Fatal(err)
		}
		raw[0] ^= 1
		r.Header.Set("Authorization", prefix+", Signature="+hex.EncodeToString(raw))
	}
	for _, tc := range []struct {
		name, code string
		make       func() *http.Request
	}{
		{"bad-signature", "SignatureDoesNotMatch", func() *http.Request {
			r := sign(baseCreds)
			prefix, _, _ := strings.Cut(r.Header.Get("Authorization"), ", Signature=")
			r.Header.Set("Authorization", prefix+", Signature="+strings.Repeat("0", 64))
			return r
		}},
		{"flipped-signature-byte", "SignatureDoesNotMatch", func() *http.Request {
			r := sign(baseCreds)
			flipSignature(r)
			return r
		}},
		{"wrong-secret", "SignatureDoesNotMatch", func() *http.Request {
			creds := baseCreds
			creds.SecretAccessKey = "wrong-secret"
			return sign(creds)
		}},
		{"tampered-token-claims", "SignatureDoesNotMatch", func() *http.Request {
			claims := vec.Claims
			claims.Prefix = "run-99/"
			raw, _ := json.Marshal(claims)
			_, mac, _ := strings.Cut(vec.Token, ".")
			creds := baseCreds
			creds.SessionToken = base64.RawURLEncoding.EncodeToString(raw) + "." + mac
			return sign(creds)
		}},
		{"flipped-token-mac", "SignatureDoesNotMatch", func() *http.Request {
			body, signature, _ := strings.Cut(vec.Token, ".")
			raw, err := base64.RawURLEncoding.DecodeString(signature)
			if err != nil {
				t.Fatal(err)
			}
			raw[0] ^= 1
			creds := baseCreds
			creds.SessionToken = body + "." + base64.RawURLEncoding.EncodeToString(raw)
			return sign(creds)
		}},
		{"malformed-token", "SignatureDoesNotMatch", func() *http.Request {
			creds := baseCreds
			creds.SessionToken = "not.a.valid.token"
			return sign(creds)
		}},
		{"expired-token", "ExpiredToken", func() *http.Request {
			claims := vec.Claims
			claims.Iat, claims.Exp = now.Unix()-3600, now.Unix()-61
			creds := baseCreds
			creds.SessionToken = kidMint(t, v, claims)
			return sign(creds)
		}},
		{"missing-token-header", "SignatureDoesNotMatch", func() *http.Request {
			r := sign(baseCreds)
			r.Header.Del("X-Amz-Security-Token")
			return r
		}},
		{"unsigned-token-header", "SignatureDoesNotMatch", func() *http.Request {
			creds := baseCreds
			creds.SessionToken = ""
			r := sign(creds)
			r.Header.Set("X-Amz-Security-Token", vec.Token)
			return r
		}},
		{"forged-credential-kid", "SignatureDoesNotMatch", func() *http.Request {
			creds := baseCreds
			creds.AccessKeyID = "STZBBBBBBBBBBBBBBBB"
			creds.SecretAccessKey = v.Secret(creds.AccessKeyID)
			return sign(creds)
		}},
		{"missing-auth", "SignatureDoesNotMatch", func() *http.Request {
			r := sign(baseCreds)
			r.Header.Del("Authorization")
			return r
		}},
		{"payload-mismatch", "SignatureDoesNotMatch", func() *http.Request {
			r := sign(baseCreds)
			r.Body = io.NopCloser(strings.NewReader("evil"))
			return r
		}},
		{"unreadable-body", "SignatureDoesNotMatch", func() *http.Request {
			r := sign(baseCreds)
			r.Body = io.NopCloser(&failingReader{})
			return r
		}},
		{"oversized-body", "EntityTooLarge", func() *http.Request {
			r := sign(baseCreds)
			r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, 1)
			return r
		}},
		{"master-wrong-secret", "SignatureDoesNotMatch", func() *http.Request {
			return kidSign(t, aws.Credentials{AccessKeyID: "kid-master", SecretAccessKey: "wrong"}, "GET", "/outside/a", "", "UNSIGNED-PAYLOAD", nil, now)
		}},
		{"early-unsupported", "NotImplemented", func() *http.Request {
			return kidSign(t, baseCreds, "PUT", "/agent-data/run-42/a", "", "UNSIGNED-PAYLOAD", http.Header{"Content-Encoding": {"aws-chunked"}}, now)
		}},
		{"presign-wrong-signature", "SignatureDoesNotMatch", func() *http.Request {
			r := signedRequest(t, v, vec.Token, vec.AccessKey, "GET", "/agent-data/run-42/a", "", "UNSIGNED-PAYLOAD", now, true)
			q := r.URL.Query()
			q.Set("X-Amz-Signature", strings.Repeat("0", 64))
			r.URL.RawQuery = q.Encode()
			return r
		}},
		{"nil-request", "SignatureDoesNotMatch", func() *http.Request { return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := v.Verify(tc.make(), now)
			requireCode(t, err, tc.code)
			if got != (Claims{}) {
				t.Fatalf("authentication failure exposed claims=%+v", got)
			}
		})
	}
	for _, header := range []string{"Authorization", "X-Amz-Date", "X-Amz-Security-Token", "X-Amz-Content-Sha256"} {
		t.Run("duplicate-"+header, func(t *testing.T) {
			r := sign(baseCreds)
			r.Header.Add(header, r.Header.Get(header))
			got, err := v.Verify(r, now)
			requireCode(t, err, "SignatureDoesNotMatch")
			if got != (Claims{}) {
				t.Fatalf("duplicate header exposed claims=%+v", got)
			}
		})
	}
}

// protects=stale/future dates with bad signatures cannot expose either scoped or master identity before signature comparison;
// fails_when=the date-window rejection returns any claims instead of Claims{};
// no_claim=all clock-skew boundaries or token expiry semantics beyond the existing vectors.
func TestKidDateWindowBadSignatureEmpty(t *testing.T) {
	f := loadFixture(t)
	v, err := NewWithMaster(f.SigningKey, "kid-master", "kid-master-secret")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(f.Now, 0)
	vec := f.Vectors[0]
	if _, err := v.Token(vec.Token, now); err != nil {
		t.Fatalf("scoped fixture must be unexpired: %v", err)
	}
	for _, master := range []bool{false, true} {
		name := "scoped"
		creds := aws.Credentials{AccessKeyID: vec.AccessKey, SecretAccessKey: "incorrect-signing-secret", SessionToken: vec.Token}
		if master {
			name = "master"
			creds = aws.Credentials{AccessKeyID: "kid-master", SecretAccessKey: "incorrect-signing-secret"}
		}
		for _, tc := range []struct {
			name string
			at   time.Time
		}{
			{"stale", now.Add(-headerValidity - ExpirySkew - time.Second)},
			{"future", now.Add(ExpirySkew + time.Second)},
		} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				r := kidSign(t, creds, "GET", "/agent-data/run-42/a", "", "UNSIGNED-PAYLOAD", nil, tc.at)
				got, err := v.Verify(r, now)
				requireCode(t, err, "SignatureDoesNotMatch")
				if got != (Claims{}) {
					t.Fatalf("date-window failure exposed claims=%+v", got)
				}
			})
		}
	}
}

// protects=implicit-hash read failures reveal no scoped or master claims before signature comparison;
// fails_when=the implicit-hash error returns partial claims or the fixture never reaches body reading;
// no_claim=successful implicit-hash buffering is avoided.
func TestKidImplicitReadFailureEmpty(t *testing.T) {
	f := loadFixture(t)
	v, err := NewWithMaster(f.SigningKey, "kid-master", "kid-master-secret")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(f.Now, 0)
	vec := f.Vectors[0]
	sum := sha256.Sum256([]byte("hello"))
	hash := hex.EncodeToString(sum[:])
	for _, master := range []bool{false, true} {
		name := "scoped"
		creds := aws.Credentials{AccessKeyID: vec.AccessKey, SecretAccessKey: vec.Secret, SessionToken: vec.Token}
		if master {
			name = "master"
			creds = aws.Credentials{AccessKeyID: "kid-master", SecretAccessKey: "kid-master-secret"}
		}
		t.Run(name, func(t *testing.T) {
			r := kidSign(t, creds, "PUT", "/agent-data/run-42/a", "hello", hash, nil, now)
			r.Header.Del("X-Amz-Content-Sha256")
			if err := v4.NewSigner().SignHTTP(r.Context(), creds, r, hash, "s3", "us-east-1", now); err != nil {
				t.Fatal(err)
			}
			if r.Header.Get("X-Amz-Content-Sha256") != "" || strings.Contains(r.Header.Get("Authorization"), "x-amz-content-sha256") {
				t.Fatal("fixture must sign without a declared hash header")
			}
			reader := &zzCountingReader{Reader: &failingReader{}}
			r.Body = io.NopCloser(reader)
			got, err := v.Verify(r, now)
			requireCode(t, err, "SignatureDoesNotMatch")
			if reader.reads == 0 {
				t.Fatal("fixture did not reach implicit body hashing")
			}
			if got != (Claims{}) {
				t.Fatalf("implicit body-read failure exposed claims=%+v", got)
			}
		})
	}
}

// protects=a master signature valid over a declared hash does not authenticate a mismatching body;
// fails_when=payload mismatch returns any master claims or a matching-body control fails;
// no_claim=the master key is safe to disclose.
func TestKidMasterDeclaredMismatchEmpty(t *testing.T) {
	f := loadFixture(t)
	v, err := NewWithMaster(f.SigningKey, "kid-master", "kid-master-secret")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(f.Now, 0)
	sum := sha256.Sum256([]byte("hello"))
	hash := hex.EncodeToString(sum[:])
	creds := aws.Credentials{AccessKeyID: "kid-master", SecretAccessKey: "kid-master-secret"}
	for _, body := range []string{"hello", "jello"} {
		t.Run(body, func(t *testing.T) {
			r := kidSign(t, creds, "PUT", "/outside/key", "hello", hash, nil, now)
			r.Body = io.NopCloser(strings.NewReader(body))
			got, err := v.Verify(r, now)
			if body == "hello" {
				if err != nil || got != (Claims{Master: true, Kid: "kid-master"}) {
					t.Fatalf("valid control: claims=%+v err=%v", got, err)
				}
			} else {
				requireCode(t, err, "SignatureDoesNotMatch")
				if got != (Claims{}) {
					t.Fatalf("master payload mismatch exposed claims=%+v", got)
				}
			}
		})
	}
}
