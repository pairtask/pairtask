// Package github is the GitHub adapter for agent reviews (#201): it pins a
// pull request's revisions, lists what it changed, publishes a review on the
// pinned head and finds one already published, with the reviewing agent's
// own token. It talks only to the configured API origin and never follows a
// redirect, so the token goes nowhere else.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/forge"
)

// Config is where the API is and how to authenticate.
type Config struct {
	BaseURL  string // the API origin, e.g. https://api.github.com or https://HOST/api/v3
	Host     string // the repositories' host, e.g. github.com
	Token    func(ctx context.Context) (string, error)
	HTTP     *http.Client
	MaxPages int // pages read when listing reviews
}

const (
	defaultPages = 10
	maxBody      = 1 << 20
	compareFiles = 300 // GitHub lists at most this many files of a comparison
)

// Production is github.com with the agent's own token: GH_TOKEN, then
// GITHUB_TOKEN, then gh's stored login. It is read for every call and never
// logged.
func Production() Config {
	return Config{BaseURL: "https://api.github.com", Host: "github.com", Token: tokenFromEnvOrGH("github.com")}
}

func tokenFromEnvOrGH(host string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
			if t := strings.TrimSpace(os.Getenv(name)); t != "" {
				return t, nil
			}
		}
		out, err := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", host).Output()
		token := strings.TrimSpace(string(out))
		if err != nil || token == "" {
			return "", forge.Errorf(forge.ClassForbidden, "no GitHub token: set GH_TOKEN or run gh auth login")
		}
		return token, nil
	}
}

// Adapter is a GitHub forge.
type Adapter struct {
	cfg    Config
	origin *url.URL
	http   *http.Client
}

// New is an adapter for cfg. A client that would follow redirects is given
// one that never does.
func New(cfg Config) *Adapter {
	if cfg.MaxPages <= 0 {
		cfg.MaxPages = defaultPages
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.github.com"
	}
	if cfg.Host == "" {
		cfg.Host = "github.com"
	}
	if cfg.Token == nil {
		cfg.Token = func(context.Context) (string, error) {
			return "", forge.Errorf(forge.ClassForbidden, "no GitHub token configured")
		}
	}
	origin, _ := url.Parse(strings.TrimRight(cfg.BaseURL, "/"))
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg.HTTP != nil {
		copied := *cfg.HTTP
		client = &copied
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("redirects are refused") }
	return &Adapter{cfg: cfg, origin: origin, http: client}
}

func (a *Adapter) Name() string { return "github" }

func (a *Adapter) Capabilities() forge.Caps {
	return forge.Caps{FormalApprove: true, FormalRequestChanges: true, LineComments: true, Reconcile: true}
}

// Identity is the token's account.
func (a *Adapter) Identity(ctx context.Context) (string, error) {
	var user struct {
		Login string `json:"login"`
	}
	if err := a.get(ctx, "/user", nil, &user); err != nil {
		return "", err
	}
	if user.Login == "" {
		return "", forge.Errorf(forge.ClassUnavailable, "GitHub returned no login")
	}
	return user.Login, nil
}

// repo is OWNER/NAME of a repository on this adapter's host.
func (a *Adapter) repo(t forge.Target) (string, error) {
	path, ok := strings.CutPrefix(t.Repository, a.cfg.Host+"/")
	owner, name, found := strings.Cut(path, "/")
	if !ok || !found {
		return "", forge.Errorf(forge.ClassNotFound, "repository is not on %s", a.cfg.Host)
	}
	if !plainSegments(owner, name) {
		return "", forge.Errorf(forge.ClassNotFound, "repository is not on %s", a.cfg.Host)
	}
	return url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}

func (a *Adapter) pullPath(t forge.Target, rest string) (string, error) {
	repo, err := a.repo(t)
	if err != nil {
		return "", err
	}
	if !isNumber(t.Change) {
		return "", forge.Errorf(forge.ClassNotFound, "a GitHub change is a pull request number")
	}
	return "/repos/" + repo + "/pulls/" + t.Change + rest, nil
}

// plainSegments are non-empty path segments without a further slash.
func plainSegments(segments ...string) bool {
	for _, seg := range segments {
		if seg == "" || strings.Contains(seg, "/") {
			return false
		}
	}
	return true
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// call is one API request: a method, a path under the API origin, its
// query, and a JSON body for a write.
type call struct {
	method string
	path   string
	query  url.Values
	body   any
}

// write: a request that changes something on GitHub, so a failure after it
// was sent may have landed.
func (c call) write() bool { return c.method != http.MethodGet }

// failed is the class of an exchange that broke, or of an answer that
// couldn't be read: after a write, the write may have landed.
func (c call) failed() string {
	if c.write() {
		return forge.ClassUncertain
	}
	return forge.ClassUnavailable
}

func (a *Adapter) get(ctx context.Context, path string, query url.Values, out any) error {
	_, err := a.do(ctx, call{method: http.MethodGet, path: path, query: query}, out)
	return err
}

// getPage reads one page of a list and reports whether GitHub announced a
// next one. A next link to any other origin is refused, never followed.
func (a *Adapter) getPage(ctx context.Context, path string, query url.Values, out any) (bool, error) {
	header, err := a.do(ctx, call{method: http.MethodGet, path: path, query: query}, out)
	if err != nil {
		return false, err
	}
	next, ok := nextLink(header.Get("Link"))
	if !ok {
		return false, nil
	}
	if !a.sameOrigin(next) {
		return false, forge.Errorf(forge.ClassUnavailable, "GitHub announced a next page on another origin")
	}
	return true, nil
}

// nextLink is the rel="next" target of a Link header.
func nextLink(header string) (*url.URL, bool) {
	for _, part := range strings.Split(header, ",") {
		target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
		if !ok || !strings.Contains(params, `rel="next"`) {
			continue
		}
		u, err := url.Parse(strings.Trim(strings.TrimSpace(target), "<>"))
		return u, err == nil
	}
	return nil, false
}

func (a *Adapter) sameOrigin(u *url.URL) bool {
	return u.Scheme == a.origin.Scheme && u.Host == a.origin.Host
}

// do sends one request to the API origin and decodes a 2xx answer into out,
// returning the answer's headers.
func (a *Adapter) do(ctx context.Context, c call, out any) (http.Header, error) {
	req, err := a.request(ctx, c)
	if err != nil {
		return nil, err
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, forge.Errorf(c.failed(), "GitHub request failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(data) > maxBody {
		return nil, forge.Errorf(c.failed(), "GitHub's answer could not be read")
	}
	if resp.StatusCode/100 != 2 {
		return nil, statusError(c, answer{resp.StatusCode, resp.Header, data})
	}
	if out != nil && json.Unmarshal(data, out) != nil {
		return nil, forge.Errorf(c.failed(), "GitHub's answer could not be decoded")
	}
	return resp.Header, nil
}

func (a *Adapter) request(ctx context.Context, c call) (*http.Request, error) {
	if a.origin == nil || a.origin.Scheme == "" {
		return nil, forge.Errorf(forge.ClassUnavailable, "no GitHub API origin configured")
	}
	target := *a.origin
	target.Path = strings.TrimRight(a.origin.Path, "/") + c.path
	target.RawQuery = c.query.Encode()
	reader, err := jsonBody(c.body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, c.method, target.String(), reader)
	if err != nil {
		return nil, forge.Errorf(forge.ClassInvalid, "bad GitHub request")
	}
	token, err := a.cfg.Token(ctx)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func jsonBody(body any) (io.Reader, error) {
	if body == nil {
		return nil, nil
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, forge.Errorf(forge.ClassInvalid, "review could not be encoded")
	}
	return bytes.NewReader(data), nil
}

// answer is a non-2xx answer: its status, headers and bounded body.
type answer struct {
	status int
	header http.Header
	body   []byte
}

// statusRules class an answer by its status alone, first match wins;
// GitHub's own text is never repeated.
var statusRules = []struct {
	match func(answer) bool
	class string
	text  string
}{
	{func(a answer) bool { return a.status == http.StatusUnauthorized }, forge.ClassForbidden, "GitHub refused the token (401)"},
	{func(a answer) bool { return a.status == http.StatusForbidden && rateLimited(a.header) }, forge.ClassRateLimited, "GitHub rate limit (403)"},
	{func(a answer) bool { return a.status == http.StatusForbidden }, forge.ClassForbidden, "GitHub forbids this (403)"},
	{func(a answer) bool { return a.status == http.StatusNotFound }, forge.ClassNotFound, "not found on GitHub (404)"},
	{func(a answer) bool { return a.status == http.StatusTooManyRequests }, forge.ClassRateLimited, "GitHub rate limit (429)"},
	{func(a answer) bool { return a.status == http.StatusUnprocessableEntity && ownPullRequest(a.body) }, forge.ClassSelfReview,
		"GitHub doesn't let an author review their own pull request (422)"},
	{func(a answer) bool { return a.status == http.StatusUnprocessableEntity }, forge.ClassInvalid, "GitHub refused the review (422)"},
}

func statusError(c call, a answer) error {
	for _, rule := range statusRules {
		if rule.match(a) {
			return forge.Errorf(rule.class, "%s", rule.text)
		}
	}
	return forge.Errorf(c.failed(), "GitHub answered %d", a.status)
}

func rateLimited(h http.Header) bool {
	return h.Get("X-RateLimit-Remaining") == "0" || h.Get("Retry-After") != ""
}

func ownPullRequest(body []byte) bool {
	return strings.Contains(strings.ToLower(string(body)), "own pull request")
}

var _ forge.Forge = (*Adapter)(nil)

func fullRevision(id string) bool {
	if len(id) != 40 && len(id) != 64 {
		return false
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
