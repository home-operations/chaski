package sink

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	apprise "github.com/unraid/apprise-go"
)

// appriseNotifier is the default Notifier, backed by apprise-go. apprise-go's
// Send has no context, so the deadline is enforced between retry attempts; a
// pre-cancelled context short-circuits before sending.
type appriseNotifier struct{}

// DefaultNotifier returns the apprise-go-backed Notifier.
func DefaultNotifier() Notifier { return appriseNotifier{} }

func (appriseNotifier) Send(ctx context.Context, targetURL, body, title string, params map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	full, err := mergeQuery(targetURL, params)
	if err != nil {
		return err
	}
	a := apprise.New()
	if err := a.Add(full); err != nil {
		// A bad URL/scheme won't fix itself on retry.
		return Permanent(fmt.Errorf("apprise: invalid target url: %w", err))
	}
	var opts []apprise.Option
	if title != "" {
		opts = append(opts, apprise.WithTitle(title))
	}
	// A route that sets format declares its body IS that format. Without this,
	// apprise-go's default input format (text) HTML-escapes the body when
	// converting for an html/markdown target, so tags render literally.
	if f := params["format"]; f != "" {
		opts = append(opts, apprise.WithInputFormat(f))
	}
	if err := a.Send(body, opts...); err != nil {
		// apprise-go embeds the full, credential-bearing target URL in its error
		// text. Strip the known URL strings so it can't reach logs. Best-effort:
		// a fully robust fix needs apprise-go to stop echoing the URL.
		return fmt.Errorf("apprise: send failed: %s", redactURLs(err.Error(), full, targetURL))
	}
	return nil
}

// redactURLs replaces each non-empty url in text with a placeholder — used to
// strip credential-bearing target URLs that apprise-go echoes in its errors.
func redactURLs(text string, urls ...string) string {
	for _, u := range urls {
		if u != "" {
			text = strings.ReplaceAll(text, u, "<redacted-url>")
		}
	}
	return text
}

// mergeQuery URL-encodes params into rawURL's query (params override existing
// keys). Provider auth/host live in rawURL and are never touched.
func mergeQuery(rawURL string, params map[string]string) (string, error) {
	if len(params) == 0 {
		return rawURL, nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", Permanent(fmt.Errorf("apprise: parse target url: %w", err))
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// appriseSink relays to an apprise target through the Notifier seam.
type appriseSink struct {
	name     string
	url      string
	notifier Notifier
	retry    RetryPolicy
}

func (s *appriseSink) Name() string { return s.name }
func (s *appriseSink) Kind() string { return kindApprise }

func (s *appriseSink) Send(ctx context.Context, msg Message) error {
	return deliver(ctx, s.name, s.Kind(), s.retry, func(ctx context.Context) error {
		return s.notifier.Send(ctx, s.url, msg.Body, msg.Title, msg.Params)
	})
}
