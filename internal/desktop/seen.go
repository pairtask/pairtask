package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// Seen marks: how far each consumer of this node's inbox has been shown
// each workspace's received messages. "Seen" is not "read": a mark moves
// when check_inbox hands new messages to a consumer, sends no receipt and
// changes no message, and survives a restart and a context compaction that
// the consumer's own memory wouldn't.
//
// Several copies of the node share one data directory (a chat, Cowork and
// Code each start one), so nothing is cached: every get and advance takes
// an exclusive lock on a file that is never replaced, reads the marks
// fresh, and replaces them atomically.

const (
	seenName = "seen.json"
	seenLock = "seen.lock"
)

// seenMarks are the marks kept in one data directory.
type seenMarks struct{ dir string }

// seenFile is the stored form: consumer, then workspace key, then the
// cursor of the last message shown.
type seenFile struct {
	Consumers map[string]map[string]int64 `json:"consumers"`
}

// of is every mark of one consumer, by workspace key.
func (s seenMarks) of(consumer string) (map[string]int64, error) {
	marks := map[string]int64{}
	err := s.locked(func(f *seenFile) bool {
		for contact, cursor := range f.Consumers[consumer] {
			marks[contact] = cursor
		}
		return false
	})
	return marks, err
}

// advance moves a consumer's mark for contact to cursor, never back, and
// answers where it now stands; the move is durable before it returns.
func (s seenMarks) advance(consumer, contact string, cursor int64) (int64, error) {
	at := cursor
	err := s.locked(func(f *seenFile) bool {
		if held := f.Consumers[consumer][contact]; held >= cursor {
			at = held
			return false
		}
		if f.Consumers[consumer] == nil {
			f.Consumers[consumer] = map[string]int64{}
		}
		f.Consumers[consumer][contact] = cursor
		return true
	})
	return at, err
}

// locked runs change on the marks as they are on disk, under the lock
// every copy takes, and stores them when change says they changed.
func (s seenMarks) locked(change func(*seenFile) bool) error {
	lock, err := os.OpenFile(filepath.Join(s.dir, seenLock), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close() // closing releases the lock
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	state, err := s.read()
	if err != nil {
		return err
	}
	if !change(state) {
		return nil
	}
	return s.write(state)
}

// read is the stored marks, empty when none were stored. Marks that can't
// be read are an error, never taken as "nothing seen".
func (s seenMarks) read() (*seenFile, error) {
	state := &seenFile{Consumers: map[string]map[string]int64{}}
	data, err := os.ReadFile(filepath.Join(s.dir, seenName))
	if errors.Is(err, fs.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, state); err != nil {
		return nil, fmt.Errorf("the seen marks in %s can't be read: %w", seenName, err)
	}
	if state.Consumers == nil {
		state.Consumers = map[string]map[string]int64{}
	}
	return state, nil
}

// write replaces the stored marks: a synced temporary file, renamed over
// them, in a synced directory.
func (s seenMarks) write(state *seenFile) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, seenName+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // nothing left behind when a step fails
	if err := writeSynced(tmp, data); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(s.dir, seenName)); err != nil {
		return err
	}
	return syncDir(s.dir)
}

func writeSynced(f *os.File, data []byte) error {
	_, err := f.Write(data)
	return errors.Join(err, f.Sync(), f.Close())
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
