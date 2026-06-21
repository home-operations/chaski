package relay

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// DecodeBody decodes an inbound request body into a payload value by
// content-type: JSON (the default when the type is absent) or form-urlencoded.
// Any other type is an error. It returns the decoded payload and the unchanged
// content type. The webhook handler and `chaski validate --payload` share it so
// both interpret a body identically.
func DecodeBody(contentType string, raw []byte) (any, string, error) {
	media := contentType
	if i := strings.IndexByte(media, ';'); i >= 0 {
		media = media[:i]
	}
	switch strings.TrimSpace(media) {
	case "", "application/json":
		if len(strings.TrimSpace(string(raw))) == 0 {
			return map[string]any{}, contentType, nil
		}
		var p any
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, contentType, fmt.Errorf("invalid JSON body: %w", err)
		}
		return p, contentType, nil
	case "application/x-www-form-urlencoded":
		vals, err := url.ParseQuery(string(raw))
		if err != nil {
			return nil, contentType, fmt.Errorf("invalid form body: %w", err)
		}
		return formToMap(vals), contentType, nil
	default:
		return nil, contentType, fmt.Errorf("unsupported content-type %q", media)
	}
}

// formToMap turns url.Values into a payload: a single value stays a string, a
// repeated key becomes a []any.
func formToMap(vals url.Values) map[string]any {
	m := make(map[string]any, len(vals))
	for k, v := range vals {
		if len(v) == 1 {
			m[k] = v[0]
			continue
		}
		s := make([]any, len(v))
		for i, e := range v {
			s[i] = e
		}
		m[k] = s
	}
	return m
}
