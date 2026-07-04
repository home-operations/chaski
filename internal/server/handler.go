package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/home-operations/chaski/internal/relay"
)

// handler builds the public webhook mux: POST /hooks/{route} (+ ?dryRun=1),
// everything else 404. Middleware adds metrics, security headers, and panic
// recovery.
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	// Registered method-agnostic (not "POST /hooks/{route}") so a non-POST gets
	// an explicit 405 rather than being swallowed by the "/" catch-all's 404.
	mux.HandleFunc("/hooks/{route}", s.handleHook)
	mux.HandleFunc("/", handleNotFound)

	var h http.Handler = mux
	// observe is outermost so its deferred recording sees the final status —
	// including a 500 written by the (now inner) recoverer for a panicking handler.
	h = recoverer(s.log)(h)
	h = securityHeaders(h)
	h = s.observe(h)
	return h
}

func handleNotFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "no such route")
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleHook runs the inbound pipeline: token auth → route lookup → body cap →
// signature verify → decode → relay, then maps the result to a status.
func (s *Server) handleHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		webhookRejected.WithLabelValues("method").Inc()
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.tokenOK(r) {
		webhookRejected.WithLabelValues("unauthorized").Inc()
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	route, ok := s.engine.Lookup(r.PathValue("route"))
	if !ok {
		if s.cfg.LogUnknownRoutes {
			s.logUnknownRoute(w, r)
		}
		webhookRejected.WithLabelValues("not_found").Inc()
		handleNotFound(w, r)
		return
	}

	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes))
	if err != nil {
		webhookRejected.WithLabelValues("body").Inc()
		writeError(w, http.StatusBadRequest, "request body too large or unreadable")
		return
	}

	// Signature verification runs over the raw bytes, before any decode.
	if !route.Verify(r.Header, raw) {
		webhookRejected.WithLabelValues("signature").Inc()
		writeError(w, http.StatusUnauthorized, "signature verification failed")
		return
	}

	payload, contentType, err := relay.DecodeBody(r.Header.Get("Content-Type"), raw)
	if err != nil {
		webhookRejected.WithLabelValues("decode").Inc()
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Post-verify, pre-gate: a whenExpr miss still logs — that is when the
	// payload is needed most.
	if route.LogPayload() {
		s.log.Info("inbound payload", "route", r.PathValue("route"), "payload", payload)
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
	case res.Status >= http.StatusInternalServerError:
		// Generic to the caller. Gate/render errors wrap the operator's expression
		// or template source (env-var names, and a value if piped through e.g.
		// atoi) — that detail goes to logs (observeRelay above), never the client,
		// which a caller who can trigger a 500 must not read.
		writeError(w, res.Status, http.StatusText(res.Status))
	default:
		w.WriteHeader(res.Status)
	}
}

// logUnknownRoute logs the body POSTed to a nonexistent route (behind the
// global token when one is set; pre-verify by definition, hence opt-in).
func (s *Server) logUnknownRoute(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes))
	if err != nil || len(raw) == 0 {
		return
	}
	var body any = string(raw)
	if pl, _, err := relay.DecodeBody(r.Header.Get("Content-Type"), raw); err == nil {
		body = pl
	}
	s.log.Info("payload for unknown route", "route", r.PathValue("route"), "payload", body)
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
