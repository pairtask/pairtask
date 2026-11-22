package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/personkey"
)

// pairtask space admin: an organization's Pairbook admin actions from the
// host, over HTTPS to the Pairbook itself (admin is a person's act, not the
// workspace's). login signs in in the browser and comes back to a loopback
// listener with a single-use code, exchanged with a PKCE verifier for an
// hour-long token kept 0600 beside the person's keys, one per Pairbook.

const adminUsage = `pairtask space admin login https://PAIRBOOK   sign in as an admin (browser)
pairtask space admin spaces | create NAME | show SPACE | audit
pairtask space admin remove SPACE PERSON
pairtask space admin approve|deny SPACE ENROLLMENT
pairtask space admin admission SPACE approve|auto
pairtask space admin policy SPACE              groups and access rules, with the version they apply to
pairtask space admin policy-set SPACE FILE     set them from JSON (what policy shows, edited); refused if the space changed
pairtask space admin logout
  --pairbook https://PAIRBOOK picks one when you are signed in to several`

var adminLoginWait = 5 * time.Minute

// adminToken is a kept admin token.
type adminToken struct {
	Origin    string    `json:"origin"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// adminClient never follows a redirect: a token goes only to the Pairbook
// it was issued by.
var adminClient = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// pairbookAdmin runs one admin verb.
func pairbookAdmin(ctx context.Context, s *pairbookSession, a pairbookArgs) (any, error) {
	if len(a.words) == 0 {
		return nil, errors.New(adminUsage)
	}
	verb, words := a.words[0], a.words[1:]
	if verb == "login-from-host" {
		return adminHostLogin(ctx, s, words, os.Stdin)
	}
	if verb == "login" {
		return adminLogin(ctx, s, words)
	}
	tok, err := s.adminTokens().pick(a.directory)
	if err != nil {
		return nil, err
	}
	out, err := adminVerb(ctx, s, adminInvocation{tok: tok, verb: verb, words: words})
	if errors.Is(err, errAdminSignedOut) {
		s.adminTokens().drop(tok.Origin) // the Pairbook said so: the kept sign-in is no good
	}
	return out, err
}

// errAdminSignedOut: the Pairbook refused the token (401); a network
// failure is never this.
var errAdminSignedOut = errors.New("not signed in, or the sign-in expired")

// adminInvocation is one admin verb with its words, signed in as tok.
type adminInvocation struct {
	tok   adminToken
	verb  string
	words []string
}

// adminRequest is one admin API call.
type adminRequest struct {
	method, path string
	body         any
}

type adminRun struct {
	words  int
	method string
	path   func(w []string) string
	body   func(ctx context.Context, tok adminToken, w []string) (any, error)
}

var adminVerbs = map[string]adminRun{
	"spaces":     {0, http.MethodGet, func([]string) string { return "spaces" }, nil},
	"audit":      {0, http.MethodGet, func([]string) string { return "audit" }, nil},
	"show":       {1, http.MethodGet, func(w []string) string { return "spaces/" + url.PathEscape(w[0]) }, nil},
	"create":     {1, http.MethodPost, func([]string) string { return "spaces" }, field("name", 0)},
	"remove":     {2, http.MethodPost, func(w []string) string { return "spaces/" + url.PathEscape(w[0]) + "/remove" }, field("person", 1)},
	"admission":  {2, http.MethodPost, func(w []string) string { return "spaces/" + url.PathEscape(w[0]) + "/admission" }, field("policy", 1)},
	"approve":    {2, http.MethodPost, enrollmentPath("approve"), shownEnrollment},
	"deny":       {2, http.MethodPost, enrollmentPath("deny"), shownEnrollment},
	"policy":     {1, http.MethodGet, policyPath, nil},
	"policy-set": {2, http.MethodPut, policyPath, policyFile},
}

func policyPath(w []string) string { return "spaces/" + url.PathEscape(w[0]) + "/policy" }

// policyFile is the policy to set, as JSON from the file named: groups,
// grants and the base_version it was edited from.
func policyFile(_ context.Context, _ adminToken, w []string) (any, error) {
	b, err := os.ReadFile(w[1])
	if err != nil {
		return nil, err
	}
	var policy json.RawMessage
	if err := json.Unmarshal(b, &policy); err != nil {
		return nil, fmt.Errorf("%s isn't JSON: %w", w[1], err)
	}
	return policy, nil
}

func field(name string, i int) func(context.Context, adminToken, []string) (any, error) {
	return func(_ context.Context, _ adminToken, w []string) (any, error) {
		return map[string]string{name: w[i]}, nil
	}
}

func enrollmentPath(verb string) func([]string) string {
	return func(w []string) string {
		return "spaces/" + url.PathEscape(w[0]) + "/enrollments/" + url.PathEscape(w[1]) + "/" + verb
	}
}

// shownEnrollment is the person and workspace the Pairbook shows for the
// enrollment now: what the admin decides on.
func shownEnrollment(ctx context.Context, tok adminToken, w []string) (any, error) {
	var detail struct {
		Pending []struct {
			ID, Person, Workspace string
		} `json:"pending"`
	}
	if err := adminCall(ctx, tok, adminRequest{method: http.MethodGet, path: "spaces/" + url.PathEscape(w[0])}, &detail); err != nil {
		return nil, err
	}
	for _, p := range detail.Pending {
		if p.ID == w[1] {
			return map[string]string{"person": p.Person, "workspace": p.Workspace}, nil
		}
	}
	return nil, errors.New("no enrollment " + w[1] + " is waiting in that space; pairtask space admin show SPACE lists them")
}

func adminVerb(ctx context.Context, s *pairbookSession, in adminInvocation) (any, error) {
	if in.verb == "logout" {
		return adminLogout(ctx, s, in.tok)
	}
	run, ok := adminVerbs[in.verb]
	if !ok || len(in.words) != run.words {
		return nil, errors.New(adminUsage)
	}
	req := adminRequest{method: run.method, path: run.path(in.words)}
	if run.body != nil {
		var err error
		if req.body, err = run.body(ctx, in.tok, in.words); err != nil {
			return nil, err
		}
	}
	var out json.RawMessage
	if err := adminCall(ctx, in.tok, req, &out); err != nil {
		return nil, err
	}
	return adminAnswer(out), nil
}

// adminAnswer prints the Pairbook's JSON as it came, indented.
type adminAnswer json.RawMessage

func (a adminAnswer) String() string {
	var b bytes.Buffer
	if json.Indent(&b, a, "", "  ") != nil {
		return string(a)
	}
	return b.String()
}

func (a adminAnswer) MarshalJSON() ([]byte, error) { return a, nil }

// adminCall makes one admin API call with tok.
func adminCall(ctx context.Context, tok adminToken, call adminRequest, out any) error {
	var rd io.Reader
	if call.body != nil {
		b, err := json.Marshal(call.body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, call.method, tok.Origin+"/admin/api/"+call.path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	req.Header.Set("Content-Type", "application/json")
	return adminDo(req, out)
}

func adminDo(req *http.Request, out any) error {
	res, err := adminClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return err
	}
	if res.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%w: pairtask space admin login %s://%s", errAdminSignedOut, req.URL.Scheme, req.URL.Host)
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("the Pairbook refused: %s %s", res.Status, strings.TrimSpace(string(b)))
	}
	if out == nil || len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

// adminLogin signs in at the Pairbook in the browser and keeps the token.
func adminLogin(ctx context.Context, s *pairbookSession, words []string) (any, error) {
	if len(words) != 1 {
		return nil, errors.New(adminUsage)
	}
	origin := pairbookOriginOf(words[0])
	if origin == "" {
		return nil, errors.New("give the Pairbook as https://HOST")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer ln.Close()
	p, err := newPKCE()
	if err != nil {
		return nil, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	start := origin + "/admin/cli?" + url.Values{"port": {strconv.Itoa(port)}, "state": {p.state}, "challenge": {p.challenge}}.Encode()
	fmt.Fprintf(s.progressOut(), "Sign in at %s\n", start)
	openURL(start)
	code, err := awaitCode(ctx, ln, p.state)
	if err != nil {
		return nil, err
	}
	return finishAdminLogin(ctx, s, origin, tokenRequest{Code: code, Verifier: p.verifier, Port: port})
}

// adminHostLogin accepts a single-use exchange from the installed host launcher's
// private stdin pipe. The access token stays inside the secured runtime.
func adminHostLogin(ctx context.Context, s *pairbookSession, words []string, input io.Reader) (any, error) {
	if len(words) != 1 {
		return nil, errors.New(adminUsage)
	}
	origin := pairbookOriginOf(words[0])
	if origin == "" {
		return nil, errors.New("give the Pairbook as https://HOST")
	}
	var in tokenRequest
	if err := json.NewDecoder(io.LimitReader(input, 8192)).Decode(&in); err != nil {
		return nil, errors.New("could not read the host sign-in response")
	}
	return finishAdminLogin(ctx, s, origin, in)
}

func finishAdminLogin(ctx context.Context, s *pairbookSession, origin string, in tokenRequest) (any, error) {
	tok, err := exchangeCode(ctx, origin, in)
	if err != nil {
		return nil, err
	}
	if err := s.adminTokens().save(tok); err != nil {
		return nil, err
	}
	return done("Signed in to " + origin + " as an admin until " + tok.ExpiresAt.Local().Format("15:04") + "."), nil
}

type pkce struct{ state, verifier, challenge string }

func newPKCE() (pkce, error) {
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		return pkce{}, err
	}
	enc := base64.RawURLEncoding
	p := pkce{state: enc.EncodeToString(b[:24]), verifier: enc.EncodeToString(b[24:])}
	sum := sha256.Sum256([]byte(p.verifier))
	p.challenge = enc.EncodeToString(sum[:])
	return p, nil
}

// awaitCode serves the loopback callback once: the code, if the state is ours.
func awaitCode(ctx context.Context, ln net.Listener, state string) (string, error) {
	got := make(chan string, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code, ok := callbackCode(r, state)
		if !ok {
			http.Error(w, "this isn't the sign-in this terminal started", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "Signed in. You can close this tab and go back to your terminal.")
		select {
		case got <- code:
		default:
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	wait, cancel := context.WithTimeout(ctx, adminLoginWait)
	defer cancel()
	select {
	case code := <-got:
		return code, nil
	case <-wait.Done():
		return "", errors.New("no sign-in came back in time; run pairtask space admin login again")
	}
}

// callbackCode is the loopback callback's code, if it carries our state.
func callbackCode(r *http.Request, state string) (string, bool) {
	q := r.URL.Query()
	switch {
	case r.URL.Path != "/done", q.Get("state") != state:
		return "", false
	}
	return q.Get("code"), q.Get("code") != ""
}

type tokenRequest struct {
	Code     string `json:"code"`
	Verifier string `json:"verifier"`
	Port     int    `json:"port"`
}

func exchangeCode(ctx context.Context, origin string, in tokenRequest) (adminToken, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return adminToken{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/admin/api/token", bytes.NewReader(b))
	if err != nil {
		return adminToken{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	tok := adminToken{Origin: origin}
	if err := adminDo(req, &tok); err != nil {
		return adminToken{}, err
	}
	tok.Origin = origin
	return tok, nil
}

func adminLogout(ctx context.Context, s *pairbookSession, tok adminToken) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tok.Origin+"/admin/api/logout", nil)
	if err == nil {
		req.Header.Set("Authorization", "Bearer "+tok.Token)
		err = adminDo(req, nil)
	}
	s.adminTokens().drop(tok.Origin)
	if err != nil {
		return nil, fmt.Errorf("signed out here; the Pairbook couldn't be told (the token still ends at %s): %w", tok.ExpiresAt.Local().Format("15:04"), err)
	}
	return done("Signed out of " + tok.Origin + "."), nil
}

// pairbookOriginOf is s as https://HOST[:PORT], or "".
func pairbookOriginOf(s string) string {
	u, err := url.Parse(strings.TrimRight(s, "/"))
	if err != nil || !bareOrigin(u) {
		return ""
	}
	return "https://" + u.Host
}

// bareOrigin: u is https://HOST[:PORT] and nothing more.
func bareOrigin(u *url.URL) bool {
	switch {
	case u.Scheme != "https", u.Host == "", u.User != nil:
		return false
	case u.Path != "", u.RawQuery != "", u.Fragment != "":
		return false
	}
	return true
}

// adminTokens is the folder of kept admin tokens, one file per Pairbook.
type adminTokens string

func (s *pairbookSession) adminTokens() adminTokens {
	if s.adminDir != "" {
		return adminTokens(s.adminDir)
	}
	dir, err := personkey.ResolveDir()
	if err != nil {
		return ""
	}
	return adminTokens(filepath.Join(dir, "admin"))
}

func (d adminTokens) path(origin string) string {
	return filepath.Join(string(d), strings.TrimPrefix(origin, "https://")+".json")
}

func (d adminTokens) save(t adminToken) error {
	if d == "" {
		return errors.New("no place to keep the sign-in")
	}
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return os.WriteFile(d.path(t.Origin), b, 0o600)
}

func (d adminTokens) drop(origin string) { _ = os.Remove(d.path(origin)) }

// pick is the live token for origin, or the only live one when origin is "".
func (d adminTokens) pick(origin string) (adminToken, error) {
	live := d.live()
	if origin != "" {
		o := pairbookOriginOf(origin)
		for _, t := range live {
			if t.Origin == o {
				return t, nil
			}
		}
		return adminToken{}, errors.New("not signed in to " + origin + ": pairtask space admin login " + origin)
	}
	switch len(live) {
	case 0:
		return adminToken{}, errors.New("not signed in to any Pairbook: pairtask space admin login https://PAIRBOOK")
	case 1:
		return live[0], nil
	}
	return adminToken{}, errors.New("signed in to several Pairbooks: name one with --pairbook")
}

// live are the kept tokens not yet expired; expired ones are removed.
func (d adminTokens) live() []adminToken {
	files, _ := filepath.Glob(filepath.Join(string(d), "*.json"))
	var out []adminToken
	for _, f := range files {
		var t adminToken
		b, err := os.ReadFile(f)
		if err != nil || json.Unmarshal(b, &t) != nil {
			continue
		}
		if time.Now().After(t.ExpiresAt) {
			_ = os.Remove(f)
			continue
		}
		out = append(out, t)
	}
	return out
}

// openURL opens u in the browser, best effort.
func openURL(u string) { openBrowserURL(u) }
