package verify

import (
	"fmt"
	"net/url"
)

func parseQuery(raw string) (url.Values, error) {
	q, err := url.ParseQuery(raw)
	if err != nil {
		return nil, err
	}
	for k, v := range q {
		if len(v) != 1 {
			return nil, fmt.Errorf("duplicate query parameter %q", k)
		}
	}
	return q, nil
}
