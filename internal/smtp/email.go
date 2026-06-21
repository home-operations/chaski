package smtp

import (
	"bytes"
	"io"
	"log/slog"
	"net/mail"
	"strings"

	gomsg "github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // register non-UTF-8 charset decoders
	gomail "github.com/emersion/go-message/mail"
)

// message is a parsed inbound email, projected into relay variables.
type message struct {
	subject string
	from    string
	text    string
	html    string
	headers map[string]string
}

// payload builds the CEL/template `payload` map for one recipient. `body`
// prefers the plain-text part, falling back to HTML, so a route that does not
// care about format still gets something useful.
func (m message) payload(to string) map[string]any {
	body := m.text
	if body == "" {
		body = m.html
	}
	return map[string]any{
		"subject": m.subject,
		"from":    m.from,
		"to":      to,
		"body":    body,
		"text":    m.text,
		"html":    m.html,
	}
}

// parseEmail reads an RFC822 message into its subject, from, and text/html
// bodies. Attachments are ignored (the relay sinks cannot carry them). It never
// fails: a message that will not parse as MIME falls back to net/mail, and a
// wholly unparseable blob becomes a text body. A part whose body cannot be fully
// decoded is logged (the relayed content would otherwise be silently truncated).
func parseEmail(raw []byte, log *slog.Logger) message {
	mr, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil && !gomsg.IsUnknownCharset(err) {
		return parsePlain(raw)
	}
	defer func() { _ = mr.Close() }()

	m := message{headers: map[string]string{}}
	if subject, err := mr.Header.Subject(); err == nil {
		m.subject = subject
	}
	m.from = firstFrom(&mr.Header)
	collectHeaders(mr.Header.Fields(), m.headers)

	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil && !gomsg.IsUnknownCharset(err) {
			break
		}
		ih, ok := p.Header.(*gomail.InlineHeader)
		if !ok {
			continue // an attachment: skipped
		}
		ct, _, _ := ih.ContentType()
		b, rerr := io.ReadAll(p.Body)
		if rerr != nil {
			log.Warn("smtp: message part body truncated", "content_type", ct, "error", rerr)
		}
		if ct == "text/html" {
			if m.html == "" {
				m.html = string(b)
			}
			continue
		}
		if m.text == "" {
			m.text = string(b)
		}
	}
	return m
}

// parsePlain is the fallback for non-MIME messages: split headers from body
// with the standard library and treat the body as plain text.
func parsePlain(raw []byte) message {
	m := message{headers: map[string]string{}}
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		m.text = string(raw)
		return m
	}
	body, _ := io.ReadAll(parsed.Body)
	m.text = string(body)
	m.subject = parsed.Header.Get("Subject")
	if addrs, err := parsed.Header.AddressList("From"); err == nil && len(addrs) > 0 {
		m.from = addrs[0].Address
	}
	for k, v := range parsed.Header {
		if len(v) > 0 {
			m.headers[strings.ToLower(k)] = v[0]
		}
	}
	return m
}

// firstFrom returns the first From address (bare e-mail, no display name).
func firstFrom(h *gomail.Header) string {
	addrs, err := h.AddressList("From")
	if err != nil || len(addrs) == 0 {
		return ""
	}
	return addrs[0].Address
}

// collectHeaders copies every header field into dst (lower-cased keys, first
// value wins) so routes can read arbitrary headers via `headers`.
func collectHeaders(fields gomsg.HeaderFields, dst map[string]string) {
	for fields.Next() {
		k := strings.ToLower(fields.Key())
		if _, exists := dst[k]; exists {
			continue
		}
		text, err := fields.Text()
		if err != nil {
			text = fields.Value()
		}
		dst[k] = text
	}
}
