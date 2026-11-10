// Package classifier asks a decision model which agent an untagged operator
// message is for (#339). Stage 1 is shadow mode: a prediction is recorded,
// never applied, so nothing here touches routing, assignments or claims.
//
// A provider is one of two hosted decision-model APIs with the same
// question shape (a "choice" among named criteria): Cloudflare Workers AI
// (Clef) and TypeSafe (Jev). Each has a fixed canonical origin chosen by its
// name; the client never follows a redirect, so the credential reaches no
// other host.
package classifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Provider names, as configured.
const (
	ProviderWorkersAI = "workers-ai" // Cloudflare Workers AI, models clef and clef-flash
	ProviderTypeSafe  = "typesafe"   // TypeSafe, model jev-latest or a pinned version
)

// Canonical origins, by provider; not configurable. Tests inject a
// transport and keep these URLs.
var origins = map[string]string{
	ProviderWorkersAI: "https://api.cloudflare.com",
	ProviderTypeSafe:  "https://api.typesafe.ai",
}

const (
	// AbstainChoice is the criterion that means no single agent is meant.
	// Agents are offered under generated aliases, so no agent ID collides.
	AbstainChoice = "none"
	// AgentsMax bounds the roster offered: the providers cap a choice at
	// 255 options, one of which is the abstain choice.
	AgentsMax = 254
	// Question is the one question asked per message.
	Question = "assignee"
	// StateMax bounds the message text sent, in bytes, on a UTF-8 boundary.
	StateMax = 8 << 10
	// responseMax bounds what is read back from a provider.
	responseMax = 64 << 10
)

// Request is what the model is asked: the state and the choice's criteria.
type Request struct {
	Model        string
	State        string
	Instructions string
	Criteria     map[string]string // choice key -> description; includes AbstainChoice
}

// Answer is the model's choice with its confidence and per-option
// probabilities, and what the provider said it used (token counts and the
// like, by the provider's own names), for cost evaluation later.
type Answer struct {
	Choice        string
	Confidence    float64
	Probabilities map[string]float64
	Model         string           // the served model, when the provider names it
	Usage         map[string]int64 // the provider's usage counters, integer-valued fields only
}

// UsageMax bounds the usage counters kept from a reply.
const UsageMax = 16

// Outcome classes for a failed call; the client reports which, never the body.
const (
	FailTimeout  = "timeout"
	FailRedirect = "redirect"
	FailDecode   = "decode"
	FailHTTP     = "http" // followed by the status class, e.g. http_429, http_5xx
	FailNetwork  = "network"
)

// Failure is a call that produced no answer, classified without its body.
type Failure struct {
	Class string
}

func (f *Failure) Error() string { return "classifier: " + f.Class }

// Credential is who to call and as whom: the provider, its account where
// one applies (Workers AI), and the key, kept in memory only.
type Credential struct {
	Provider string
	Account  string
	Key      string
}

// check: a known provider, its account where needed, and a key.
func (c Credential) check() error {
	if _, ok := origins[c.Provider]; !ok {
		return fmt.Errorf("classifier: unknown provider %q (%s or %s)", c.Provider, ProviderWorkersAI, ProviderTypeSafe)
	}
	if c.Provider == ProviderWorkersAI && c.Account == "" {
		return errors.New("classifier: " + ProviderWorkersAI + " needs the account ID")
	}
	if c.Key == "" {
		return errors.New("classifier: empty credential")
	}
	return nil
}

// Client calls one provider with one credential.
type Client struct {
	Provider string
	Account  string // Workers AI account ID
	Timeout  time.Duration
	key      string
	http     *http.Client
}

// NewClient builds a client for the credential. transport nil is the
// default; tests pass a fake round-tripper and the canonical URL stays.
func NewClient(credential Credential, timeout time.Duration, transport http.RoundTripper) (*Client, error) {
	if err := credential.check(); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Client{Provider: credential.Provider, Account: credential.Account, Timeout: timeout, key: credential.Key,
		http: &http.Client{Timeout: timeout, CheckRedirect: refuseRedirect, Transport: transport}}, nil
}

// refuseRedirect: a 3xx is an error; the credential never follows it.
func refuseRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// endpoint is the provider's URL for the model.
func (c *Client) endpoint(model string) (string, error) {
	base, err := url.Parse(origins[c.Provider])
	if err != nil {
		return "", err
	}
	switch c.Provider {
	case ProviderWorkersAI:
		base.Path = "/client/v4/accounts/" + url.PathEscape(c.Account) + "/ai/run/@cf/cloudflare/" + url.PathEscape(model)
	case ProviderTypeSafe:
		base.Path = "/v1/systemone"
	}
	return base.String(), nil
}

// body is the provider-neutral request body.
func body(req Request) ([]byte, error) {
	if len(req.Criteria) < 2 {
		return nil, errors.New("classifier: a choice needs at least two criteria")
	}
	return json.Marshal(map[string]any{
		"model": req.Model,
		"state": req.State,
		"questions": map[string]any{
			Question: map[string]any{"type": "choice", "instructions": req.Instructions, "criteria": req.Criteria},
		},
	})
}

// Ask sends the question and reads the answer. A failure says why, in a
// class, never with the provider's text.
func (c *Client) Ask(ctx context.Context, req Request) (Answer, error) {
	target, err := c.endpoint(req.Model)
	if err != nil {
		return Answer{}, err
	}
	payload, err := body(req)
	if err != nil {
		return Answer{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return Answer{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.key)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return Answer{}, classify(ctx, err)
	}
	defer resp.Body.Close()
	return c.answer(resp)
}

// classify names a transport failure.
func classify(ctx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return &Failure{FailTimeout}
	}
	return &Failure{FailNetwork}
}

// answer reads a response into an Answer, by status and provider envelope.
func (c *Client) answer(resp *http.Response) (Answer, error) {
	if failure := statusFailure(resp.StatusCode); failure != nil {
		return Answer{}, failure
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, responseMax+1))
	if err != nil || len(data) > responseMax {
		return Answer{}, &Failure{FailDecode}
	}
	answer, ok := decode(c.Provider, data)
	if !ok {
		return Answer{}, &Failure{FailDecode}
	}
	return answer, nil
}

// statusFailure is nil for a 2xx, redirect for a 3xx, else the status class.
func statusFailure(code int) error {
	switch {
	case code >= 200 && code < 300:
		return nil
	case code < 400 && code >= 300:
		return &Failure{FailRedirect}
	}
	return &Failure{statusClass(code)}
}

// statusClass is http_429 for the limits a caller may act on, else http_4xx/5xx.
func statusClass(code int) string {
	switch {
	case code == http.StatusTooManyRequests, code == http.StatusUnauthorized, code == http.StatusForbidden:
		return fmt.Sprintf("%s_%d", FailHTTP, code)
	case code >= 500:
		return FailHTTP + "_5xx"
	default:
		return FailHTTP + "_4xx"
	}
}

// choiceAnswer is a provider's answer to one choice question.
type choiceAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Confidence    *float64           `json:"confidence"` // absent is malformed
	Probabilities map[string]float64 `json:"probabilities"`
}

// workersEnvelope is Workers AI's result wrapper.
type workersEnvelope struct {
	Success bool `json:"success"`
	Result  struct {
		Model   string                     `json:"model"`
		Answers map[string]choiceAnswer    `json:"answers"`
		Usage   map[string]json.RawMessage `json:"usage"`
	} `json:"result"`
}

// typesafeEnvelope is TypeSafe's: answers keyed by question, the served
// model and usage beside them.
type typesafeEnvelope struct {
	Model   string                     `json:"model"`
	Answers map[string]choiceAnswer    `json:"answers"`
	Usage   map[string]json.RawMessage `json:"usage"`
}

// usageCounts keeps the integer-valued fields of a usage object, at most
// UsageMax of them, by the provider's own names.
func usageCounts(raw map[string]json.RawMessage) map[string]int64 {
	if len(raw) == 0 {
		return nil
	}
	out := map[string]int64{}
	for key, value := range raw {
		var n int64
		if json.Unmarshal(value, &n) == nil && len(out) < UsageMax {
			out[key] = n
		}
	}
	return out
}

// decode reads the provider's envelope; false when the answer isn't there.
func decode(provider string, data []byte) (Answer, bool) {
	var model string
	var answers map[string]choiceAnswer
	var usage map[string]json.RawMessage
	switch provider {
	case ProviderWorkersAI:
		var env workersEnvelope
		if json.Unmarshal(data, &env) != nil || !env.Success {
			return Answer{}, false
		}
		model, answers, usage = env.Result.Model, env.Result.Answers, env.Result.Usage
	case ProviderTypeSafe:
		var env typesafeEnvelope
		if json.Unmarshal(data, &env) != nil {
			return Answer{}, false
		}
		model, answers, usage = env.Model, env.Answers, env.Usage
	}
	a, ok := answers[Question]
	if !ok || !a.valid() {
		return Answer{}, false
	}
	return Answer{Choice: a.Choice, Confidence: *a.Confidence, Probabilities: a.Probabilities, Model: model, Usage: usageCounts(usage)}, true
}

// valid: a choice answer with a choice, a confidence in [0, 1] and
// probabilities in [0, 1].
func (a choiceAnswer) valid() bool {
	return a.isChoice() && a.Choice != "" && a.hasConfidence() && a.probabilitiesUnit()
}

func (a choiceAnswer) isChoice() bool { return a.Type == "" || a.Type == "choice" }

func (a choiceAnswer) hasConfidence() bool { return a.Confidence != nil && unit(*a.Confidence) }

func (a choiceAnswer) probabilitiesUnit() bool {
	for _, p := range a.Probabilities {
		if !unit(p) {
			return false
		}
	}
	return true
}

func unit(v float64) bool { return !math.IsNaN(v) && v >= 0 && v <= 1 }

// State is the text the model judges: the message, cut on a UTF-8 boundary,
// with the room named when there is one.
func State(text, room string) string {
	text = cut(strings.TrimSpace(text), StateMax)
	if room == "" {
		return text
	}
	return "Room: " + room + "\n\n" + text
}

// cut shortens s to at most n bytes without splitting a character.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isStart(s[n]) {
		n--
	}
	return s[:n]
}

func isStart(b byte) bool { return b&0xC0 != 0x80 }

// Roster is the choice offered: each eligible agent under a generated
// alias (a1, a2, …), so an agent ID can never collide with the abstain
// choice or confuse the model, with its role line or a plain description;
// plus the abstain choice. Agents is the alias → agent ID map.
type Roster struct {
	Criteria map[string]string
	Agents   map[string]string
}

// NewRoster builds the choice for the sorted eligible agents; the caller
// keeps the roster within AgentsMax.
func NewRoster(agents []string, roles map[string]string) Roster {
	r := Roster{Criteria: make(map[string]string, len(agents)+1), Agents: make(map[string]string, len(agents))}
	for i, id := range agents {
		alias := fmt.Sprintf("a%d", i+1)
		r.Agents[alias] = id
		if role := strings.TrimSpace(roles[id]); role != "" {
			r.Criteria[alias] = id + ": " + role
		} else {
			r.Criteria[alias] = "Agent " + id
		}
	}
	r.Criteria[AbstainChoice] = "No single agent is clearly meant, or several are; leave it to whoever claims it."
	return r
}

// Agent is the agent an answer's choice names, or "" for the abstain
// choice and for anything outside the roster.
func (r Roster) Agent(choice string) string { return r.Agents[choice] }

// Eligible are the sorted, de-duplicated agent IDs. An agent may be
// named "none": the aliases keep it apart from the abstain choice.
func Eligible(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
