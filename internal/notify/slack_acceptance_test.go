package notify_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/notify"
)

const webhook = "https://hooks.slack.com/services/T000/B000/secret-marker"

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func reply(code int) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("ignored")), Header: make(http.Header)}
}

func TestSlackWebhookAcceptancePostsOneCorrelatedJSONNotice(t *testing.T) {
	var calls int
	var gotText string
	transport := transportFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodPost || req.URL.Host != notify.WebhookHost || req.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected webhook request: %s %s", req.Method, req.URL.Host)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		gotText = payload.Text
		return reply(http.StatusOK), nil
	})
	client, err := notify.NewSlackWebhook(webhook, transport, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := client.Post(context.Background(), notify.Notice{
		Workspace: "desk", Agent: "codex-1", Summary: "Choose the release", Anchor: "ask-123",
	})
	if err != nil || receipt != "sent" || calls != 1 {
		t.Fatalf("post = (%q, %v), calls = %d", receipt, err, calls)
	}
	for _, part := range []string{"desk", "codex-1", "Choose the release", "ask-123", "Delivered is not approved"} {
		if !strings.Contains(gotText, part) {
			t.Fatalf("Slack notice omitted %q: %q", part, gotText)
		}
	}
	if strings.Contains(gotText, "secret-marker") {
		t.Fatal("webhook credential leaked into the posted text")
	}
}

func TestSlackWebhookAcceptanceHiddenNoticeOmitsSummaryAndAnchor(t *testing.T) {
	var text string
	transport := transportFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		text = payload.Text
		return reply(http.StatusNoContent), nil
	})
	client, err := notify.NewSlackWebhook(webhook, transport, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Post(context.Background(), notify.Notice{
		Workspace: "desk", Agent: "codex-1", Summary: "private-summary", Anchor: "private-anchor", Hidden: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "decision is waiting") || strings.Contains(text, "private-summary") || strings.Contains(text, "private-anchor") {
		t.Fatalf("hidden Slack notice disclosed the request: %q", text)
	}
}

func TestSlackWebhookAcceptanceDeliveryErrorsAreBoundedAndSecretFree(t *testing.T) {
	for _, tc := range []struct {
		name  string
		code  int
		fault error
		class string
		retry bool
	}{
		{"redirect", http.StatusFound, nil, notify.FailRedirect, false},
		{"rate limit", http.StatusTooManyRequests, nil, notify.FailHTTP + "_429", true},
		{"server error", http.StatusBadGateway, nil, notify.FailHTTP + "_5xx", true},
		{"client error", http.StatusForbidden, nil, notify.FailHTTP + "_4xx", false},
		{"network with URL", 0, &url.Error{Op: "Post", URL: webhook, Err: errors.New("connection refused")}, notify.FailNetwork, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := transportFunc(func(*http.Request) (*http.Response, error) {
				if tc.fault != nil {
					return nil, tc.fault
				}
				return reply(tc.code), nil
			})
			client, err := notify.NewSlackWebhook(webhook, transport, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Post(context.Background(), notify.Notice{Summary: "ready"})
			var failure *notify.Failure
			if !errors.As(err, &failure) || failure.Class != tc.class || failure.Retryable() != tc.retry {
				t.Fatalf("failure = %v, want class %q and retry %t", err, tc.class, tc.retry)
			}
			if strings.Contains(err.Error(), "secret-marker") || strings.Contains(err.Error(), webhook) {
				t.Fatal("webhook secret leaked into the delivery error")
			}
		})
	}
}

func TestSlackWebhookAcceptanceRejectsOtherDestinations(t *testing.T) {
	for _, raw := range []string{
		"http://hooks.slack.com/services/T/B/C",
		"https://hooks.slack.com.evil.example/services/T/B/C",
		"https://evil.example/services/T/B/C",
		"https://hooks.slack.com/other/T/B/C",
		"https://user@hooks.slack.com/services/T/B/C",
	} {
		if err := notify.CheckWebhookURL(raw); err == nil {
			t.Errorf("accepted unsafe webhook URL %q", raw)
		}
	}
}
