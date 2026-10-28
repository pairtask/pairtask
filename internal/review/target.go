package review

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/pairtask/pairtask/internal/protocol"
)

var shortTarget = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)#([0-9]+)$`)

// ParseTarget reads a change to review the way an operator types it: a
// GitHub pull request URL (https://github.com/OWNER/REPO/pull/N) or
// OWNER/REPO#N. Other forges are named explicitly instead.
func ParseTarget(raw string) (protocol.ReviewTarget, error) {
	if m := shortTarget.FindStringSubmatch(raw); m != nil {
		return protocol.ReviewTarget{Forge: "github", Repository: "github.com/" + m[1] + "/" + m[2], Change: m[3]}, nil
	}
	parts, ok := githubPullPath(raw)
	if !ok {
		return protocol.ReviewTarget{}, fmt.Errorf("TARGET %q is neither a GitHub pull request URL nor OWNER/REPO#N", raw)
	}
	return protocol.ReviewTarget{Forge: "github", Repository: "github.com/" + parts[0] + "/" + parts[1], Change: parts[3]}, nil
}

// githubPullPath is OWNER, REPO, "pull" and N from a pull request URL.
func githubPullPath(raw string) ([]string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme+"://"+u.Host != "https://github.com" {
		return nil, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 {
		return nil, false
	}
	return parts, parts[2] == "pull"
}
