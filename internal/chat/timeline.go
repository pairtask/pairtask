package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Receipt states of a received message in this chat.
const (
	ReceiptNone      = ""
	ReceiptPending   = "pending"
	ReceiptConfirmed = "confirmed"
	ReceiptFailed    = "failed"
)

// entry is one rendered message. Bodies are kept across resyncs; metadata is
// reset to each new baseline.
type entry struct {
	msg             protocol.Message
	direction       string
	omitted         bool
	hash            string
	receipt         string
	operatorStage   string
	stages          map[string]stageState
	stagesTruncated bool
	conflict        bool
	gapBelow        bool
	gapUpper        uint64 // the upper_seq of the catch-up that left the gap
	size            int
	epoch           string // the broker epoch this entry was last read in
	unread          bool   // arrived and not yet shown while following the bottom
	drawings        *diagramRenders
	// The entry's last layouts, for one width each, so a redraw or scroll
	// reformats only what changed (#180). They live and are evicted with the
	// entry, and like the drawings they aren't counted in the timeline's bytes.
	body   *bodyLayout   // see bodyAt
	header *headerLayout // see headerAt
	status *statusLayout // see statusAt
}

// stageState is a current delivery state with the watermark it was read at.
type stageState struct {
	protocol.RecipientStage
	wm protocol.Watermark
}

type timeline struct {
	entries  map[string]*entry
	order    []*entry // ascending seq
	bytes    int
	selected int
	paused   bool
}

func newTimeline() timeline {
	return timeline{entries: map[string]*entry{}, selected: -1}
}

func bodyHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func newEntry(msg protocol.Message, direction string, omitted bool) *entry {
	msg.SessionID = ""
	e := &entry{msg: msg, direction: direction, omitted: omitted, hash: bodyHash(msg.Payload), stages: map[string]stageState{}}
	e.size = len(msg.Payload) + len(msg.MessageID) + 256
	return e
}

// upsert inserts a message, or merges it into the known entry with that ID.
func (t *timeline) upsert(fresh *entry) (*entry, bool) {
	if known, ok := t.entries[fresh.msg.MessageID]; ok {
		t.merge(known, fresh)
		return known, false
	}
	t.insert(fresh)
	return fresh, true
}

// merge completes an omitted body, or flags a conflict when the same ID
// arrives with different content; rendered content is never rewritten.
func (t *timeline) merge(known, fresh *entry) {
	switch {
	case fresh.omitted:
	case known.omitted:
		t.bytes += fresh.size - known.size
		known.msg.Payload, known.omitted, known.hash, known.size = fresh.msg.Payload, false, fresh.hash, fresh.size
	case known.hash != fresh.hash:
		known.conflict = true
	}
}

// insert places a new entry in seq order, keeping the selection on its entry.
func (t *timeline) insert(fresh *entry) {
	t.entries[fresh.msg.MessageID] = fresh
	t.bytes += fresh.size
	i := sort.Search(len(t.order), func(i int) bool { return t.order[i].msg.Seq > fresh.msg.Seq })
	t.order = append(t.order, nil)
	copy(t.order[i+1:], t.order[i:])
	t.order[i] = fresh
	if t.selected >= i && t.selected >= 0 {
		t.selected++
	}
}

// sent returns the sent entry with id, if any.
func (t *timeline) sent(id string) *entry {
	if e := t.entries[id]; e != nil && e.direction == "sent" {
		return e
	}
	return nil
}

// fits reports whether one more entry of size fits after evicting old,
// unpinned history. Eviction never acknowledges and can be paged in again.
func (t *timeline) fits(size int, b Bounds, pinned func(*entry) bool) bool {
	return t.grow(1, size, b, pinned)
}

// grow reports whether entries more entries and size more bytes fit after
// evicting old, unpinned history.
func (t *timeline) grow(entries, size int, b Bounds, pinned func(*entry) bool) bool {
	for len(t.order)+entries > b.Entries || t.bytes+size > b.Bytes {
		if !t.evictOldest(pinned) {
			return false
		}
	}
	return true
}

func (t *timeline) evictOldest(pinned func(*entry) bool) bool {
	for i, e := range t.order {
		if pinned(e) {
			continue
		}
		t.order = append(t.order[:i], t.order[i+1:]...)
		delete(t.entries, e.msg.MessageID)
		t.bytes -= e.size
		if t.selected > i {
			t.selected--
		} else if t.selected == i {
			t.selected = -1
		}
		return true
	}
	return false
}

func (t *timeline) oldestSeq() uint64 {
	if len(t.order) == 0 {
		return 0
	}
	return t.order[0].msg.Seq
}

func (t *timeline) selectedEntry() *entry {
	if t.selected < 0 || t.selected >= len(t.order) {
		return nil
	}
	return t.order[t.selected]
}

// atBottom reports whether the selection follows the newest message.
func (t *timeline) atBottom() bool {
	return t.selected < 0 || t.selected >= len(t.order)-1
}

// newer reports whether wm supersedes a stored watermark: a new epoch always
// does, otherwise only a higher revision.
func newer(wm, stored protocol.Watermark) bool {
	if wm.Epoch != stored.Epoch {
		return true
	}
	return wm.Revision > stored.Revision
}

// applyStage records a recipient stage read at wm unless newer state is known.
func (e *entry) applyStage(st protocol.RecipientStage, wm protocol.Watermark) {
	known, ok := e.stages[st.AgentID]
	if ok && !newer(wm, known.wm) {
		return
	}
	e.stages[st.AgentID] = stageState{RecipientStage: st, wm: wm}
}

// resetStages marks stages unknown at a new baseline; fresher reads refill them.
func (t *timeline) resetStages() {
	for _, e := range t.order {
		for id, st := range e.stages {
			st.Stage = "unknown"
			st.wm = protocol.Watermark{}
			e.stages[id] = st
		}
	}
}
