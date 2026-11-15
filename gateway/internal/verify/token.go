// Package verify implements the scoped agent credential verification core.
package verify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

const ExpirySkew = 60 * time.Second

var kidPattern = regexp.MustCompile(`^STZ[A-Z2-7]{16}$`)
var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

type Claims struct {
	Kid    string `json:"kid"`
	Bucket string `json:"bucket"`
	Prefix string `json:"prefix"`
	Mode   string `json:"mode"`
	Iat    int64  `json:"iat"`
	Exp    int64  `json:"exp"`
	Jti    string `json:"jti"`
}

type Verifier struct {
	key                        []byte
	masterAccess, masterSecret string
}

// NewWithMaster also accepts tokenless requests authenticated with the master key.
func NewWithMaster(signingKey, access, secret string) (*Verifier, error) {
	v, err := New(signingKey)
	if err == nil {
		v.masterAccess, v.masterSecret = access, secret
	}
	return v, err
}

func New(signingKey string) (*Verifier, error) {
	if len([]rune(signingKey)) < 32 {
		return nil, denied("Invalid signing key configuration")
	}
	return &Verifier{key: []byte(signingKey)}, nil
}

func (v *Verifier) mac(s string) []byte {
	m := hmac.New(sha256.New, v.key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

func (v *Verifier) Secret(kid string) string { return hex.EncodeToString(v.mac("secret|" + kid)) }

func (v *Verifier) Token(token string, now time.Time) (Claims, error) {
	var c Claims
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return c, signatureError()
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil || !hmac.Equal(sig, v.mac(parts[0])) {
		return c, signatureError()
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil {
		return c, signatureError()
	}
	if err = json.Unmarshal(raw, &c); err != nil {
		return Claims{}, signatureError()
	}
	if !kidPattern.MatchString(c.Kid) || !bucketPattern.MatchString(c.Bucket) || strings.Contains(c.Bucket, "..") || strings.Contains(c.Bucket, ".-") || strings.Contains(c.Bucket, "-.") || SafeKey(c.Prefix) != nil || (c.Mode != "ro" && c.Mode != "rw") || c.Jti == "" || c.Iat <= 0 || c.Exp <= c.Iat || c.Exp-c.Iat > 3600 {
		return Claims{}, signatureError()
	}
	if now.Unix() > c.Exp && now.Unix()-c.Exp > int64(ExpirySkew/time.Second) {
		return Claims{}, &Error{403, "ExpiredToken", "The provided token has expired."}
	}
	return c, nil
}
