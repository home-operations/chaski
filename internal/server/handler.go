package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/home-operations/chaski/internal/relay"
)

// handler builds the public webhook mux: POST /notify/{route} (+ ?dryRun=1),
// everything else 404. Middleware adds metrics, security headers, and panic
// recovery.
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	// Registered method-agnostic (not "POST /notify/{route}") so a non-POST gets
	// an explicit 405 rather than being swallowed by the "/" catch-all's 404.
	mux.HandleFunc("/notify/{route}", s.handleNotify)
	mux.HandleFunc("/", handleNotFound)

	var h http.Handler = mux
	h = s.observe(h)
	h = securityHeaders(h)
	h = recoverer(s.log)(h)
	return h
}

func handleNotFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "no such route")
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleNotify runs the inbound pipeline: token auth → route lookup → body cap →
// signature verify → decode → relay, then maps the result to a status.
func (s *Server) handleNotify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.tokenOK(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	route, ok := s.engine.Lookup(r.PathValue("route"))
	if !ok {
		handleNotFound(w, r)
		return
	}

	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "request body too large or unreadable")
		return
	}

	// Signature verification runs over the raw bytes, before any decode.
	if !route.Verify(r.Header, raw) {
		writeError(w, http.StatusUnauthorized, "signature verification failed")
		return
	}

	payload, contentType, err := decodePayload(r.Header.Get("Content-Type"), raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
	defer cancel()

	res := route.Handle(ctx, relay.Input{
		Payload:     payload,
		RawBody:     raw,
		ContentType: contentType,
		Headers:     lowerHeaders(r.Header),
		Query:       firstQuery(r.URL.Query()),
		Method:      r.Method,
		Now:         time.Now(),
		DryRun:      r.URL.Query().Get("dryRun") == "1",
	})
	s.observeRelay(r.PathValue("route"), res)

	// A debug breadcrumb on every response: the outcome, with the skip sub-reason
	// (gate vs no_targets) that the bare status code can't convey.
	w.Header().Set("X-Chaski-Result", resultLabel(res))

	switch {
	case res.Plan != nil:
		writeJSON(w, res.Status, res.Plan)
	case (res.Kind == relay.GateError || res.Kind == relay.RenderError) && res.Err != nil:
		// Operator-fault: surface the cause. These carry no payload body or target
		// credentials (unlike a relay/502 error, which stays generic below).
		writeError(w, res.Status, res.Err.Error())
	case res.Status >= http.StatusInternalServerError:
		writeError(w, res.Status, http.StatusText(res.Status))
	default:
		w.WriteHeader(res.Status)
	}
}

// resultLabel renders a relay outcome for the X-Chaski-Result header, appending
// the sub-reason for low-information kinds (e.g. "skipped:gate").
func resultLabel(res relay.Result) string {
	if res.Reason != "" {
		return res.Kind.String() + ":" + res.Reason
	}
	return res.Kind.String()
}

// tokenOK constant-time compares the inbound token against CHASKI_WEBHOOK_TOKEN.
// When no token is configured, auth is disabled (warned at startup) and per-route
// verify is the only gate.
func (s *Server) tokenOK(r *http.Request) bool {
	want := s.cfg.WebhookToken
	if want == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(inboundToken(r)), []byte(want)) == 1
}

func inboundToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return r.URL.Query().Get("token")
}

// decodePayload decodes the body into payload by content-type: JSON (the default
// when the header is absent) or form-urlencoded. Any other type → error (→ 400).
func decodePayload(contentType string, raw []byte) (any, string, error) {
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
		return queryToMap(vals), contentType, nil
	default:
		return nil, contentType, fmt.Errorf("unsupported content-type %q", media)
	}
}

func lowerHeaders(h http.Header) map[string]string {
	m := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			m[strings.ToLower(k)] = v[0]
		}
	}
	return m
}

func firstQuery(q url.Values) map[string]string {
	m := make(map[string]string, len(q))
	for k, v := range q {
		if len(v) > 0 {
			m[k] = v[0]
		}
	}
	return m
}

// queryToMap turns url.Values into payload: a single value stays a string, a
// repeated key becomes a []any.
func queryToMap(vals url.Values) map[string]any {
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

// writeError writes a JSON {"error": msg} body with the given status.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeJSON writes v as a JSON response with the given status. HTML escaping is
// on so any rendered string is inert if a response is viewed in a browser.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}
