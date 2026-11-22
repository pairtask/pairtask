package hub

import (
	"context"
	"encoding/json"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"strings"
	"testing"
)

func TestMapPublishRetryReturnsStoredReceipt(t *testing.T) {
	f := newRetryFixture(t)
	body := mapPut{Put: space.MapPut{Map: f.m}}
	req := protocol.SpaceRequest{Op: protocol.SpaceOpMapPublish, Space: f.pin.Space, Directory: f.pin.Base}
	messageWorkerOK(t, runMapOp(t, f.h, req, body))
	// The CLI lost the first reply. Neither retry should produce another PUT,
	// and new invite metadata on the second retry must have no effect.
	checkPublishReceipt(t, f, req, body)
	digest := strings.Repeat("ab", 32)
	body.Put.Applied = []string{digest}
	body.Invites = []mapConsumed{{TokenHash: strings.Repeat("cd", 32), Change: digest}}
	checkPublishReceipt(t, f, req, body)
	if len(f.d.puts) != 1 {
		t.Fatalf("directory received %d PUTs, want 1", len(f.d.puts))
	}
}
func checkPublishReceipt(t *testing.T, f retryFixture, req protocol.SpaceRequest, body mapPut) {
	t.Helper()
	ctx := context.Background()
	record, err := json.Marshal(body)
	messageWorkerOK(t, err)
	req.Record = record
	step, refusal := f.h.mapPublish(ctx, req)
	if refusal != nil {
		t.Fatal(refusal)
	}
	result := step.now
	if step.job != nil {
		result, err = step.job(ctx, ctx)
		messageWorkerOK(t, err)
	}
	if result == nil || result.Mark == nil {
		t.Fatal("publish retry omitted the receipt")
	}
	if *result.Mark != *markView(f.m.Mark()) {
		t.Fatalf("unexpected receipt %+v", result.Mark)
	}
}
