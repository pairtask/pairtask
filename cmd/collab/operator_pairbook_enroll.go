package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/enroll"
	"github.com/pairtask/pairtask/internal/personkey"
)

// Joining an organization space from the host: enroll this workspace, show
// the person where to confirm it, and wait until the organization admits
// it. internal/enroll does the work; this is the command's words.

// enrollPoll is how often the enrollment is checked; enrollWait is how long
// the command waits for an admin before keeping the enrollment to resume.
var (
	enrollPoll = 2 * time.Second
	enrollWait = 30 * time.Minute
)

// pairbookEnroll joins the organization space link names: it picks up an
// enrollment this host left waiting for an admin, or starts one.
func pairbookEnroll(ctx context.Context, s *pairbookSession, a pairbookArgs, link string) (any, error) {
	name, err := joinName(a.name) // a kept enrollment keeps its own name
	if err != nil {
		return nil, err
	}
	f := &enroll.Flow{Call: s.space, Person: s.person, Link: link, Saves: enroll.Saves(s.enrollSaves()), Poll: enrollPoll, Wait: enrollWait}
	st, err := f.Begin(ctx, name)
	if err != nil {
		return nil, err
	}
	announce(s.progressOut(), f)
	out, err := f.Follow(ctx, st)
	return enrollAnswer(f, out, err)
}

// announce tells the person where to confirm, and which machine.
func announce(w io.Writer, f *enroll.Flow) {
	if f.Resume {
		fmt.Fprintf(w, "Picking up the enrollment started earlier (code %s).\n", f.Enroll.Code)
		return
	}
	fmt.Fprintf(w, "To join, open %s\n", f.Enroll.URL)
	fmt.Fprintf(w, "sign in with your company account, and check this machine's fingerprint: %s\n", f.Fingerprint())
	fmt.Fprintf(w, "Code: %s (valid until %s)\n", f.Enroll.Code, f.Enroll.ExpiresAt.Local().Format("15:04"))
	openBrowser(f)
}

func enrollAnswer(f *enroll.Flow, out enroll.Outcome, err error) (any, error) {
	switch {
	case errors.Is(err, enroll.ErrExpired):
		return nil, errors.New("the enrollment expired; run pairtask space join again")
	case err != nil:
		return nil, err
	case out == enroll.Waiting:
		return done("Confirmed; an admin hasn't approved yet. After they do, run this same pairtask space join command again to finish."), nil
	}
	return done("Joined " + f.Enroll.Space + " as " + f.Name + ". Members connect within minutes: pairtask space status shows it."), nil
}

// openBrowser opens the enrollment's URL on a desktop, best effort; the
// URL is printed either way.
func openBrowser(f *enroll.Flow) { openBrowserURL(f.Enroll.URL) }

// openBrowserURL opens an https URL on a desktop, best effort.
func openBrowserURL(url string) {
	if !strings.HasPrefix(url, "https://") {
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return
	}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	_ = cmd.Start()
}

// enrollSaves is the folder, beside the person's keys, that keeps
// enrollments waiting for an admin; "" keeps none.
type enrollSaves string

func (s *pairbookSession) enrollSaves() enrollSaves {
	if s.enrollDir != "" {
		return enrollSaves(s.enrollDir)
	}
	dir, err := personkey.ResolveDir()
	if err != nil {
		return ""
	}
	return enrollSaves(filepath.Join(dir, "enrollments"))
}

func (d enrollSaves) path(space string) string { return enroll.Saves(d).Path(space) }

func (d enrollSaves) load(space string) (enroll.Saved, bool) { return enroll.Saves(d).Load(space) }
