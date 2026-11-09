package classifier_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pairtask/pairtask/internal/classifier"
)

type classifierTransport func(*http.Request) (*http.Response, error)

func (f classifierTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func syntheticRequest() classifier.Request {
	return classifier.Request{Model: "jev-1.14.0", State: "Please write regression tests.", Instructions: "Select one agent or abstain.", Criteria: map[string]string{"a1": "tester", "a2": "builder", "none": "abstain"}}
}

func syntheticResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func acceptedAnswer() string {
	return `{"model":"jev-1.14.0","answers":{"assignee":{"type":"choice","choice":"a1","confidence":0.91,"probabilities":{"a1":0.95,"a2":0.03,"none":0.02}}}}`
}

func TestClassifierProviderAcceptanceCanonicalRequest(t *testing.T) {
	for _, provider := range []string{classifier.ProviderTypeSafe, classifier.ProviderWorkersAI} {
		t.Run(provider, func(t *testing.T) {
			request := syntheticRequest()
			wantURL := "https://api.typesafe.ai/v1/systemone"
			response := acceptedAnswer()
			if provider == classifier.ProviderWorkersAI {
				request.Model = "clef"
				wantURL = "https://api.cloudflare.com/client/v4/accounts/account123/ai/run/@cf/cloudflare/clef"
				response = `{"success":true,"result":` + response + `}`
			}
			calls := 0
			transport := classifierTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != wantURL || r.Method != http.MethodPost {
					t.Errorf("request = %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Authorization") != "Bearer synthetic-private-marker" {
					t.Error("missing credential header")
				}
				data, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), "synthetic-private-marker") {
					t.Error("credential entered request content")
				}
				var body struct {
					Model, State string
					Questions    map[string]struct {
						Type, Instructions string
						Criteria           map[string]string
					}
				}
				if err := json.Unmarshal(data, &body); err != nil {
					t.Fatal(err)
				}
				q := body.Questions["assignee"]
				if body.Model != request.Model || body.State != request.State || len(body.Questions) != 1 || q.Type != "choice" || !reflect.DeepEqual(q.Criteria, request.Criteria) {
					t.Errorf("unexpected provider body: %s", data)
				}
				return syntheticResponse(r, 200, response), nil
			})
			client, err := classifier.NewClient(provider, "account123", "synthetic-private-marker", time.Second, transport)
			if err != nil {
				t.Fatal(err)
			}
			answer, err := client.Ask(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || answer.Choice != "a1" || answer.Confidence != .91 || answer.Model != "jev-1.14.0" {
				t.Fatalf("calls=%d answer=%+v", calls, answer)
			}
		})
	}
}

func TestClassifierProviderAcceptanceRedirectNeverForwardsCredential(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			transport := classifierTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls != 1 || r.URL.Host != "api.typesafe.ai" {
					t.Error("redirect target received a request")
				}
				resp := syntheticResponse(r, status, "synthetic-private-marker")
				resp.Header.Set("Location", "https://api.typesafe.ai/untrusted-target")
				return resp, nil
			})
			client, err := classifier.NewClient(classifier.ProviderTypeSafe, "", "synthetic-private-marker", time.Second, transport)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Ask(context.Background(), syntheticRequest())
			requireFailure(t, err, classifier.FailRedirect)
			if calls != 1 {
				t.Fatalf("redirect made %d requests", calls)
			}
		})
	}
}

func requireFailure(t *testing.T, err error, class string) {
	t.Helper()
	var failure *classifier.Failure
	if !errors.As(err, &failure) || failure.Class != class {
		t.Fatalf("error = %v, want class %s", err, class)
	}
	if strings.Contains(err.Error(), "synthetic-private-marker") {
		t.Fatal("provider error exposed credential/body")
	}
}

func TestClassifierProviderAcceptanceInvalidResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body, class string
		status            int
	}{
		{"rate limit", "synthetic-private-marker", "http_429", 429},
		{"server error", "synthetic-private-marker", "http_5xx", 503},
		{"invalid JSON", "synthetic-private-marker", "decode", 200},
		{"missing answer", `{}`, "decode", 200},
		{"missing confidence", `{"answers":{"assignee":{"type":"choice","choice":"a1"}}}`, "decode", 200},
		{"negative confidence", strings.Replace(acceptedAnswer(), "0.91", "-0.1", 1), "decode", 200},
		{"confidence above one", strings.Replace(acceptedAnswer(), "0.91", "1.01", 1), "decode", 200},
		{"wrong answer type", strings.Replace(acceptedAnswer(), `"choice","choice"`, `"noul","choice"`, 1), "decode", 200},
		{"oversized body", strings.Repeat(" ", 65537) + acceptedAnswer(), "decode", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := classifierTransport(func(r *http.Request) (*http.Response, error) { return syntheticResponse(r, tc.status, tc.body), nil })
			client, err := classifier.NewClient(classifier.ProviderTypeSafe, "", "synthetic-private-marker", time.Second, transport)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Ask(context.Background(), syntheticRequest())
			requireFailure(t, err, tc.class)
		})
	}
}

func TestClassifierProviderAcceptanceDeadline(t *testing.T) {
	transport := classifierTransport(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	client, err := classifier.NewClient(classifier.ProviderTypeSafe, "", "synthetic-private-marker", 20*time.Millisecond, transport)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = client.Ask(ctx, syntheticRequest())
	requireFailure(t, err, classifier.FailTimeout)
	if ctx.Err() != nil {
		t.Fatal("provider ignored its own deadline")
	}
}

func TestClassifierRosterAcceptanceIncludesAgentNamedNone(t *testing.T) {
	ids := classifier.Eligible([]string{"tester", "none", "", "tester", "builder"})
	if !reflect.DeepEqual(ids, []string{"builder", "none", "tester"}) {
		t.Fatalf("eligible = %v", ids)
	}
	roster := classifier.NewRoster(ids, map[string]string{"none": "writes tests"})
	var found bool
	for alias, id := range roster.Agents {
		if id == "none" {
			found = true
			if alias == classifier.AbstainChoice || roster.Agent(alias) != "none" {
				t.Fatal("agent collides with abstain")
			}
		}
	}
	if !found || roster.Agent(classifier.AbstainChoice) != "" {
		t.Fatal("agent or abstain missing")
	}
}

func TestClassifierStateAcceptanceUTF8(t *testing.T) {
	state := classifier.State(strings.Repeat("é", classifier.StateMax), "")
	if !utf8.ValidString(state) || len(state) > classifier.StateMax || !strings.HasPrefix(state, "é") {
		t.Fatal("invalid or overlong truncated state")
	}
	if got := classifier.State("hello", "team"); !strings.Contains(got, "team") || !strings.Contains(got, "hello") {
		t.Fatalf("room/text absent: %q", got)
	}
}
