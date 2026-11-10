package notify_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/notify"
)

func loadNotify(values map[string]string, files notify.Files) (notify.Config, string, error) {
	lookup := func(name string) (string, bool) { value, ok := values[name]; return value, ok }
	return notify.Load(lookup, files)
}

func privateWebhook(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "webhook")
	if err := os.WriteFile(path, []byte(webhook+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNotifyConfigAcceptanceOffByDefaultAndMissingSecretKeepsBrokerAvailable(t *testing.T) {
	for _, values := range []map[string]string{
		{},
		{"PAIRTASK_NOTIFY": "off"},
		{"PAIRTASK_NOTIFY": "slack"},
		{"PAIRTASK_NOTIFY": "slack", "PAIRTASK_NOTIFY_SECRET_FILE": filepath.Join(t.TempDir(), "missing")},
	} {
		cfg, status, err := loadNotify(values, notify.Files{ReadFile: os.ReadFile, Stat: os.Stat})
		if err != nil || cfg.Enabled() || status == "" {
			t.Fatalf("missing/off config = enabled %t, status %q, error %v", cfg.Enabled(), status, err)
		}
	}
}

func TestNotifyConfigAcceptancePrivateWebhookAndOptions(t *testing.T) {
	path := privateWebhook(t)
	cfg, status, err := loadNotify(map[string]string{
		"PAIRTASK_NOTIFY": "slack", "PAIRTASK_NOTIFY_SECRET_FILE": path,
		"PAIRTASK_NOTIFY_HIDDEN": "1", "PAIRTASK_NOTIFY_WORKSPACE": "desk",
	}, notify.Files{ReadFile: os.ReadFile, Stat: os.Stat})
	if err != nil || !cfg.Enabled() || cfg.Secret != webhook || !cfg.Hidden || cfg.Workspace != "desk" {
		t.Fatalf("private config = %+v, error %v", cfg, err)
	}
	if strings.Contains(status, "secret-marker") || strings.Contains(status, webhook) {
		t.Fatal("config status disclosed the webhook secret")
	}
}

func TestNotifyConfigAcceptanceRejectsReadableOrWrongOwnerBeforeReadingSecret(t *testing.T) {
	path := privateWebhook(t)
	values := map[string]string{"PAIRTASK_NOTIFY": "slack", "PAIRTASK_NOTIFY_SECRET_FILE": path}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	_, _, err := loadNotify(values, notify.Files{ReadFile: os.ReadFile, Stat: os.Stat})
	if err == nil || !strings.Contains(err.Error(), "readable by its owner only") {
		t.Fatalf("world-readable secret accepted: %v", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	read := false
	_, _, err = loadNotify(values, notify.Files{
		Stat: os.Stat, UID: 1234,
		Owner:    func(fs.FileInfo) (int, bool) { return 5678, true },
		ReadFile: func(string) ([]byte, error) { read = true; return []byte(webhook), nil },
	})
	if err == nil || !strings.Contains(err.Error(), "owned by the broker") || read {
		t.Fatalf("wrong-owner secret accepted or read: error %v, read %t", err, read)
	}
}

func TestNotifyConfigAcceptanceRejectsInvalidModeAndURL(t *testing.T) {
	_, _, err := loadNotify(map[string]string{"PAIRTASK_NOTIFY": "email"}, notify.Files{})
	if err == nil {
		t.Fatal("unsupported notification mode accepted")
	}
	path := privateWebhook(t)
	if err := os.WriteFile(path, []byte("https://evil.example/services/T/B/C\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, status, err := loadNotify(map[string]string{
		"PAIRTASK_NOTIFY": "slack", "PAIRTASK_NOTIFY_SECRET_FILE": path,
	}, notify.Files{ReadFile: os.ReadFile, Stat: os.Stat})
	if err == nil || strings.Contains(status, "evil.example") {
		t.Fatalf("non-Slack webhook accepted or disclosed: status %q, error %v", status, err)
	}
}
