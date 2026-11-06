package store_test

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestEpochSchemaRecoveryAcceptanceRestoredTablesKeepTheirIndexes(t *testing.T) {
	for table, index := range map[string]string{
		"peer_messages":    "peer_messages_state",
		"consent_requests": "consent_requests_state",
		"remote_imports":   "remote_imports_pending",
	} {
		t.Run(table, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "recovery.db")
			epochOpen(t, path)
			raw := epochRaw(t, path)
			before := epochIndexColumns(t, raw, index)
			if len(before) == 0 {
				t.Fatal("fresh schema lacks the required index")
			}
			_, err := raw.Exec("DROP TABLE " + epochQuote(table))
			pairOK(t, err)
			epochOpen(t, path)
			epochAssertIndex(t, raw, index, before)
		})
	}
}

func TestEpochSchemaRecoveryAcceptanceLedgerRequiresStateMachineFields(t *testing.T) {
	for _, field := range []string{"role", "kind", "state"} {
		t.Run(field, func(t *testing.T) {
			f := newOperatorContactFixture(t)
			before := epochCount(t, f.raw, "SELECT count(*) FROM pairing_exchanges")
			values := map[string]any{"role": "inviter", "kind": "pair", "state": "open"}
			values[field] = nil
			_, err := f.raw.Exec(`INSERT INTO pairing_exchanges
(operation_id,identity_key,role,kind,transcript_id,begun_mark,state,mode,recorded_at)
VALUES ('incomplete-ledger',?,?,?,?,0,?,'epoch','2026-11-06T00:00:00Z')`,
				f.contact, values["role"], values["kind"], bytes.Repeat([]byte{1}, 32), values["state"])
			if err == nil {
				t.Fatalf("ledger accepted NULL %s", field)
			}
			pairEqual(t, epochCount(t, f.raw, "SELECT count(*) FROM pairing_exchanges"), before)
		})
	}
}
