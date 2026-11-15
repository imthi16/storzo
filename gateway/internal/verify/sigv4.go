package verify

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const algorithm = "AWS4-HMAC-SHA256"
const signingTimeFormat = "20060102T150405Z"

// Header signatures follow S3's 15-minute request validity window.
const headerValidity = 15 * time.Minute

var headerName = regexp.MustCompile(`^[a-z0-9-]+$`)

type authentication struct {
	validity                              time.Duration
	credential, headers, signature, token string
	date                                  time.Time
	presigned                             bool
}

func readAuth(r *http.Request) (authentication, error) {
	var a authentication
	q, err := parseQuery(r.URL.RawQuery)
	if err != nil {
		return a, signatureError()
	}
	for _, name := range []string{"Authorization", "X-Amz-Date", "X-Amz-Security-Token", "X-Amz-Content-Sha256"} {
		if len(r.Header.Values(name)) > 1 {
			return a, signatureError()
		}
	}
	auth := r.Header.Get("Authorization")
	date := r.Header.Get("X-Amz-Date")
	validity := headerValidity
	if q.Has("X-Amz-Algorithm") {
		a.presigned = true
		if auth != "" || q.Get("X-Amz-Algorithm") != algorithm || r.Header.Get("X-Amz-Security-Token") != "" {
			return a, signatureError()
		}
		a.credential = q.Get("X-Amz-Credential")
		a.headers = q.Get("X-Amz-SignedHeaders")
		a.signature = q.Get("X-Amz-Signature")
		a.token = q.Get("X-Amz-Security-Token")
		date = q.Get("X-Amz-Date")
		seconds, e := strconv.ParseInt(q.Get("X-Amz-Expires"), 10, 64)
		if e != nil || seconds < 1 || seconds > 604800 {
			return a, signatureError()
		}
		validity = time.Duration(seconds) * time.Second
	} else {
		for name := range q {
			if strings.HasPrefix(strings.ToLower(name), "x-amz-") {
				return a, signatureError()
			}
		}
		rest, ok := strings.CutPrefix(auth, algorithm+" ")
		if !ok {
			return a, signatureError()
		}
		fields := map[string]string{}
		for _, field := range strings.Split(rest, ",") {
			k, value, ok := strings.Cut(strings.TrimSpace(field), "=")
			if !ok || value == "" || fields[k] != "" {
				return a, signatureError()
			}
			fields[k] = value
		}
		if len(fields) != 3 {
			return a, signatureError()
		}
		a.credential = fields["Credential"]
		a.headers = fields["SignedHeaders"]
		a.signature = fields["Signature"]
		a.token = r.Header.Get("X-Amz-Security-Token")
	}
	a.date, err = time.Parse(signingTimeFormat, date)
	if err != nil {
		return a, signatureError()
	}
	a.validity = validity
	sig, err := hex.DecodeString(a.signature)
	if err != nil || len(sig) != sha256.Size || a.signature != strings.ToLower(a.signature) {
		return a, signatureError()
	}
	return a, nil
}

// Verify authenticates a scoped credential and SigV4 request, then enforces
// scope (except for authenticated master requests). On allow it returns claims
// and sets X-Amz-Content-Sha256 to the verified payload hash or UNSIGNED-PAYLOAD;
// on denial it returns an S3 *Error.
// A non-empty Claims alongside a non-nil error means authenticated but denied.
// A hashed payload is read and restored in memory. UNSIGNED-PAYLOAD is not read.
// The caller must preserve the unmodified path-style URL and impose its own
// transport/body limits when this core is wired to a server.
func (v *Verifier) Verify(r *http.Request, now time.Time) (Claims, error) {
	var empty Claims
	if r == nil || r.URL == nil || r.URL.Opaque != "" {
		return empty, signatureError()
	}
	a, err := readAuth(r)
	if err != nil {
		return empty, err
	}
	kid, _, _ := strings.Cut(a.credential, "/")
	master := a.token == "" && v.masterAccess != "" && kid == v.masterAccess
	c := Claims{Kid: kid}
	secret := v.masterSecret
	if !master {
		c, err = v.Token(a.token, now)
		if err != nil {
			return empty, err
		}
		secret = v.Secret(c.Kid)
	}
	// Header authentication must bind the exact token used to authorize scope.
	if !master && !a.presigned && !strings.Contains(";"+a.headers+";", ";x-amz-security-token;") {
		return empty, signatureError()
	}
	if a.date.After(now.Add(ExpirySkew)) || now.After(a.date.Add(a.validity).Add(ExpirySkew)) {
		return empty, signatureError()
	}
	scope := strings.Split(a.credential, "/")
	if len(scope) != 5 || scope[0] != c.Kid || scope[1] != a.date.Format("20060102") || scope[2] == "" || scope[3] != "s3" || scope[4] != "aws4_request" {
		return empty, signatureError()
	}
	payload := r.Header.Get("X-Amz-Content-Sha256")
	q, _ := parseQuery(r.URL.RawQuery)
	if q.Has("X-Amz-Content-Sha256") {
		if payload != "" {
			return empty, signatureError()
		}
		payload = q.Get("X-Amz-Content-Sha256")
	}
	if strings.HasPrefix(payload, "STREAMING-") || strings.Contains(strings.ToLower(r.Header.Get("Content-Encoding")), "aws-chunked") {
		return empty, unsupported()
	}
	if payload == "" && a.presigned {
		payload = "UNSIGNED-PAYLOAD"
	}
	declaredHash := payload != "" && payload != "UNSIGNED-PAYLOAD"
	if payload == "" {
		payload, err = readPayloadHash(r, payload)
		if err != nil {
			return empty, err
		}
	}
	clone := r.Clone(r.Context())
	clone.Header = make(http.Header)
	clone.ContentLength = 0
	last := ""
	host := false
	dateSigned := false
	for _, name := range strings.Split(a.headers, ";") {
		if !headerName.MatchString(name) || name <= last {
			return empty, signatureError()
		}
		last = name
		switch name {
		case "host":
			host = true
		case "content-length":
			if r.ContentLength <= 0 {
				return empty, signatureError()
			}
			clone.ContentLength = r.ContentLength
		default:
			values := r.Header.Values(name)
			if len(values) == 0 {
				return empty, signatureError()
			}
			clone.Header[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
			if name == "x-amz-date" {
				dateSigned = true
			}
		}
	}
	if !host || (!a.presigned && !dateSigned) {
		return empty, signatureError()
	}
	creds := aws.Credentials{AccessKeyID: c.Kid, SecretAccessKey: secret}
	signer := v4.NewSigner(func(o *v4.SignerOptions) {
		o.DisableURIPathEscaping = true
		o.DisableHeaderHoisting = true
		o.DisableSessionToken = true
	})
	expected := ""
	if a.presigned {
		q.Del("X-Amz-Signature")
		clone.URL.RawQuery = q.Encode()
		signed, _, e := signer.PresignHTTP(r.Context(), creds, clone, payload, "s3", scope[2], a.date)
		if e != nil {
			return empty, signatureError()
		}
		u, e := url.Parse(signed)
		if e != nil {
			return empty, signatureError()
		}
		sq := u.Query()
		if sq.Get("X-Amz-SignedHeaders") != a.headers {
			return empty, signatureError()
		}
		expected = sq.Get("X-Amz-Signature")
	} else {
		if err := signer.SignHTTP(r.Context(), creds, clone, payload, "s3", scope[2], a.date); err != nil {
			return empty, signatureError()
		}
		expectedAuth := clone.Header.Get("Authorization")
		// Comparing the complete generated Authorization also binds the declared
		// credential scope and signed-header list to the library's output.
		expected = ""
		_, expected, _ = strings.Cut(expectedAuth, ", Signature=")
		before, _, _ := strings.Cut(expectedAuth, ", Signature=")
		if before != algorithm+" Credential="+a.credential+", SignedHeaders="+a.headers {
			return empty, signatureError()
		}
	}
	if !hmac.Equal([]byte(expected), []byte(a.signature)) {
		return empty, signatureError()
	}
	// A declared hash authenticates the request before we allocate for its body.
	if declaredHash {
		payload, err = readPayloadHash(r, payload)
		if err != nil {
			return empty, err
		}
	}
	c.Master = master
	if master {
		for name := range r.Header {
			if strings.EqualFold(name, "X-Amz-Copy-Source") {
				return c, unsupported()
			}
		}
	} else if err := Scope(r, c); err != nil {
		return c, err
	}
	// Publish the verified hash for the forwarding signer, including implicit hashes.
	r.Header.Set("X-Amz-Content-Sha256", payload)
	return c, nil
}

// readPayloadHash verifies and restores a bounded hashed body. The caller applies
// its transport cap; an implicit hash necessarily requires a read before signing.
func readPayloadHash(r *http.Request, payload string) (string, error) {
	var err error
	body := []byte(nil)
	if r.Body != nil {
		body, err = io.ReadAll(r.Body)
		closeErr := r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			// The cap is public and also applies to implicit hashes, which must
			// be read before their signature can be checked.
			return "", &Error{http.StatusRequestEntityTooLarge, "EntityTooLarge", "The hashed request body exceeds the maximum allowed size."}
		}
		if err != nil || closeErr != nil {
			return "", signatureError()
		}
	}
	sum := sha256.Sum256(body)
	actual := hex.EncodeToString(sum[:])
	if payload != "" && !hmac.Equal([]byte(payload), []byte(actual)) {
		return "", signatureError()
	}
	return actual, nil
}
