package verify

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

type zzCountingReader struct {
	io.Reader
	reads int
}

func (r *zzCountingReader) Read(p []byte) (int, error) {
	r.reads++
	return r.Reader.Read(p)
}

// protects=declared-hash master signatures authenticate before body reads while valid bodies are verified/restored;
// fails_when=bad signatures consume a body, valid bodies fail, or mismatched bodies authenticate;
// no_claim=implicit hashes can avoid reading their body or transport-level memory limits.
func TestZZMasterDeclaredHashBeforeBody(t *testing.T) {
	f := loadFixture(t)
	v, err := NewWithMaster(f.SigningKey, "zz-master", "zz-master-secret")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(f.Now, 0)
	sum := sha256.Sum256([]byte("original"))
	hash := hex.EncodeToString(sum[:])
	for _, tc := range []struct {
		name, body, secret string
		valid, read        bool
	}{
		{"bad-signature", "original", "wrong-secret", false, false},
		{"good-signature", "original", "zz-master-secret", true, true},
		{"body-mismatch", "changed!", "zz-master-secret", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &zzCountingReader{Reader: strings.NewReader(tc.body)}
			r := httptest.NewRequest("PUT", "http://gateway.test/bucket/key", reader)
			r.ContentLength = int64(len(tc.body))
			r.Header.Set("X-Amz-Content-Sha256", hash)
			creds := aws.Credentials{AccessKeyID: "zz-master", SecretAccessKey: tc.secret}
			if err := v4.NewSigner().SignHTTP(r.Context(), creds, r, hash, "s3", "us-east-1", now); err != nil {
				t.Fatal(err)
			}
			c, err := v.Verify(r, now)
			if tc.valid {
				if err != nil || c.Kid != "zz-master" {
					t.Fatalf("valid request: claims=%+v err=%v", c, err)
				}
				got, err := io.ReadAll(r.Body)
				if err != nil || string(got) != tc.body {
					t.Fatalf("restored body=%q err=%v", got, err)
				}
			} else {
				requireCode(t, err, "SignatureDoesNotMatch")
			}
			if (reader.reads > 0) != tc.read {
				t.Fatalf("body reads=%d, want any read=%v", reader.reads, tc.read)
			}
		})
	}
}

// protects=HMAC-valid tokens must have a directory prefix ending in slash;
// fails_when=prefix run-42 is accepted and could match run-420;
// no_claim=all alternative issuers enforce the same construction rules.
func TestZZTokenRequiresTrailingSlash(t *testing.T) {
	f := loadFixture(t)
	v, _ := New(f.SigningKey)
	c := f.Vectors[0].Claims
	c.Prefix = "run-42"
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	token := body + "." + base64.RawURLEncoding.EncodeToString(v.mac(body))
	_, err = v.Token(token, time.Unix(f.Now, 0))
	requireCode(t, err, "SignatureDoesNotMatch")
}

// protects=header-signed scoped requests bind their token, while normal signed and presigned flows remain valid;
// fails_when=an unsigned attached token authenticates or correctly bound tokens fail;
// no_claim=two tokens with the same kid are issued by the HTTP API.
func TestZZScopedTokenMustBeSigned(t *testing.T) {
	f := loadFixture(t)
	v, _ := New(f.SigningKey)
	vec := f.Vectors[1]
	now := time.Unix(f.Now, 0)
	r := signedRequest(t, v, "", vec.AccessKey, "DELETE", "/agent-data/run-42/a", "", "UNSIGNED-PAYLOAD", now, false)
	if strings.Contains(r.Header.Get("Authorization"), "x-amz-security-token") {
		t.Fatal("fixture already signed the token")
	}
	r.Header.Set("X-Amz-Security-Token", vec.Token)
	_, err := v.Verify(r, now)
	requireCode(t, err, "SignatureDoesNotMatch")
	for _, presign := range []bool{false, true} {
		r = signedRequest(t, v, vec.Token, vec.AccessKey, "DELETE", "/agent-data/run-42/a", "", "UNSIGNED-PAYLOAD", now, presign)
		if _, err := v.Verify(r, now); err != nil {
			t.Fatalf("normal bound token presign=%v: %v", presign, err)
		}
	}
}
