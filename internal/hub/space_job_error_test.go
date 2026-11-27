package hub

import (
	"errors"
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestSpaceJobErrorDistinguishesDNSFromRefusal(t *testing.T) {
	request := &url.Error{Op: "Post", URL: "https://directory.example/enrollments", Err: &net.DNSError{Name: "directory.example", Err: "server misbehaving"}}
	got := spaceJobError(request)
	if got.code != protocol.ErrSpaceUnreachable || !strings.Contains(got.detail, "directory.example") {
		t.Fatal(got)
	}
	if got := spaceJobError(errors.New("approval denied")); got.code != protocol.ErrSpace || got.detail != "approval denied" {
		t.Fatal(got)
	}
}
