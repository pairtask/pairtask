//go:build linux

package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

type unpairOperatorFixture struct {
	*redeemOperatorFixture
	key string
}

func newUnpairOperatorFixture(t *testing.T) *unpairOperatorFixture {
	t.Helper()
	cfg := redeemConfig()
	cfg.MessageTick = make(chan time.Time)
	f := newRedeemOperator(t, cfg, "")
	id := messageIdentity(t)
	messageConfirm(f, id, 1)
	return &unpairOperatorFixture{f, id.Fingerprint()}
}
func (f *unpairOperatorFixture) request(t *testing.T, id string, epoch int64) protocol.Message {
	t.Helper()
	b, err := json.Marshal(map[string]any{"type": "operator_contact", "contact_op": "unpair", "contact_key": f.key, "contact_confirm": f.key, "contact_epoch": epoch, "message_id": id})
	operatorNoError(t, err)
	var m protocol.Message
	operatorNoError(t, json.Unmarshal(b, &m))
	return m
}
func (f *unpairOperatorFixture) fields(t *testing.T, m protocol.Message) map[string]json.RawMessage {
	t.Helper()
	b, err := json.Marshal(m)
	operatorNoError(t, err)
	var fields map[string]json.RawMessage
	operatorNoError(t, json.Unmarshal(b, &fields))
	return fields
}
func (f *unpairOperatorFixture) rootUnpair(t *testing.T) {
	t.Helper()
	audit, err := f.st.RecordOperatorAudit(context.Background(), store.OperatorAudit{Action: "contact unpair", MessageID: "fixture-unpair", Scope: f.key, Result: store.AuditPending})
	operatorNoError(t, err)
	_, _, err = f.st.UnpairContact(context.Background(), store.ContactUnpair{OperationID: "fixture-unpair", Contact: f.key, Confirm: f.key, SeenEpoch: 1, AuditID: audit, At: time.Now()})
	operatorNoError(t, err)
}
