package audit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"sync/atomic"
	"time"
)

// Revocations holds the latest successful snapshot of all active revoked kids.
type Revocations struct {
	url    string
	key    []byte
	kids   atomic.Pointer[map[string]struct{}]
	client http.Client
}

func NewRevocations(auditURL, key string) (*Revocations, error) {
	u, err := url.Parse(auditURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, errors.New("invalid audit URL for revocation polling")
	}
	u.Path = path.Join(path.Dir(u.Path), "revoked")
	u.RawPath = ""
	u.RawQuery = "since=0"
	return &Revocations{
		url: u.String(), key: []byte(key),
		client: http.Client{
			Timeout:       3 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (r *Revocations) Contains(kid string) bool {
	current := r.kids.Load()
	if current == nil {
		return false
	}
	_, found := (*current)[kid]
	return found
}

// Refresh replaces the cache only after receiving a complete, valid snapshot.
func (r *Revocations) Refresh(ctx context.Context) {
	if err := r.refresh(ctx); err != nil && ctx.Err() == nil {
		log.Print("gateway revocation poll failed; serving with last known revocations (freshness fail-open)")
	}
}

func (r *Revocations) Poll(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Refresh(ctx)
		}
	}
}

func (r *Revocations) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return err
	}
	signRequest(req, r.key, nil)
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusError(resp.StatusCode)
	}
	const limit = 4 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(body) > limit {
		return errors.New("could not read revocation snapshot")
	}
	var snapshot struct {
		Kids []string `json:"kids"`
		Now  int64    `json:"now"`
	}
	if err := json.Unmarshal(body, &snapshot); err != nil || snapshot.Kids == nil || snapshot.Now <= 0 {
		return errors.New("invalid revocation snapshot")
	}
	kids := make(map[string]struct{}, len(snapshot.Kids))
	for _, kid := range snapshot.Kids {
		kids[kid] = struct{}{}
	}
	r.kids.Store(&kids)
	return nil
}

func signRequest(req *http.Request, key, body []byte) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set(headerTimestamp, ts)
	req.Header.Set(headerSignature, signature(key, ts, body))
}
