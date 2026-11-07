package main

import (
	"testing"
)

const offlinePeer = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

var offlineProtectionSQL = map[string]string{
	"claim":                 `INSERT INTO claims (operator_message_id,task,role,agent_id,topic,state,claimed_at) VALUES (?1,'work','owner','worker','topic','active','2026-11-07T00:00:00Z')`,
	"assignment":            `INSERT INTO assignments (operator_message_id,agent_id,task,state,assigned_at) VALUES (?1,'worker','reply','assigned','2026-11-07T00:00:00Z')`,
	"pending_audit":         `INSERT INTO operator_audit (ts,action,principal_uid,message_id,result) VALUES ('2026-11-07T00:00:00Z','work',0,?1,'pending')`,
	"paired":                `INSERT INTO paired_threads (local_id,contact,root_local_id,peer_id,peer_audience) VALUES (?1,'` + offlinePeer + `',?1,'remote-root','["operator"]')`,
	"peer_linked":           `INSERT INTO peer_messages (contact,direction,message_id,n,created_at,text_bytes,text_hash,hash,state,stored_at,local_id) VALUES ('` + offlinePeer + `','in','remote-message',1,'2026-11-07T00:00:00Z',0,zeroblob(32),zeroblob(32),'received','2026-11-07T00:00:00Z',?1)`,
	"pending_import":        `INSERT INTO remote_imports (contact,remote_id,local_id,sender,recipients,reply_id,reply_author,state,staged_at) VALUES ('` + offlinePeer + `','remote-message',?1,'remote-agent','["worker"]','',0,'pending','2026-11-07T00:00:00Z')`,
	"unsettled_ask":         `INSERT INTO review_asks_out (contact,ask_id,reviewer,requester_agent,local_id,ask,state,created_at,updated_at) VALUES ('` + offlinePeer + `','ask','reviewer','worker',?1,'{}','requested','2026-11-07T00:00:00Z','2026-11-07T00:00:00Z')`,
	"legacy_unacknowledged": `UPDATE message_receipts SET agent_acknowledged_at=NULL WHERE message_id=?1`,
}

func (f *offlineInboxFixture) protect(t *testing.T, reason, id string) {
	t.Helper()
	f.exec(t, `INSERT INTO peer_contacts (identity_key,name,destination,established_at,joined,invited) VALUES (?,'Peer','peer:7445','2026-11-07T00:00:00Z',1,0)`, offlinePeer)
	query, ok := offlineProtectionSQL[reason]
	if !ok {
		t.Fatalf("no fixture for %s", reason)
	}
	f.exec(t, query, id)
}
