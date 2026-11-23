//go:build !darwin && !linux

package desktop

// followLock elsewhere is per process only: one follower per space within
// this process (enrollments.active) is all that is enforced.
type followLock struct{}

func takeFollow(string) (*followLock, error) { return &followLock{}, nil }

func (*followLock) release() {}
