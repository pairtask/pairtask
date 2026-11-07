//go:build linux

package server

import (
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestReplyableNoticeAcceptanceRefusalExplainsItsOwnNoticeKind(t *testing.T) {
	f := newOutcomeNoticeServer(t)
	_, err := f.db.Exec(`UPDATE remote_imports SET remote_id='refused·ask' WHERE local_id=?`, f.notice.MessageID)
	operatorNoError(t, err)
	before := f.snapshot()
	f.agent.write(outcomeNoticeReply(f.notice.MessageID, f.key))
	response := f.agent.read()
	operatorEqual(t, "refusal notice still refused", response.Code, protocol.ErrMalformedFrame)
	operatorEqual(t, "reason identifies refusal notice", strings.Contains(response.Detail, "refusal notice"), true)
	operatorEqual(t, "reason doesn't reject supported outcome replies", strings.Contains(response.Detail, "review outcome notice"), false)
	outcomeNoticeUnchanged(f, before)
}
