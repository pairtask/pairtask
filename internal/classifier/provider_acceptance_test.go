package classifier_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	return `{"model":"jev-1.14.0","answers":{"assignee":{"type":"choice","choice":"a1","confidence":0.91,"probabilities":{"a1":0.95,"a2":0.03,"none":0.02}}},"usage":{"input_tokens":120,"output_tokens":12}}`
}

type canonicalCase struct {
	provider, model, endpoint, response string
}

func TestClassifierProviderAcceptanceCanonicalRequest(t *testing.T) {
	for _, tc := range []canonicalCase{
		{classifier.ProviderTypeSafe, "jev-1.14.0", "https://api.typesafe.ai/v1/systemone", acceptedAnswer()},
		{classifier.ProviderWorkersAI, "clef", "https://api.cloudflare.com/client/v4/accounts/account123/ai/run/@cf/cloudflare/clef", `{"success":true,"result":` + acceptedAnswer() + `}`},
	} {
		t.Run(tc.provider, func(t *testing.T) { checkCanonicalProvider(t, tc) })
	}
}

func checkCanonicalProvider(t *testing.T, tc canonicalCase) {
	t.Helper()
	request := syntheticRequest()
	request.Model = tc.model
	calls := 0
	transport := classifierTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		providerEqual(t, r.URL.String(), tc.endpoint)
		checkProviderRequest(t, r, request)
		return syntheticResponse(r, 200, tc.response), nil
	})
	client, err := classifier.NewClient(classifier.Credential{Provider: tc.provider, Account: "account123", Key: "synthetic-private-marker"}, time.Second, transport)
	providerOK(t, err)
	answer, err := client.Ask(context.Background(), request)
	providerOK(t, err)
	providerEqual(t, calls, 1)
	providerEqual(t, answer.Choice, "a1")
	providerEqual(t, answer.Confidence, .91)
	providerEqual(t, answer.Model, "jev-1.14.0")
	providerEqual(t, answer.Usage, map[string]int64{"input_tokens": 120, "output_tokens": 12})
}

func checkProviderRequest(t *testing.T, r *http.Request, request classifier.Request) {
	t.Helper()
	providerEqual(t, r.Method, http.MethodPost)
	providerEqual(t, r.Header.Get("Authorization"), "Bearer synthetic-private-marker")
	data, err := io.ReadAll(r.Body)
	providerOK(t, err)
	providerEqual(t, strings.Contains(string(data), "synthetic-private-marker"), false)
	var body struct {
		Model, State string
		Questions    map[string]struct {
			Type, Instructions string
			Criteria           map[string]string
		}
	}
	providerOK(t, json.Unmarshal(data, &body))
	q := body.Questions["assignee"]
	providerEqual(t, body.Model, request.Model)
	providerEqual(t, body.State, request.State)
	providerEqual(t, len(body.Questions), 1)
	providerEqual(t, q.Type, "choice")
	providerEqual(t, q.Criteria, request.Criteria)
}

func providerEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func providerOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
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
			client, err := classifier.NewClient(classifier.Credential{Provider: classifier.ProviderTypeSafe, Key: "synthetic-private-marker"}, time.Second, transport)
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
			client, err := classifier.NewClient(classifier.Credential{Provider: classifier.ProviderTypeSafe, Key: "synthetic-private-marker"}, time.Second, transport)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Ask(context.Background(), syntheticRequest())
			requireFailure(t, err, tc.class)
		})
	}
}

func TestClassifierProviderAcceptanceUsageKeepsCostCountersWhenBounded(t *testing.T) {
	usage := map[string]any{"input_tokens": 120, "output_tokens": 12, "negative": -1, "fractional": .5}
	for i := 0; i < classifier.UsageMax+2; i++ {
		usage[fmt.Sprintf("noise_%02d", i)] = 1
	}
	var body map[string]any
	providerOK(t, json.Unmarshal([]byte(acceptedAnswer()), &body))
	body["usage"] = usage
	encoded, err := json.Marshal(body)
	providerOK(t, err)
	transport := classifierTransport(func(r *http.Request) (*http.Response, error) {
		return syntheticResponse(r, 200, string(encoded)), nil
	})
	client, err := classifier.NewClient(classifier.Credential{Provider: classifier.ProviderTypeSafe, Key: "synthetic-private-marker"}, time.Second, transport)
	providerOK(t, err)
	answer, err := client.Ask(context.Background(), syntheticRequest())
	providerOK(t, err)
	providerEqual(t, answer.Usage["input_tokens"], int64(120))
	providerEqual(t, answer.Usage["output_tokens"], int64(12))
	providerEqual(t, len(answer.Usage) <= classifier.UsageMax, true)
	_, keptNegative := answer.Usage["negative"]
	_, keptFractional := answer.Usage["fractional"]
	providerEqual(t, keptNegative, false)
	providerEqual(t, keptFractional, false)
}

func TestClassifierProviderAcceptanceDeadline(t *testing.T) {
	transport := classifierTransport(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	client, err := classifier.NewClient(classifier.Credential{Provider: classifier.ProviderTypeSafe, Key: "synthetic-private-marker"}, 20*time.Millisecond, transport)
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
	providerEqual(t, utf8.ValidString(state), true)
	providerEqual(t, len(state) <= classifier.StateMax, true)
	providerEqual(t, strings.HasPrefix(state, "é"), true)
	got := classifier.State("hello", "team")
	providerEqual(t, strings.Contains(got, "team"), true)
	providerEqual(t, strings.Contains(got, "hello"), true)
}
