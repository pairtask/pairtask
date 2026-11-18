package main

import (
	"context"
	"fmt"
	"io"
)

const operatorSpaceUsage = `usage: pairtask space COMMAND [--socket PATH] [--json]
  list
  create --directory URL (or --pairbook URL) [--name NAME] [--id ID]
  invite   [--space ID] [--valid 24h]
  join LINK [--name NAME]
  requests [--space ID]
  approve FP [--space ID] [--valid 2160h]
  members  [--space ID]
  publish HOST:PORT... [--space ID] [--valid 24h]
  revoke FP [--space ID]
  leave    [--space ID]
  delete   [--space ID]
--space may be left out when exactly one space is pinned.`

// runOperatorSpace runs a Pairbook space command (pairtask space, or
// pairtask operator space): one operator_space request to the broker.
func runOperatorSpace(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, "pairtask space: not implemented")
	fmt.Fprintln(stderr, operatorSpaceUsage)
	return 2
}
