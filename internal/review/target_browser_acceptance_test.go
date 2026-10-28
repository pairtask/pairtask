package review_test

import (
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/review"
	"testing"
)

func TestBrowserPullLinksResolveToSameReviewTarget(t *testing.T) {
	for _, host := range []string{"github.com", "www.github.com"} {
		for _, suffix := range []string{"", "/", "/files", "/commits", "/commits/0123456789abcdef0123456789abcdef01234567", "/checks", "/files?diff=split#diff-123", "?tab=files#discussion_r1"} {
			raw := "https://" + host + "/3tio/3t.interceptor/pull/323" + suffix
			got, err := review.ParseTarget(raw)
			if err != nil {
				t.Errorf("%s: %v", raw, err)
				continue
			}
			want := (protocol.ReviewTarget{Forge: "github", Repository: "github.com/3tio/3t.interceptor", Change: "323"})
			if got != want {
				t.Errorf("%s: %+v", raw, got)
			}
		}
	}
}

func TestBrowserPullLinksRejectOtherOriginsAndPaths(t *testing.T) {
	for _, raw := range []string{
		"https://github.com.evil.test/a/b/pull/3", "https://github.com@evil.test/a/b/pull/3", "https://user:pass@github.com/a/b/pull/3",
		"http://github.com/a/b/pull/3", "https://github.com:444/a/b/pull/3", "https://github.com/a/b/issues/3",
		"https://github.com/a/b/pull/not-a-number", "https://github.com/a/b/pull/3/files/other", "https://github.com/a/b/pull/3/../../issues/4",
	} {
		if got, err := review.ParseTarget(raw); err == nil {
			t.Errorf("accepted unsupported target %s: %+v", raw, got)
		}
	}
}
