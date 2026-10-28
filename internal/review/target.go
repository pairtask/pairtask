package review

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/pairtask/pairtask/internal/protocol"
)

var (
	shortTarget = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)#([0-9]+)$`)
	segment     = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	number      = regexp.MustCompile(`^[0-9]+$`)
	commitID    = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
)

// pullTabs are the pull request pages a browser link may point at, by how
// many further segments each takes: /commits may name one commit.
var pullTabs = map[string]int{"files": 0, "commits": 1, "checks": 0}

// ParseTarget reads a change to review the way an operator types or pastes
// it: OWNER/REPO#N, or a GitHub pull request link as a browser shows it
// (https://github.com/OWNER/REPO/pull/N, www. included, with a trailing
// slash, a /files, /commits[/SHA] or /checks tab, a query or a fragment).
// Other forges are named explicitly instead.
func ParseTarget(raw string) (protocol.ReviewTarget, error) {
	if m := shortTarget.FindStringSubmatch(raw); m != nil && plainName(m[1], m[2]) {
		return githubTarget(m[1], m[2], m[3]), nil
	}
	parts, ok := githubPullPath(raw)
	if !ok {
		return protocol.ReviewTarget{}, fmt.Errorf("TARGET %q is neither a GitHub pull request URL nor OWNER/REPO#N", raw)
	}
	return githubTarget(parts[0], parts[1], parts[3]), nil
}

func githubTarget(owner, repo, change string) protocol.ReviewTarget {
	return protocol.ReviewTarget{Forge: "github", Repository: "github.com/" + owner + "/" + repo, Change: change}
}

// githubPullPath is OWNER, REPO, "pull" and N from a pull request link on
// github.com: https only, no account, no port, no escaped or dot segments.
func githubPullPath(raw string) ([]string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" {
		return nil, false
	}
	if u.Host != "github.com" && u.Host != "www.github.com" {
		return nil, false
	}
	if u.RawPath != "" || strings.Contains(u.Path, "//") {
		return nil, false
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" || !number.MatchString(parts[3]) || !plainName(parts[0], parts[1]) {
		return nil, false
	}
	return parts[:4], pullTab(parts[4:])
}

// pullTab accepts nothing, or one known tab with what it takes.
func pullTab(rest []string) bool {
	if len(rest) == 0 {
		return true
	}
	extra, ok := pullTabs[rest[0]]
	if !ok || len(rest)-1 > extra {
		return false
	}
	return len(rest) == 1 || commitID.MatchString(rest[1])
}

// plainName: an owner and a repository name, neither a dot segment.
func plainName(names ...string) bool {
	for _, n := range names {
		if !segment.MatchString(n) || strings.Trim(n, ".") == "" {
			return false
		}
	}
	return true
}
