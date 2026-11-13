package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type inboxCLIRequest struct {
	Type         string   `json:"type"`
	MessageID    string   `json:"message_id"`
	MessageIDs   []string `json:"message_ids"`
	InReplyTo    string   `json:"in_reply_to"`
	AckSelection struct {
		From  string `json:"from"`
		After uint64 `json:"after_seq"`
		Upper uint64 `json:"upper_seq"`
		Limit int    `json:"limit"`
	} `json:"ack_selection"`
	PageRequest struct {
		After uint64 `json:"after_seq"`
		Upper uint64 `json:"upper_seq"`
		Limit int    `json:"limit"`
	} `json:"page_request"`
}

func inboxCLISocket(t *testing.T, reply func(inboxCLIRequest) any) (string, <-chan inboxCLIRequest) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "inbox-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "op.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan inboxCLIRequest, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			serveInboxCLI(c, requests, reply)
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("fake operator did not stop")
		}
	})
	return path, requests
}
func serveInboxCLI(c net.Conn, requests chan<- inboxCLIRequest, reply func(inboxCLIRequest) any) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	var req inboxCLIRequest
	if json.NewDecoder(bufio.NewReader(c)).Decode(&req) != nil {
		return
	}
	requests <- req
	json.NewEncoder(c).Encode(reply(req))
}
func inboxCLIPage() map[string]any {
	return map[string]any{"type": "operator_page", "page": map[string]any{"messages": []any{map[string]any{"type": "msg", "message_id": "inbox-1", "seq": 10, "from": "author", "to": "operator", "session_id": "original", "origin": "agent", "durable": true, "ack_requested": true, "payload": map[string]string{"text": "hello"}}}, "next_after_seq": 10, "upper_seq": 10, "more": false}}
}
func inboxCLIReply(req inboxCLIRequest) any {
	if req.Type == "operator_receive" {
		return inboxCLIPage()
	}
	return map[string]any{"type": "ack", "stage": "adapter_received", "receipts": []any{map[string]string{"message_id": "inbox-1", "stage": "adapter_received"}}}
}
func takeInboxCLIRequest(t *testing.T, requests <-chan inboxCLIRequest) inboxCLIRequest {
	t.Helper()
	select {
	case r := <-requests:
		return r
	case <-time.After(time.Second):
		t.Fatal("missing operator request")
		return inboxCLIRequest{}
	}
}
func assertNoInboxCLIRequest(t *testing.T, requests <-chan inboxCLIRequest) {
	t.Helper()
	select {
	case r := <-requests:
		t.Fatalf("unexpected receipt request: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
}
func TestOperatorInboxCLIReceiveThenReceived(t *testing.T) {
	path, requests := inboxCLISocket(t, inboxCLIReply)
	var out, stderr bytes.Buffer
	args := []string{"operator", "receive", "--socket", path, "--after", "4", "--upper", "10", "--limit", "2", "--json", "--timeout", "1s"}
	if code := run(context.Background(), args, &out, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !json.Valid(out.Bytes()) {
		t.Fatalf("invalid JSON: %s", out.String())
	}
	r := takeInboxCLIRequest(t, requests)
	assertStatusEqual(t, "operation", r.Type, "operator_receive")
	assertStatusEqual(t, "after", r.PageRequest.After, uint64(4))
	assertStatusEqual(t, "upper", r.PageRequest.Upper, uint64(10))
	assertStatusEqual(t, "limit", r.PageRequest.Limit, 2)
	receipt := takeInboxCLIRequest(t, requests)
	assertStatusEqual(t, "receipt operation", receipt.Type, "operator_received")
	assertStatusEqual(t, "receipt count", len(receipt.MessageIDs), 1)
	assertStatusEqual(t, "receipt ID", receipt.MessageIDs[0], "inbox-1")
}

type inboxBrokenOutput struct{}

func (inboxBrokenOutput) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }
func TestOperatorInboxCLIOutputFailureDoesNotReceipt(t *testing.T) {
	path, requests := inboxCLISocket(t, inboxCLIReply)
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "receive", "--socket", path, "--json"}, inboxBrokenOutput{}, &stderr)
	assertStatusEqual(t, "output failure exit", code, 1)
	assertStatusEqual(t, "first op", takeInboxCLIRequest(t, requests).Type, "operator_receive")
	assertNoInboxCLIRequest(t, requests)
}
func TestOperatorInboxCLIFailedReceiptPreservesOutput(t *testing.T) {
	path, requests := inboxCLISocket(t, func(r inboxCLIRequest) any {
		if r.Type == "operator_receive" {
			return inboxCLIPage()
		}
		return map[string]string{"type": "error", "code": "internal", "detail": "receipt unavailable"}
	})
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "receive", "--socket", path, "--json"}, &out, &stderr)
	assertStatusEqual(t, "receipt failure exit", code, 1)
	if !strings.Contains(out.String(), "inbox-1") {
		t.Fatal("fetched page lost on receipt failure")
	}
	if !strings.Contains(strings.ToLower(stderr.String()), "receipt") {
		t.Fatalf("receipt failure not distinguished: %s", stderr.String())
	}
	takeInboxCLIRequest(t, requests)
	assertStatusEqual(t, "receipt attempted", takeInboxCLIRequest(t, requests).Type, "operator_received")
}
func TestOperatorInboxCLIRejectsInvalidPageBeforeOutput(t *testing.T) {
	path, requests := inboxCLISocket(t, func(inboxCLIRequest) any {
		p := inboxCLIPage()
		p["page"].(map[string]any)["next_after_seq"] = 9
		return p
	})
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "receive", "--socket", path, "--json"}, &out, &stderr)
	assertStatusEqual(t, "invalid page exit", code, 1)
	assertStatusEqual(t, "invalid page output", out.Len(), 0)
	takeInboxCLIRequest(t, requests)
	assertNoInboxCLIRequest(t, requests)
}
func TestOperatorInboxCLIAckWire(t *testing.T) {
	path, requests := inboxCLISocket(t, func(r inboxCLIRequest) any {
		return map[string]string{"type": "ack", "stage": "agent_acknowledged", "message_id": r.MessageID}
	})
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "ack", "--socket", path, "--json", "--timeout", "1s", "inbox-1"}, &out, &stderr)
	assertStatusEqual(t, "ack exit", code, 0)
	r := takeInboxCLIRequest(t, requests)
	assertStatusEqual(t, "ack type", r.Type, "operator_ack")
	assertStatusEqual(t, "ack ID", r.MessageID, "inbox-1")
	if !json.Valid(out.Bytes()) {
		t.Fatal("ack output is not JSON")
	}
}
func TestOperatorInboxCLISendReplyCorrelation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path, requests := inboxCLISocket(t, func(r inboxCLIRequest) any {
		return map[string]any{"type": "ack", "stage": "accepted", "message_id": r.MessageID, "seq": 11}
	})
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "send", "--socket", path, "--to", "author", "--in-reply-to", "inbox-1", "answer"}, &out, &stderr)
	assertStatusEqual(t, "send reply exit", code, 0)
	assertStatusEqual(t, "reply correlation", takeInboxCLIRequest(t, requests).InReplyTo, "inbox-1")
}
func TestOperatorInboxCLIInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{"receive", "--limit", "33"}, {"receive", "--limit", "-1"}, {"receive", "--after", "-1"}, {"receive", "--upper", "bad"}, {"receive", "unexpected"}, {"ack"}, {"ack", "one", "two"}, {"ack", "--timeout", "0s", "id"}} {
		var out, stderr bytes.Buffer
		code := run(context.Background(), append([]string{"operator"}, args...), &out, &stderr)
		assertStatusEqual(t, "argument exit", code, 2)
		assertStatusEqual(t, "no argument-error stdout", out.Len(), 0)
	}
}
func TestOperatorInboxCLILegacyErrorPreservesReason(t *testing.T) {
	path, _ := inboxCLISocket(t, func(inboxCLIRequest) any {
		return map[string]string{"type": "error", "code": "malformed_frame", "detail": "invalid cursor"}
	})
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "receive", "--socket", path}, io.Discard, &stderr)
	assertStatusEqual(t, "legacy/error exit", code, 1)
	if !strings.Contains(stderr.String(), "invalid cursor") {
		t.Fatal("broker reason hidden behind unsupported assertion")
	}
}

func TestOperatorInboxCLIRejectsImpossiblePages(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"exceeds requested limit", func(p map[string]any) { p["messages"] = []any{inboxCLIMessage("one", 9), inboxCLIMessage("two", 10)} }},
		{"duplicate IDs", func(p map[string]any) { p["messages"] = []any{inboxCLIMessage("same", 9), inboxCLIMessage("same", 10)} }},
		{"empty page with more", func(p map[string]any) { p["messages"] = []any{}; p["next_after_seq"] = 0; p["more"] = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { checkImpossibleInboxPage(t, tc.name, tc.mutate) })
	}
}
func inboxCLIMessage(id string, seq int) map[string]any {
	return map[string]any{"type": "msg", "message_id": id, "seq": seq, "from": "author", "to": "operator", "origin": "agent", "durable": true, "ack_requested": true, "payload": map[string]string{"text": "hello"}}
}
func checkImpossibleInboxPage(t *testing.T, name string, mutate func(map[string]any)) {
	t.Helper()
	path, requests := inboxCLISocket(t, func(r inboxCLIRequest) any { p := inboxCLIPage(); mutate(p["page"].(map[string]any)); return p })
	limit := "2"
	if name == "exceeds requested limit" {
		limit = "1"
	}
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "receive", "--socket", path, "--limit", limit, "--json"}, &out, &stderr)
	assertStatusEqual(t, "invalid page exit", code, 1)
	assertStatusEqual(t, "invalid page not printed", out.Len(), 0)
	takeInboxCLIRequest(t, requests)
	assertNoInboxCLIRequest(t, requests)
}
func TestOperatorInboxCLIValidatesReceiptResponse(t *testing.T) {
	for _, response := range []any{
		map[string]string{"type": "ack", "stage": "accepted"},
		map[string]any{"type": "ack", "stage": "adapter_received", "receipts": []any{}},
		map[string]any{"type": "ack", "stage": "adapter_received", "receipts": []any{map[string]string{"message_id": "wrong-id", "stage": "adapter_received"}}},
	} {
		checkInvalidInboxReceipt(t, response)
	}
}
func checkInvalidInboxReceipt(t *testing.T, response any) {
	t.Helper()
	path, _ := inboxCLISocket(t, func(r inboxCLIRequest) any {
		if r.Type == "operator_receive" {
			return inboxCLIPage()
		}
		return response
	})
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "receive", "--socket", path, "--json"}, &out, &stderr)
	assertStatusEqual(t, "invalid receipt response exit", code, 1)
	if !strings.Contains(out.String(), "inbox-1") {
		t.Fatal("receipt response failure lost fetched page")
	}
}
func TestOperatorInboxCLIValidatesHumanAckResponse(t *testing.T) {
	for _, response := range []any{map[string]string{"type": "ack", "stage": "adapter_received", "message_id": "inbox-1"}, map[string]string{"type": "ack", "stage": "agent_acknowledged", "message_id": "wrong-id"}} {
		path, _ := inboxCLISocket(t, func(inboxCLIRequest) any { return response })
		var out, stderr bytes.Buffer
		code := run(context.Background(), []string{"operator", "ack", "--socket", path, "inbox-1"}, &out, &stderr)
		assertStatusEqual(t, "invalid human ack response exit", code, 1)
	}
}
