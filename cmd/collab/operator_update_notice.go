package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/pairtask/pairtask/internal/protocol"
)

const operatorUpdateNoticeUsage = "usage: pairtask operator update-notice set --state STATE --action ACTION [--app-release TAG] [--app-commit SHA] [--host-release TAG] [--available-release TAG] [--available-state STATE] [--scope default|custom] [--checked-at TIME] --observed-at TIME [--socket PATH] [--json] [--timeout 5s]"

// runOperatorUpdateNotice hands the host's update report to the broker for
// the chat. The host's `pairtask version` runs it; every value is an enum, a
// release tag, a commit or a time.
func runOperatorUpdateNotice(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	call, req, code := parseOperatorUpdateNotice(args, stderr)
	if code != -1 {
		return code
	}
	resp, err := call.expect(ctx, req, expectation{protocol.TypeOperatorUpdateNoticeResult, "operator update-notice"}, stderr)
	if err != nil {
		return 1
	}
	if call.asJSON {
		return encodeJSON(stdout, stderr, resp)
	}
	fmt.Fprintln(stdout, "update notice kept for the chat")
	return 0
}

func parseOperatorUpdateNotice(args []string, stderr io.Writer) (operatorCall, protocol.Message, int) {
	var call operatorCall
	if len(args) == 0 || args[0] != "set" {
		fmt.Fprintln(stderr, operatorUpdateNoticeUsage)
		return call, protocol.Message{}, 2
	}
	var notice protocol.UpdateNotice
	flags := flag.NewFlagSet("operator update-notice", flag.ContinueOnError)
	call.register(flags)
	for name, field := range map[string]*string{
		"state": &notice.State, "action": &notice.Action, "app-release": &notice.AppRelease,
		"app-commit": &notice.AppCommit, "host-release": &notice.HostRelease,
		"available-release": &notice.AvailableRelease, "available-state": &notice.AvailableState, "scope": &notice.Scope,
		"checked-at": &notice.CheckedAt, "observed-at": &notice.ObservedAt,
	} {
		flags.StringVar(field, name, "", "")
	}
	code := parseFlags(flags, args[1:], operatorUpdateNoticeUsage, stderr, func(rest []string) error {
		if len(rest) != 0 {
			return errors.New("update-notice set takes no arguments")
		}
		if err := notice.Validate(time.Now()); err != nil {
			return err
		}
		return call.validate()
	})
	req := protocol.Message{Type: protocol.TypeOperatorUpdateNotice, MessageID: uuid.NewString(), UpdateNotice: &notice}
	return call, req, code
}
