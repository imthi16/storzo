package verify

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	SigningKey string `json:"signing_key"`
	Now        int64  `json:"now"`
	Vectors    []struct {
		AccessKey string `json:"access_key_id"`
		Secret    string `json:"secret_access_key"`
		Token     string `json:"session_token"`
		Claims    Claims `json:"claims"`
		Requests  []struct {
			Method  string            `json:"method"`
			URL     string            `json:"url"`
			Body    string            `json:"body"`
			Headers map[string]string `json:"headers"`
		} `json:"requests"`
	} `json:"vectors"`
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/python.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// protects=Python/Go token interoperability, derived secrets, expiry boundary and tamper rejection;
// fails_when=Python tokens fail or changed/expired tokens pass; no_claim=live request signing.
func TestPythonTokens(t *testing.T) {
	f := loadFixture(t)
	v, err := New(f.SigningKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, vec := range f.Vectors {
		c, err := v.Token(vec.Token, time.Unix(f.Now, 0))
		if err != nil {
			t.Fatal(err)
		}
		if c != vec.Claims || v.Secret(c.Kid) != vec.Secret || c.Kid != vec.AccessKey {
			t.Fatal("Python claims or derived secret mismatch")
		}
		for _, delta := range []int64{0, 60, 61} {
			_, err := v.Token(vec.Token, time.Unix(c.Exp+delta, 0))
			if delta <= 60 && err != nil {
				t.Fatal(err)
			}
			if delta == 61 && (err == nil || err.(*Error).Code != "ExpiredToken") {
				t.Fatalf("expiry: %v", err)
			}
		}
		flipped := []byte(vec.Token)
		flipped[8] ^= 1
		if _, err := v.Token(string(flipped), time.Unix(f.Now, 0)); err == nil {
			t.Fatal("flipped token accepted")
		}
		for _, token := range []string{"", vec.Token + ".extra", strings.Split(vec.Token, ".")[0], "!!.!!"} {
			if _, err := v.Token(token, time.Unix(f.Now, 0)); err == nil {
				t.Fatal("malformed token accepted")
			}
		}
	}
}

// protects=fail-closed configuration and authenticated claim validation;
// fails_when=invalid authenticated scopes are accepted; no_claim=issuer API validation.
func TestTokenInvalidClaims(t *testing.T) {
	if _, err := New("short"); err == nil {
		t.Fatal("short signing key accepted")
	}
	f := loadFixture(t)
	v, _ := New(f.SigningKey)
	for _, change := range []func(*Claims){func(c *Claims) { c.Mode = "admin" }, func(c *Claims) { c.Prefix = "../x" }, func(c *Claims) { c.Kid = "other" }, func(c *Claims) { c.Exp = c.Iat + 3601 }, func(c *Claims) { c.Exp = c.Iat }, func(c *Claims) { c.Jti = "" }, func(c *Claims) { c.Bucket = "BAD" }} {
		c := f.Vectors[0].Claims
		change(&c)
		raw, _ := json.Marshal(c)
		body := base64.RawURLEncoding.EncodeToString(raw)
		token := body + "." + base64.RawURLEncoding.EncodeToString(v.mac(body))
		if _, err := v.Token(token, time.Unix(f.Now, 0)); err == nil {
			t.Fatalf("invalid claims accepted: %+v", c)
		}
	}
}

// protects=S3 XML status and error codes; fails_when=SDK-readable error contract changes;
// no_claim=SDK retry behavior.
func TestErrorXML(t *testing.T) {
	for _, e := range []*Error{signatureError(), denied("scope"), {403, "ExpiredToken", "expired"}} {
		w := httptest.NewRecorder()
		e.Write(w)
		if w.Code != e.Status || w.Header().Get("Content-Type") != "application/xml" || !strings.Contains(w.Body.String(), "<Error><Code>"+e.Code+"</Code>") {
			t.Fatalf("bad XML response: %v %s", w.Code, w.Body.String())
		}
	}
}
