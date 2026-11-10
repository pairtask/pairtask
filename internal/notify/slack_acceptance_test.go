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

func webhookClient(t *testing.T, transport http.RoundTripper) *notify.SlackWebhook {
	t.Helper()
	client, err := notify.NewSlackWebhook(webhook, transport, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func noticeText(t *testing.T, req *http.Request) string {
	t.Helper()
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
	return payload.Text
}

func assertWebhookRequest(t *testing.T, req *http.Request) {
	t.Helper()
	if req.Method != http.MethodPost {
		t.Fatalf("webhook method = %s", req.Method)
	}
	if req.URL.Host != notify.WebhookHost {
		t.Fatalf("webhook host = %s", req.URL.Host)
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("webhook content type = %s", req.Header.Get("Content-Type"))
	}
}

func TestSlackWebhookAcceptancePostsOneCorrelatedJSONNotice(t *testing.T) {
	var calls int
	var gotText string
	transport := transportFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		assertWebhookRequest(t, req)
		gotText = noticeText(t, req)
		return reply(http.StatusOK), nil
	})
	client := webhookClient(t, transport)
	receipt, err := client.Post(context.Background(), notify.Notice{
		Workspace: "desk", Agent: "codex-1", Summary: "Choose the release", Anchor: "ask-123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt != "sent" {
		t.Fatalf("post receipt = %q", receipt)
	}
	if calls != 1 {
		t.Fatalf("post = (%q, %v), calls = %d", receipt, err, calls)
	}
	assertNoticeParts(t, gotText, "desk", "codex-1", "Choose the release", "ask-123", "Delivered is not approved")
	if strings.Contains(gotText, "secret-marker") {
		t.Fatal("webhook credential leaked into the posted text")
	}
}

func assertNoticeParts(t *testing.T, text string, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if !strings.Contains(text, part) {
			t.Fatalf("Slack notice omitted %q: %q", part, text)
		}
	}
}

func TestSlackWebhookAcceptanceHiddenNoticeOmitsSummaryAndAnchor(t *testing.T) {
	var text string
	transport := transportFunc(func(req *http.Request) (*http.Response, error) {
		text = noticeText(t, req)
		return reply(http.StatusNoContent), nil
	})
	client := webhookClient(t, transport)
	_, err := client.Post(context.Background(), notify.Notice{
		Workspace: "desk", Agent: "codex-1", Summary: "private-summary", Anchor: "private-anchor", Hidden: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "decision is waiting") {
		t.Fatalf("hidden Slack notice omitted the pointer: %q", text)
	}
	if strings.Contains(text, "private-summary") {
		t.Fatalf("hidden Slack notice disclosed the request: %q", text)
	}
	if strings.Contains(text, "private-anchor") {
		t.Fatalf("hidden Slack notice disclosed the request: %q", text)
	}
	if !strings.Contains(text, "desk") {
		t.Fatalf("hidden Slack notice omitted the workspace pointer: %q", text)
	}
	if strings.Contains(text, "codex-1") {
		t.Fatalf("hidden Slack notice disclosed the agent: %q", text)
	}
}

type failureCase struct {
	name  string
	code  int
	fault error
	class string
	retry bool
}

func TestSlackWebhookAcceptanceDeliveryErrorsAreBoundedAndSecretFree(t *testing.T) {
	for _, tc := range []failureCase{
		{"redirect", http.StatusFound, nil, notify.FailRedirect, false},
		{"rate limit", http.StatusTooManyRequests, nil, notify.FailHTTP + "_429", true},
		{"server error", http.StatusBadGateway, nil, notify.FailHTTP + "_5xx", true},
		{"client error", http.StatusForbidden, nil, notify.FailHTTP + "_4xx", false},
		{"network with URL", 0, &url.Error{Op: "Post", URL: webhook, Err: errors.New("connection refused")}, notify.FailNetwork, true},
	} {
		t.Run(tc.name, func(t *testing.T) { testDeliveryFailure(t, tc) })
	}
}

func testDeliveryFailure(t *testing.T, tc failureCase) {
	t.Helper()
	transport := transportFunc(func(*http.Request) (*http.Response, error) {
		if tc.fault != nil {
			return nil, tc.fault
		}
		return reply(tc.code), nil
	})
	client := webhookClient(t, transport)
	_, err := client.Post(context.Background(), notify.Notice{Summary: "ready"})
	assertDeliveryFailure(t, err, tc)
}

func assertDeliveryFailure(t *testing.T, err error, tc failureCase) {
	t.Helper()
	var failure *notify.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("delivery did not return a bounded failure: %v", err)
	}
	if failure.Class != tc.class {
		t.Fatalf("failure class = %q, want %q", failure.Class, tc.class)
	}
	if failure.Retryable() != tc.retry {
		t.Fatalf("retryable = %t, want %t", failure.Retryable(), tc.retry)
	}
	if strings.Contains(err.Error(), "secret-marker") {
		t.Fatal("webhook secret leaked into the delivery error")
	}
	if strings.Contains(err.Error(), webhook) {
		t.Fatal("webhook URL leaked into the delivery error")
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
