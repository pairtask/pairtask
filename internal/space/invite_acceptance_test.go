package space_test

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/space"
)

func inviteLink() string {
	return directory + "/v0/spaces/" + spaceID + "#token=" + base64.RawURLEncoding.EncodeToString(make([]byte, 32)) + "&owner=" + pin().Owner
}
func TestInviteAcceptanceRoundTripAndStrictParsing(t *testing.T) {
	link := inviteLink()
	inv, err := space.ParseInvite(link)
	requireOK(t, err)
	if inv.Base != directory || inv.Space != spaceID || inv.Owner != pin().Owner {
		t.Fatalf("invite fields changed: %+v", inv)
	}
	again, err := space.ParseInvite(inv.String())
	requireOK(t, err)
	if again != inv {
		t.Fatal("invite roundtrip changed pinned values")
	}
	u, err := url.Parse(inv.String())
	requireOK(t, err)
	if u.RawQuery != "" || u.User != nil || !strings.Contains(u.Fragment, "token=") {
		t.Fatal("invite secrets left fragment")
	}
}

func TestInviteAcceptanceRejectsAmbiguousOrInsecureLinks(t *testing.T) {
	link := inviteLink()
	cases := map[string]string{
		"http":            strings.Replace(link, "https:", "http:", 1),
		"userinfo":        strings.Replace(link, "directory.example", "user:pass@directory.example", 1),
		"query":           strings.Replace(link, "#", "?leak=1#", 1),
		"no-fragment":     strings.Split(link, "#")[0],
		"short-space":     strings.Replace(link, spaceID, "abc", 1),
		"uppercase-space": strings.Replace(link, spaceID, strings.ToUpper(spaceID), 1),
		"duplicate-token": link + "&token=" + base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		"duplicate-owner": link + "&owner=" + pin().Owner,
		"unknown-field":   link + "&extra=x",
		"bad-owner":       strings.Replace(link, pin().Owner, "sha256:bad", 1),
		"short-token":     strings.Replace(link, base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "AA", 1),
		"padded-token":    strings.Replace(link, "&owner=", "=&owner=", 1),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) { _, err := space.ParseInvite(value); requireError(t, err) })
	}
}

func TestClientAcceptanceRejectsInvalidDirectoryOrigins(t *testing.T) {
	for _, base := range []string{"http://directory.example", "https://user@directory.example", "https://directory.example/path", "https://directory.example?x=1", "https://directory.example#fragment", "https://"} {
		t.Run(base, func(t *testing.T) {
			_, err := space.NewClient(base, clientKey("node"), space.Options{})
			requireError(t, err)
		})
	}
	_, err := space.NewClient(directory, nil, space.Options{})
	requireError(t, err)
}
