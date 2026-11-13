package desktop

import (
	"context"
	"os/exec"
	"runtime"
)

// Notifier shows one system notification: a title and a line of text.
type Notifier func(ctx context.Context, title, text string) error

// SystemNotifier is this system's notifier: Notification Center on macOS,
// the desktop's notification daemon on Linux, none elsewhere.
func SystemNotifier() Notifier {
	switch runtime.GOOS {
	case "darwin":
		return execNotifier("osascript", osascriptArgs)
	case "linux":
		return execNotifier("notify-send", notifySendArgs)
	}
	return nil
}

// osascriptArgs passes the title and text to the script as its run
// arguments, after the end of the options, so neither is ever read as
// AppleScript or as an option.
func osascriptArgs(title, text string) []string {
	return []string{"-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", "--", title, text}
}

// notifySendArgs ends the options before the title and text.
func notifySendArgs(title, text string) []string {
	return []string{"--", title, text}
}

// execNotifier runs name with the arguments args makes, without a shell.
func execNotifier(name string, args func(title, text string) []string) Notifier {
	return func(ctx context.Context, title, text string) error {
		return exec.CommandContext(ctx, name, args(title, text)...).Run()
	}
}
