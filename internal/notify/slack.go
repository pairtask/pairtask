// Package notify tells the operator outside the chat that a decision is
// waiting (#352, Slack-only first). A Notice is one pointer: who asks, a
// one-line summary and the chat message to answer; delivery never counts as
// a decision. The only transport here is a Slack incoming webhook: one
// secret URL, one fixed channel, one JSON POST, no edits afterwards.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Limits.
const (
	SummaryMax  = 200 // characters of summary carried
	WebhookHost = "hooks.slack.com"
	responseMax = 4 << 10
)

// Notice is what the operator is told.
type Notice struct {
	Workspace string // the pairing name, as the operator knows this workspace
	Agent     string
	Summary   string
	Anchor    string // the chat message to answer
	Hidden    bool   // say only that a decision is waiting in the workspace: no agent, summary or anchor
}

// Text is the message posted, one line.
func (n Notice) Text() string {
	where := n.Workspace
	if where == "" {
		where = "workspace"
	}
	if n.Hidden {
		return fmt.Sprintf("pairtask · %s: a decision is waiting in pairtask chat. Delivered is not approved.", where)
	}
	return fmt.Sprintf("pairtask · %s: %s needs a decision — %s. Answer in pairtask chat (reply to %s). Delivered is not approved.",
		where, n.Agent, Summary(n.Summary), n.Anchor)
}

// Summary is the summary as carried: one line of printable text, cut at
// SummaryMax characters.
func Summary(text string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(text) {
		if r == '\n' || r == '\t' {
			r = ' '
		}
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	out := []rune(b.String())
	if len(out) > SummaryMax {
		out = out[:SummaryMax]
	}
	return string(out)
}

// Failure classes a delivery that did not happen, never with the provider's text.
const (
	FailTimeout  = "timeout"
	FailRedirect = "redirect"
	FailNetwork  = "network"
	FailHTTP     = "http"
)

// Failure is a post that was not accepted.
type Failure struct {
	Class string
}

func (f *Failure) Error() string { return "notify: " + f.Class }

// Retryable: a later attempt may succeed.
func (f *Failure) Retryable() bool {
	return f.Class == FailTimeout || f.Class == FailNetwork || f.Class == FailHTTP+"_429" || f.Class == FailHTTP+"_5xx"
}

// SlackWebhook posts to one incoming webhook URL. Transport nil is the
// default; the broker passes its outbound transport, tests a fake.
type SlackWebhook struct {
	url       string
	Transport http.RoundTripper
	Timeout   time.Duration
}

// NewSlackWebhook checks the URL: https, hooks.slack.com, a services path.
func NewSlackWebhook(raw string, transport http.RoundTripper, timeout time.Duration) (*SlackWebhook, error) {
	if err := CheckWebhookURL(raw); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &SlackWebhook{url: raw, Transport: transport, Timeout: timeout}, nil
}

// webhookShape is the one URL shape accepted, the same the host and
// container commands check before storing a secret: https, the webhook
// host, a services path of plain segments, nothing else.
var webhookShape = regexp.MustCompile(`^https://hooks\.slack\.com/services/[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+)*$`)

// CheckWebhookURL accepts only a Slack incoming webhook URL.
func CheckWebhookURL(raw string) error {
	if !webhookShape.MatchString(strings.TrimSpace(raw)) {
		return errors.New("notify: the secret must be a Slack incoming webhook URL (https://hooks.slack.com/services/…)")
	}
	return nil
}

// Post sends the notice. A webhook returns no message id: the receipt is
// "sent". A 3xx is never followed.
func (s *SlackWebhook) Post(ctx context.Context, notice Notice) (string, error) {
	body, err := json.Marshal(map[string]string{"text": notice.Text()})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: s.Timeout, Transport: s.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return "", &Failure{FailTimeout}
		}
		return "", &Failure{FailNetwork}
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, responseMax)) //nolint:errcheck // the body is not read
	return receipt(resp.StatusCode)
}

// receipt turns the status into the delivery outcome.
func receipt(code int) (string, error) {
	switch {
	case code >= 200 && code < 300:
		return "sent", nil
	case code >= 300 && code < 400:
		return "", &Failure{FailRedirect}
	case code == http.StatusTooManyRequests:
		return "", &Failure{FailHTTP + "_429"}
	case code >= 500:
		return "", &Failure{FailHTTP + "_5xx"}
	}
	return "", &Failure{FailHTTP + "_4xx"}
}
