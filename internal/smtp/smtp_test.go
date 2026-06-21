package smtp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gosmtp "github.com/emersion/go-smtp"
	"github.com/home-operations/chaski/internal/config"
	"github.com/home-operations/chaski/internal/relay"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type fakeNotifier struct {
	mu     sync.Mutex
	calls  int
	bodies []string
	err    error // when set, Send fails (drives a relay error)
}

func (f *fakeNotifier) Send(_ context.Context, targetURL, body, _ string, _ map[string]string) error {
	f.mu.Lock()
	f.calls++
	f.bodies = append(f.bodies, body)
	err := f.err
	f.mu.Unlock()
	if strings.Contains(targetURL, "fail") { // the "broken" route's target always errors
		return context.DeadlineExceeded
	}
	return err
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

const routeYAML = `
targets:
  po: { apprise: { url: 'pover://u@t/' } }
  failtarget: { apprise: { url: 'pover://u@fail/' } }
routes:
  alerts:
    target: po
    whenExpr: 'payload.subject != ""'
    title: '{{ .payload.subject }}'
    message: '{{ .payload.body }}'
  broken:
    target: failtarget
    whenExpr: 'payload.subject != ""'
    message: '{{ .payload.body }}'
`

func engineWithAlerts(t *testing.T, n *fakeNotifier) *relay.Engine {
	t.Helper()
	file := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(file, []byte(routeYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, err := config.LoadRouteConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	e, err := relay.Build(rc, &config.Config{RetryAttempts: 1, RetryBackoff: time.Millisecond}, relay.Options{Notifier: n})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func testBackend(t *testing.T, n *fakeNotifier) *backend {
	t.Helper()
	return &backend{
		engine:  engineWithAlerts(t, n),
		log:     discardLog(),
		baseCtx: context.Background(),
		timeout: 5 * time.Second,
	}
}

func TestLocalpart(t *testing.T) {
	for in, want := range map[string]string{
		"alerts@chaski":         "alerts",
		"<alerts@chaski.local>": "alerts",
		"sonarr-download@x.y.z": "sonarr-download",
		"  spaced@host  ":       "spaced",
		"noatsign":              "noatsign",
	} {
		if got := localpart(in); got != want {
			t.Errorf("localpart(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseEmailPlain(t *testing.T) {
	raw := "From: sensor@device.local\r\n" +
		"To: alerts@chaski\r\n" +
		"Subject: Disk full\r\n\r\n" +
		"the disk is full"
	m := parseEmail([]byte(raw), discardLog())
	if m.subject != "Disk full" {
		t.Errorf("subject = %q, want %q", m.subject, "Disk full")
	}
	if m.from != "sensor@device.local" {
		t.Errorf("from = %q, want sensor@device.local", m.from)
	}
	if !strings.Contains(m.text, "the disk is full") {
		t.Errorf("text = %q, want it to contain the body", m.text)
	}
	if body := m.payload("alerts@chaski")["body"]; !strings.Contains(body.(string), "the disk is full") {
		t.Errorf("payload body = %q, want the text body", body)
	}
}

func TestParseEmailMultipartPrefersText(t *testing.T) {
	raw := "Subject: Multi\r\n" +
		"Content-Type: multipart/alternative; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nplain body\r\n" +
		"--b\r\nContent-Type: text/html\r\n\r\n<p>html body</p>\r\n" +
		"--b--\r\n"
	m := parseEmail([]byte(raw), discardLog())
	if !strings.Contains(m.text, "plain body") {
		t.Errorf("text = %q, want plain body", m.text)
	}
	if !strings.Contains(m.html, "<p>html body</p>") {
		t.Errorf("html = %q, want html body", m.html)
	}
	if body := m.payload("x")["body"].(string); !strings.Contains(body, "plain body") {
		t.Errorf("payload body = %q, want text preferred over html", body)
	}
}

func TestParseEmailDecodesSubjectAndSkipsAttachment(t *testing.T) {
	raw := "Subject: =?utf-8?q?Caf=C3=A9?=\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nhi\r\n" +
		"--b\r\nContent-Type: application/octet-stream\r\n" +
		"Content-Disposition: attachment; filename=\"x.bin\"\r\n\r\nBINARYDATA\r\n" +
		"--b--\r\n"
	m := parseEmail([]byte(raw), discardLog())
	if m.subject != "Café" {
		t.Errorf("subject = %q, want Café (decoded)", m.subject)
	}
	if !strings.Contains(m.text, "hi") {
		t.Errorf("text = %q, want hi", m.text)
	}
	if strings.Contains(m.text, "BINARYDATA") || strings.Contains(m.html, "BINARYDATA") {
		t.Errorf("attachment leaked into body: text=%q html=%q", m.text, m.html)
	}
}

func TestParseEmailNonMIMEFallback(t *testing.T) {
	// A blob with no header/body separator fails MIME parsing, exercising the
	// net/mail parsePlain fallback (whole body becomes text).
	m := parseEmail([]byte("not an email, no headers at all"), discardLog())
	if !strings.Contains(m.text, "not an email") {
		t.Errorf("text = %q, want the raw blob via the parsePlain fallback", m.text)
	}
	if m.from != "" || m.subject != "" {
		t.Errorf("from=%q subject=%q, want empty for a headerless blob", m.from, m.subject)
	}
}

func TestSessionRcptResolvesAndRejects(t *testing.T) {
	s := &session{be: testBackend(t, &fakeNotifier{}), authed: true}
	if err := s.Rcpt("alerts@chaski", nil); err != nil {
		t.Fatalf("Rcpt(known route) = %v, want nil", err)
	}
	if len(s.rcpts) != 1 || s.rcpts[0].name != "alerts" {
		t.Fatalf("rcpts = %+v, want one resolved to alerts", s.rcpts)
	}
	if err := s.Rcpt("nope@chaski", nil); err == nil {
		t.Error("Rcpt(unknown route) = nil, want rejection (no open relay)")
	}
}

func TestSessionDataRelays(t *testing.T) {
	n := &fakeNotifier{}
	s := &session{be: testBackend(t, n), authed: true}
	if err := s.Rcpt("alerts@chaski", nil); err != nil {
		t.Fatal(err)
	}
	raw := "To: alerts@chaski\r\nSubject: Ping\r\n\r\npong"
	if err := s.Data(strings.NewReader(raw)); err != nil {
		t.Fatalf("Data = %v, want nil (relayed)", err)
	}
	if n.calls != 1 {
		t.Fatalf("notifier calls = %d, want 1", n.calls)
	}
	if !strings.Contains(n.bodies[0], "pong") {
		t.Errorf("relayed body = %q, want it to contain pong", n.bodies[0])
	}
}

func TestSessionDedupesRepeatRcpts(t *testing.T) {
	n := &fakeNotifier{}
	s := &session{be: testBackend(t, n), authed: true}
	for range 3 {
		if err := s.Rcpt("alerts@chaski", nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.rcpts) != 1 {
		t.Fatalf("rcpts = %d, want 1 (repeat RCPTs to the same route deduped)", len(s.rcpts))
	}
	if err := s.Data(strings.NewReader("Subject: x\r\n\r\nbody")); err != nil {
		t.Fatal(err)
	}
	if n.calls != 1 {
		t.Errorf("notifier calls = %d, want 1 (no fan-out amplification)", n.calls)
	}
}

func TestSessionDataMixedOutcomeAccepts(t *testing.T) {
	// alerts relays OK, broken's downstream always fails. Because one recipient
	// was delivered, the whole message is accepted (250/nil) so a retry can't
	// duplicate the delivered one. This pins `failed && !delivered`.
	n := &fakeNotifier{}
	s := &session{be: testBackend(t, n), authed: true}
	if err := s.Rcpt("alerts@chaski", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Rcpt("broken@chaski", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Data(strings.NewReader("Subject: x\r\n\r\nbody")); err != nil {
		t.Fatalf("Data = %v, want nil (250) when one recipient was delivered", err)
	}
}

func TestSessionDataTransientOnRelayFailure(t *testing.T) {
	n := &fakeNotifier{err: context.DeadlineExceeded}
	s := &session{be: testBackend(t, n), authed: true}
	if err := s.Rcpt("alerts@chaski", nil); err != nil {
		t.Fatal(err)
	}
	err := s.Data(strings.NewReader("Subject: x\r\n\r\nbody"))
	var smtpErr *gosmtp.SMTPError
	if !errors.As(err, &smtpErr) || smtpErr.Code != 451 {
		t.Fatalf("Data = %v, want a 451 transient error so the sender retries", err)
	}
}

func TestSessionUnauthenticatedWhenNoCreds(t *testing.T) {
	s := &session{be: testBackend(t, &fakeNotifier{}), authed: true} // authRequired=false
	if got := s.AuthMechanisms(); got != nil {
		t.Errorf("AuthMechanisms = %v, want nil (AUTH not advertised without creds)", got)
	}
	if err := s.Mail("x@y", nil); err != nil {
		t.Errorf("Mail without auth = %v, want nil", err)
	}
	if err := s.Rcpt("alerts@chaski", nil); err != nil {
		t.Errorf("Rcpt without auth = %v, want nil", err)
	}
}

func TestRejectionMetrics(t *testing.T) {
	// Unknown recipient increments reason="recipient".
	before := testutil.ToFloat64(smtpRejected.WithLabelValues("recipient"))
	s := &session{be: testBackend(t, &fakeNotifier{}), authed: true}
	_ = s.Rcpt("nope@chaski", nil)
	if got := testutil.ToFloat64(smtpRejected.WithLabelValues("recipient")) - before; got != 1 {
		t.Errorf("recipient rejections delta = %v, want 1", got)
	}

	// Bad credentials increment reason="auth".
	beforeAuth := testutil.ToFloat64(smtpRejected.WithLabelValues("auth"))
	as := &session{be: &backend{users: map[string]string{"u": "p"}, authRequired: true}}
	_ = as.authenticate("u", "wrong")
	if got := testutil.ToFloat64(smtpRejected.WithLabelValues("auth")) - beforeAuth; got != 1 {
		t.Errorf("auth rejections delta = %v, want 1", got)
	}
}

func TestSessionAuthGate(t *testing.T) {
	be := &backend{users: map[string]string{"alice": "secret"}, authRequired: true}
	s := &session{be: be, authed: false}

	if got := s.AuthMechanisms(); len(got) != 2 {
		t.Errorf("AuthMechanisms = %v, want PLAIN and LOGIN", got)
	}
	if err := s.Mail("x@y", nil); err == nil {
		t.Error("Mail before auth = nil, want authentication required")
	}
	if err := s.authenticate("alice", "wrong"); err == nil {
		t.Error("authenticate(bad password) = nil, want error")
	}
	if s.authed {
		t.Error("authed = true after a failed login")
	}
	if err := s.authenticate("alice", "secret"); err != nil {
		t.Errorf("authenticate(good) = %v, want nil", err)
	}
	if !s.authed {
		t.Error("authed = false after a successful login")
	}
	if err := s.Mail("x@y", nil); err != nil {
		t.Errorf("Mail after auth = %v, want nil", err)
	}
}

func TestLoginServerExchange(t *testing.T) {
	var gotUser, gotPass string
	l := &loginServer{auth: func(u, p string) error { gotUser, gotPass = u, p; return nil }}

	ch, done, err := l.Next(nil)
	if string(ch) != "Username:" || done || err != nil {
		t.Fatalf("step 1 = %q,%v,%v; want Username:,false,nil", ch, done, err)
	}
	ch, done, err = l.Next([]byte("alice"))
	if string(ch) != "Password:" || done || err != nil {
		t.Fatalf("step 2 = %q,%v,%v; want Password:,false,nil", ch, done, err)
	}
	_, done, err = l.Next([]byte("secret"))
	if !done || err != nil {
		t.Fatalf("step 3 = done %v err %v; want true,nil", done, err)
	}
	if gotUser != "alice" || gotPass != "secret" {
		t.Errorf("auth got %q/%q, want alice/secret", gotUser, gotPass)
	}
}
