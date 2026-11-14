package verify

import (
	"net/http"
	"strings"
	"unicode/utf8"
)

// SafeKey matches backend/app/security.py on decoded UTF-8 object keys.
// URL decoding belongs to net/url; never clean paths or decode them twice.
func SafeKey(key string) error {
	if key == "" || !utf8.ValidString(key) || len(key) > 1024 || strings.HasPrefix(key, "/") || strings.ContainsAny(key, "\\\x00") {
		return badKey()
	}
	for _, part := range strings.Split(key, "/") {
		if part == ".." {
			return badKey()
		}
	}
	return nil
}

// Scope accepts only the explicit v1 operation set. The request URL must be
// the original path-style URL, without a cleaning redirect or path rewrite.
func Scope(r *http.Request, c Claims) error {
	for name := range r.Header {
		if strings.EqualFold(name, "X-Amz-Copy-Source") {
			return unsupported()
		}
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(path, "/")
	if bucket != c.Bucket {
		return denied("Bucket is outside credential scope.")
	}
	q, err := parseQuery(r.URL.RawQuery)
	if err != nil {
		return denied("Invalid query.")
	}
	if key == "" {
		if r.Method == http.MethodGet && q.Get("list-type") == "2" {
			for name := range q {
				switch name {
				case "list-type", "prefix", "delimiter", "max-keys", "continuation-token", "start-after", "encoding-type", "fetch-owner", "x-id", "X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Date", "X-Amz-Expires", "X-Amz-SignedHeaders", "X-Amz-Signature", "X-Amz-Security-Token":
				default:
					return denied("Unsupported bucket operation.")
				}
			}
			p := q.Get("prefix")
			if SafeKey(p) != nil {
				if p == "" {
					return denied("List prefix is required.")
				}
				return badKey()
			}
			if strings.HasPrefix(p, c.Prefix) {
				return nil
			}
		}
		return denied("Bucket operation or list prefix is outside scope.")
	}
	if err := SafeKey(key); err != nil {
		return err
	}
	if !strings.HasPrefix(key, c.Prefix) {
		return denied("Key is outside credential scope.")
	}
	// Object subresources such as ACL/policy/versioning never become writes.
	for name := range q {
		switch name {
		case "uploadId", "partNumber", "uploads", "x-id", "response-cache-control", "response-content-disposition", "response-content-encoding", "response-content-language", "response-content-type", "response-expires", "X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Date", "X-Amz-Expires", "X-Amz-SignedHeaders", "X-Amz-Signature", "X-Amz-Security-Token":
		default:
			return denied("Unsupported object operation.")
		}
	}
	upload := q.Has("uploadId")
	if upload && q.Get("uploadId") == "" {
		return denied("Missing multipart upload ID.")
	}
	_, create := q["uploads"]
	_, part := q["partNumber"]
	if create {
		if len(q["uploads"]) != 1 || q.Get("uploads") != "" || upload || part || r.Method != "POST" {
			return denied("Invalid multipart operation.")
		}
	} else if part {
		if !upload || r.Method != "PUT" {
			return denied("Invalid multipart operation.")
		}
	} else if upload {
		if r.Method != "POST" && r.Method != "DELETE" {
			return denied("Unsupported multipart operation.")
		}
	}
	if !create && !upload && !part && (r.Method == "GET" || r.Method == "HEAD") {
		return nil
	}
	if c.Mode == "rw" && (create || upload || r.Method == "PUT" || r.Method == "DELETE") {
		return nil
	}
	return denied("Operation is outside credential scope.")
}
