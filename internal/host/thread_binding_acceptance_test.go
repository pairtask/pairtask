package host

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestThreadBindingAcceptanceOnlySuccessfulOwnedLifecycleIsRecorded(t *testing.T) {
	for _, method := range []string{"thread/start", "thread/resume", "thread/fork"} {
		t.Run(method, func(t *testing.T) {
			p, up, down := proxyFixture(t)
			p.RuntimeMCP = map[string]any{"command": "/launcher"}
			var captured []Thread
			p.OnThread = func(thread Thread) {
				if p.ThreadID() != thread.ID {
					t.Error("callback observed the wrong owned thread")
				}
				captured = append(captured, thread)
			}
			request := Frame{ID: json.RawMessage(`1`), Method: method, Params: json.RawMessage(`{"cwd":"/requested","threadId":"parent"}`)}
			check(t, p.FromOperator(context.Background(), request))
			frameAt(t, up)
			threadBindingResponse(t, p, down, Frame{ID: json.RawMessage(`99`), Result: json.RawMessage(`{"thread":{"id":"foreign","cwd":"/foreign"}}`)})
			threadBindingResponse(t, p, down, Frame{ID: request.ID, Error: json.RawMessage(`{"code":-1,"message":"failed"}`)})
			if len(captured) != 0 {
				t.Fatal("unrelated or failed response recorded a binding")
			}
			request.ID = json.RawMessage(`2`)
			check(t, p.FromOperator(context.Background(), request))
			frameAt(t, up)
			response := Frame{ID: request.ID, Result: json.RawMessage(`{"thread":{"id":"owned","cwd":"/actual"}}`)}
			threadBindingResponse(t, p, down, response)
			threadBindingResponse(t, p, down, response)
			if len(captured) != 1 || captured[0].ID != "owned" || captured[0].Cwd != "/actual" {
				t.Fatalf("want one actual owned thread binding, got %+v", captured)
			}
		})
	}
}

func threadBindingResponse(t *testing.T, p *Proxy, down <-chan Frame, response Frame) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.FromHost(context.Background(), response) }()
	select {
	case err := <-done:
		check(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("thread callback deadlocked while reading ThreadID")
	}
	frameAt(t, down)
}

func TestThreadBindingAcceptanceFallsBackToRequestedCwd(t *testing.T) {
	p, up, down := proxyFixture(t)
	p.RuntimeMCP = map[string]any{"command": "/launcher"}
	var got Thread
	p.OnThread = func(thread Thread) { got = thread }
	check(t, p.FromOperator(context.Background(), Frame{ID: json.RawMessage(`1`), Method: "thread/resume", Params: json.RawMessage(`{"cwd":"/override","threadId":"saved"}`)}))
	frameAt(t, up)
	threadBindingResponse(t, p, down, Frame{ID: json.RawMessage(`1`), Result: json.RawMessage(`{"thread":{"id":"saved"}}`)})
	if got.Cwd != "/override" || got.ID != "saved" {
		t.Fatalf("request cwd fallback lost: %+v", got)
	}
}
