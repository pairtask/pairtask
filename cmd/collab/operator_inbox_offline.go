package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/pairtask/pairtask/internal/store"
)

// Offline inbox maintenance (#119): a backup of the broker's database, and
// an archive that frees retained capacity after one. Both run on the
// database file with its broker stopped: they take --database, never a
// socket, and refuse while a broker or another maintenance holds it.
const (
	operatorInboxExportUsage  = "usage: pairtask operator inbox export PATH --database DB [--json] (offline: stop the broker first)"
	operatorInboxArchiveUsage = "usage: pairtask operator inbox archive PATH --database DB (--confirm | --dry-run) [--upper SEQ] [--json] (offline: stop the broker first)"
)

// offlineArgs are what both commands take: the backup path, then flags.
type offlineArgs struct {
	backup, database string
	asJSON           bool
}

func (a *offlineArgs) register(flags *flag.FlagSet) {
	flags.StringVar(&a.database, "database", "", "the broker's database file (required; never a default)")
	flags.BoolVar(&a.asJSON, "json", false, "emit the result as JSON")
}

// offlineCommand is one maintenance command's flags, usage and own checks.
type offlineCommand struct {
	flags    *flag.FlagSet
	usage    string
	validate func() error
}

// parse takes PATH first, then the flags after it.
func (a *offlineArgs) parse(cmd offlineCommand, args []string, stderr io.Writer) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, "the backup PATH comes first")
		fmt.Fprintln(stderr, cmd.usage)
		return 2
	}
	a.backup = args[0]
	return parseFlags(cmd.flags, args[1:], cmd.usage, stderr, func(rest []string) error {
		if len(rest) != 0 {
			return errors.New("unexpected arguments after the flags")
		}
		if a.database == "" {
			return errors.New("--database is required: name the broker's database file")
		}
		return cmd.validate()
	})
}

func runInboxExport(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var a offlineArgs
	flags := flag.NewFlagSet("operator inbox export", flag.ContinueOnError)
	a.register(flags)
	if code := a.parse(offlineCommand{flags, operatorInboxExportUsage, func() error { return nil }}, args, stderr); code != -1 {
		return code
	}
	return withOffline(a.database, stderr, func(db *store.OfflineDatabase) int {
		backup, err := db.Export(ctx, a.backup)
		if err != nil {
			fmt.Fprintln(stderr, "export:", err)
			return 1
		}
		if a.asJSON {
			return encodeJSON(stdout, stderr, backup)
		}
		fmt.Fprintf(stdout, "backup %s: %s, sha256 %s\n", backup.Path, humanBytes(int(backup.Bytes)), backup.SHA256)
		return 0
	})
}

// archiveArgs are an archive's own flags.
type archiveArgs struct {
	offlineArgs
	upper           uint64
	confirm, dryRun bool
}

func (a *archiveArgs) request() store.ArchiveRequest {
	return store.ArchiveRequest{Upper: a.upper, HasUpper: a.upper != 0, DryRun: a.dryRun, Backup: a.backup}
}

func (a *archiveArgs) validate() error {
	if a.confirm == a.dryRun {
		return errors.New("archive needs exactly one of --confirm or --dry-run")
	}
	return nil
}

func runInboxArchive(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var a archiveArgs
	flags := flag.NewFlagSet("operator inbox archive", flag.ContinueOnError)
	a.register(flags)
	flags.Uint64Var(&a.upper, "upper", 0, "archive no message after this sequence, inclusive; default: the last, frozen for the run")
	flags.BoolVar(&a.confirm, "confirm", false, "back up, then archive")
	flags.BoolVar(&a.dryRun, "dry-run", false, "report what would be archived and kept; write nothing")
	if code := a.parse(offlineCommand{flags, operatorInboxArchiveUsage, a.validate}, args, stderr); code != -1 {
		return code
	}
	return withOffline(a.database, stderr, func(db *store.OfflineDatabase) int {
		report, err := db.Archive(ctx, a.request())
		if err != nil {
			fmt.Fprintln(stderr, "archive:", err)
			return 1
		}
		if a.asJSON {
			return encodeJSON(stdout, stderr, report)
		}
		writeArchiveReport(stdout, report)
		return 0
	})
}

// withOffline runs one maintenance on the locked database.
func withOffline(database string, stderr io.Writer, run func(*store.OfflineDatabase) int) int {
	db, err := store.OpenOffline(database)
	if err != nil {
		fmt.Fprintln(stderr, "maintenance refused:", err)
		return 1
	}
	code := run(db)
	if err := db.Close(); err != nil {
		fmt.Fprintln(stderr, "close the database:", err)
		return 1
	}
	return code
}

func writeArchiveReport(w io.Writer, r store.ArchiveReport) {
	verb := "archived"
	if r.DryRun {
		verb = "would archive"
	}
	fmt.Fprintf(w, "%s %d messages up to #%d, freeing %s of retained history\n", verb, r.Archived.Messages, r.Upper, humanBytes(int(r.Archived.Bytes)))
	if r.Backup != nil {
		fmt.Fprintf(w, "backup %s: sha256 %s\n", r.Backup.Path, r.Backup.SHA256)
	}
	for reason, n := range r.KeptByReason {
		if n > 0 {
			fmt.Fprintf(w, "  kept %d: %s\n", n, reason)
		}
	}
	fmt.Fprintf(w, "retained history: %d/%d messages, %s of %s\n", r.Retained.After.Messages, r.Retained.LimitMessages,
		humanBytes(int(r.Retained.After.Bytes)), humanBytes(int(r.Retained.LimitBytes)))
}
