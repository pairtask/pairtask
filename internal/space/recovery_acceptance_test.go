package space_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestClientAcceptanceDeleteRetryAcceptsStructuredAbsence(t *testing.T) {
	calls := 0
	c := client(t, clientKey("owner"), func(r *http.Request) (*http.Response, error) {
		calls++
		readRequest(t, r)
		return response(404, map[string]string{"error": "not_found"}), nil
	})
	requireOK(t, c.Delete(clientContext, pin()))
	if calls != 1 {
		t.Fatal("delete retry did not check directory")
	}
}

func TestClientAcceptanceDeleteRetryDoesNotHideOtherFailures(t *testing.T) {
	for name, reply := range map[string]func() *http.Response{
		"html": func() *http.Response {
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("<html>Not found</html>"))}
		},
		"forbidden":  func() *http.Response { return response(403, map[string]string{"error": "not_found"}) },
		"other-code": func() *http.Response { return response(404, map[string]string{"error": "unavailable"}) },
		"trailing-data": func() *http.Response {
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(`{"error":"not_found"} trailing`))}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := client(t, clientKey("owner"), func(*http.Request) (*http.Response, error) { return reply(), nil })
			requireError(t, c.Delete(clientContext, pin()))
		})
	}
	// A missing roster still means failure; the delete exception is narrow.
	c := client(t, clientKey("owner"), func(*http.Request) (*http.Response, error) {
		return response(404, map[string]string{"error": "not_found"}), nil
	})
	_, err := c.Members(clientContext, pin())
	requireError(t, err)
}
