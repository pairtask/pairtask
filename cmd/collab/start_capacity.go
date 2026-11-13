package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// An agent session writes into its home as it starts: settings, session
// files, the shell prompt's cache. When the home volume is full that write
// fails somewhere unrelated (#371), so start checks the space first and
// says what fills it and what is safe to clear.

const (
	// capacityRefuse is the free space below which start refuses: too
	// little for a session to write its first files.
	capacityRefuse = 64 << 20
	// capacityWarn is the free space below which start warns and goes on.
	capacityWarn = 256 << 20
	// cacheSizing bounds how long start measures the caches it names.
	cacheSizing = 2 * time.Second
)

// homeSpace is how full the agent's home is.
type homeSpace struct {
	Path        string
	Free, Total uint64
}

// capacityFunc reports the agent home's space; nil skips the check.
type capacityFunc func() (homeSpace, error)

// homeCapacity is the space left on the file system holding home.
func homeCapacity() (homeSpace, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return homeSpace{}, err
	}
	var st unix.Statfs_t
	if err := unix.Statfs(home, &st); err != nil {
		return homeSpace{}, err
	}
	bsize := uint64(st.Bsize) // a block size is never negative
	return homeSpace{Path: home, Free: st.Bavail * bsize, Total: st.Blocks * bsize}, nil
}

// disposableCaches are the build caches under home that are safe to clear,
// each with the command that clears it.
var disposableCaches = []struct{ dir, clear string }{
	{".cache/go-build", "go clean -cache"},
	{"go/pkg/mod", "go clean -modcache"},
	{".npm", "npm cache clean --force"},
	{".cache/pip", "pip cache purge"},
	{".cache/uv", "uv cache clean"},
	{".cache/starship", "rm -rf ~/.cache/starship"},
}

// checkCapacity refuses to start when home has too little space left and
// warns when it runs low; a check that can't run doesn't stop the start.
func checkCapacity(rt startRuntime) int {
	if reportCapacity(rt.Capacity, rt.Stderr, "pairtask start") {
		return 1
	}
	return 0
}

// runHomeCheck is `pairtask home-check`, run as an agent's shell starts:
// the same report as start's, which never fails the shell.
func runHomeCheck(stderr io.Writer) int {
	reportCapacity(homeCapacity, stderr, "pairtask")
	return 0
}

// reportCapacity writes what's wrong when home is low on space, and
// reports whether it's too low to start a session.
func reportCapacity(capacity capacityFunc, w io.Writer, who string) bool {
	if capacity == nil {
		return false
	}
	space, err := capacity()
	if err != nil || space.Free >= capacityWarn {
		return false
	}
	full := space.Free < capacityRefuse
	lead := who + ": warning: the agent home is nearly full"
	if full {
		lead = who + ": the agent home is full, so an agent session can't start"
	}
	fmt.Fprintf(w, "%s: %s free of %s in %s.\n", lead, size(space.Free), size(space.Total), space.Path)
	writeRecovery(w, space.Path)
	return full
}

// writeRecovery names the caches that hold space and how to clear each,
// then how to grow the volume. Nothing is deleted here.
func writeRecovery(w io.Writer, home string) {
	ctx, cancel := context.WithTimeout(context.Background(), cacheSizing)
	defer cancel()
	fmt.Fprintln(w, "  Build caches are safe to clear; they refill as needed:")
	for _, c := range disposableCaches {
		if used := dirSize(ctx, filepath.Join(home, c.dir)); used > 0 {
			fmt.Fprintf(w, "    ~/%s  %s  →  %s\n", c.dir, size(used), c.clear)
		}
	}
	fmt.Fprintln(w, "  Keep ~/.codex, ~/.claude, ~/.pi and ~/.local/state: they hold the agents' settings and sessions.")
	fmt.Fprintln(w, "  To grow the volume, set agent_home_gib in the sandbox settings on the host, then pairtask plan and pairtask apply.")
}

// dirSize is the bytes of regular files under dir, as far as ctx allows;
// 0 when it doesn't exist.
func dirSize(ctx context.Context, dir string) uint64 {
	var total uint64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return fs.SkipAll
		}
		if info, ierr := d.Info(); ierr == nil && info.Mode().IsRegular() {
			total += uint64(info.Size()) // a file size is never negative
		}
		return nil
	})
	return total
}

// size is n bytes for people: MiB below a GiB, else GiB with one decimal.
func size(n uint64) string {
	if n < 1<<30 {
		return fmt.Sprintf("%d MiB", n>>20)
	}
	return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
}
