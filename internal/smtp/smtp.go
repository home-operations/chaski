// Package smtp is chaski's optional SMTP front-door: it accepts mail and relays
// it through the same compiled route engine the webhook listener uses, selecting
// the route by the recipient localpart (sonarr@... → the route named "sonarr").
// It is off unless CHASKI_SMTP_ENABLED is set, never an open relay (an unknown
// recipient is rejected at RCPT), and gates senders with optional SMTP AUTH.
package smtp

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"
	"github.com/home-operations/chaski/internal/config"
	"github.com/home-operations/chaski/internal/relay"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Connection timeouts bounding slow-client and idle resource exhaustion.
const (
	readTimeout  = 30 * time.Second
	writeTimeout = 30 * time.Second
)

// smtpRejected counts SMTP commands refused before a message ever reaches a
// route, by reason: "auth" (bad credentials) or "recipient" (no such route, the
// open-relay guard). Relay outcomes themselves are recorded by the shared
// chaski_relays_total counter via the injected Observer.
var smtpRejected = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "chaski_smtp_rejected_total",
	Help: "SMTP commands rejected before relay, by reason (auth or recipient).",
}, []string{"reason"})

// Observer records a relay outcome. The server package passes its own so SMTP
// relays land in the same metrics and logs as webhook relays.
type Observer func(route string, res relay.Result)

// Server wraps the SMTP listener that relays received mail through the engine.
type Server struct {
	inner  *gosmtp.Server
	log    *slog.Logger
	cancel context.CancelFunc
}

// New builds the SMTP server from the resolved ops config and the compiled
// engine. AUTH is offered without TLS (v1 is meant for a trusted cluster
// network); credentials therefore travel in the clear, so keep the listener
// internal.
func New(cfg *config.Config, engine *relay.Engine, log *slog.Logger, observe Observer) *Server {
	// baseCtx parents every in-flight relay so Shutdown can cancel work that is
	// mid-flight rather than letting it run detached past the drain window.
	baseCtx, cancel := context.WithCancel(context.Background())
	be := &backend{
		engine:       engine,
		log:          log,
		observe:      observe,
		baseCtx:      baseCtx,
		users:        cfg.SMTPUsers,
		authRequired: len(cfg.SMTPUsers) > 0,
		timeout:      cfg.RequestTimeout,
	}
	inner := gosmtp.NewServer(be)
	inner.Addr = fmt.Sprintf(":%d", cfg.SMTPPort)
	inner.Domain = cfg.SMTPHostname
	inner.MaxMessageBytes = cfg.SMTPMaxMessageBytes
	inner.MaxRecipients = cfg.SMTPMaxRecipients
	inner.ReadTimeout = readTimeout
	inner.WriteTimeout = writeTimeout
	inner.AllowInsecureAuth = true
	return &Server{inner: inner, log: log, cancel: cancel}
}

// ListenAndServe runs the SMTP listener until Shutdown is called. Unlike
// net/http, go-smtp's ListenAndServe returns nil (not a sentinel) on a graceful
// Shutdown, so any non-nil error here is a real failure.
func (s *Server) ListenAndServe() error {
	s.log.Info("listening", "server", "smtp", "addr", s.inner.Addr)
	if err := s.inner.ListenAndServe(); err != nil {
		return fmt.Errorf("smtp server: %w", err)
	}
	return nil
}

// Shutdown cancels in-flight relays and drains the listener (nil-safe, like the
// HTTP drain).
func (s *Server) Shutdown(ctx context.Context) {
	if s == nil {
		return
	}
	s.cancel()
	_ = s.inner.Shutdown(ctx)
}

// backend mints one session per connection.
type backend struct {
	engine       *relay.Engine
	log          *slog.Logger
	observe      Observer
	baseCtx      context.Context
	users        map[string]string
	authRequired bool
	timeout      time.Duration
}

func (b *backend) NewSession(_ *gosmtp.Conn) (gosmtp.Session, error) {
	// With no credentials configured the listener is unauthenticated, so the
	// session starts already "authed" and AUTH is not advertised.
	return &session{be: b, authed: !b.authRequired}, nil
}

var (
	errAuthRequired = &gosmtp.SMTPError{Code: 530, EnhancedCode: gosmtp.EnhancedCode{5, 7, 0}, Message: "authentication required"}
	errBadCreds     = &gosmtp.SMTPError{Code: 535, EnhancedCode: gosmtp.EnhancedCode{5, 7, 8}, Message: "invalid credentials"}
)

// session is one SMTP conversation.
type session struct {
	be     *backend
	authed bool
	rcpts  []recipient
}

// recipient is a RCPT that resolved to a known route.
type recipient struct {
	name  string
	addr  string
	route *relay.Route
}

// AuthMechanisms advertises AUTH only when credentials are configured.
func (s *session) AuthMechanisms() []string {
	if !s.be.authRequired {
		return nil
	}
	return []string{sasl.Plain, sasl.Login}
}

// Auth returns the SASL server for a requested mechanism.
func (s *session) Auth(mech string) (sasl.Server, error) {
	switch mech {
	case sasl.Plain:
		return sasl.NewPlainServer(func(_, username, password string) error {
			return s.authenticate(username, password)
		}), nil
	case sasl.Login:
		return &loginServer{auth: s.authenticate}, nil
	default:
		return nil, &gosmtp.SMTPError{Code: 504, EnhancedCode: gosmtp.EnhancedCode{5, 5, 4}, Message: "unsupported auth mechanism"}
	}
}

// authenticate compares the password in constant time. On this listener AUTH is
// offered without TLS, so the credentials (and any timing channel) are already
// in the clear; the constant-time compare is defence-in-depth for when the
// listener is fronted by TLS, not a guarantee on the plaintext path.
func (s *session) authenticate(username, password string) error {
	want, ok := s.be.users[username]
	match := subtle.ConstantTimeCompare([]byte(password), []byte(want)) == 1
	if !ok || !match {
		smtpRejected.WithLabelValues("auth").Inc()
		return errBadCreds
	}
	s.authed = true
	return nil
}

// ensureAuthed gates MAIL/RCPT when credentials are configured.
func (s *session) ensureAuthed() error {
	if s.be.authRequired && !s.authed {
		return errAuthRequired
	}
	return nil
}

func (s *session) Mail(_ string, _ *gosmtp.MailOptions) error {
	return s.ensureAuthed()
}

// Rcpt resolves the recipient localpart to a route, rejecting anything unknown
// so the server is never an open relay. Repeat RCPTs to the same route are
// collapsed so one message cannot fan out to the same target many times.
func (s *session) Rcpt(to string, _ *gosmtp.RcptOptions) error {
	if err := s.ensureAuthed(); err != nil {
		return err
	}
	name := localpart(to)
	route, ok := s.be.engine.Lookup(name)
	if !ok {
		smtpRejected.WithLabelValues("recipient").Inc()
		return &gosmtp.SMTPError{Code: 550, EnhancedCode: gosmtp.EnhancedCode{5, 1, 1}, Message: fmt.Sprintf("unknown recipient: no route %q", name)}
	}
	for _, rc := range s.rcpts {
		if rc.name == name {
			return nil // already routed; accept without duplicating the fan-out
		}
	}
	s.rcpts = append(s.rcpts, recipient{name: name, addr: to, route: route})
	return nil
}

// Data parses the message once and relays it to every resolved recipient's
// route. Every failure (downstream or operator-fault gate/render) is treated as
// retryable: a relay should requeue until config is fixed rather than bounce a
// real notification. But the sender is asked to retry (451) only when *nothing*
// was delivered — once any recipient succeeded, a whole-message retry would
// duplicate it, so we accept and rely on the per-relay error log instead.
func (s *session) Data(r io.Reader) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	msg := parseEmail(raw, s.be.log)

	var delivered, failed bool
	for _, rc := range s.rcpts {
		ctx, cancel := context.WithTimeout(s.be.baseCtx, s.be.timeout)
		res := rc.route.Handle(ctx, relay.Input{
			Payload:     msg.payload(rc.addr),
			RawBody:     raw,
			ContentType: "message/rfc822",
			Headers:     msg.headers,
			Method:      "SMTP",
			Now:         time.Now(),
		})
		cancel()
		if s.be.observe != nil {
			s.be.observe(rc.name, res)
		}
		switch res.Kind {
		case relay.Relayed:
			delivered = true
		case relay.RelayError, relay.GateError, relay.RenderError:
			failed = true
		}
	}

	if failed && !delivered {
		return &gosmtp.SMTPError{Code: 451, EnhancedCode: gosmtp.EnhancedCode{4, 3, 0}, Message: "relay failed, try again later"}
	}
	return nil
}

func (s *session) Reset()        { s.rcpts = nil }
func (s *session) Logout() error { return nil }

// loginServer implements the SASL LOGIN mechanism server side (go-sasl ships
// only the client). LOGIN is obsolete but still common in appliance mailers.
type loginServer struct {
	auth     func(username, password string) error
	username string
	haveUser bool
}

func (l *loginServer) Next(response []byte) (challenge []byte, done bool, err error) {
	if !l.haveUser {
		if len(response) == 0 {
			return []byte("Username:"), false, nil
		}
		l.username = string(response)
		l.haveUser = true
		return []byte("Password:"), false, nil
	}
	return nil, true, l.auth(l.username, string(response))
}

// localpart extracts the route name from a recipient address: the text before
// the first '@', without surrounding angle brackets. The match against route
// names is exact (no case folding), mirroring the webhook path's exact route
// lookup.
func localpart(addr string) string {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimPrefix(addr, "<")
	addr = strings.TrimSuffix(addr, ">")
	if i := strings.IndexByte(addr, '@'); i >= 0 {
		addr = addr[:i]
	}
	return strings.TrimSpace(addr)
}
