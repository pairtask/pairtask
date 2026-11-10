package classifier_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/classifier"
)

func loadClassifier(values map[string]string) (classifier.Config, string, error) {
	lookup := func(name string) (string, bool) { value, ok := values[name]; return value, ok }
	return classifier.Load(lookup, classifier.Files{ReadFile: os.ReadFile, Stat: os.Stat})
}

func syntheticKey(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("synthetic-private-marker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClassifierConfigAcceptanceMissingKeyKeepsChatAvailable(t *testing.T) {
	for _, values := range []map[string]string{
		{},
		{"PAIRTASK_CLASSIFIER": "off"},
		{"PAIRTASK_CLASSIFIER": "shadow"},
		{"PAIRTASK_CLASSIFIER": "shadow", "PAIRTASK_CLASSIFIER_KEY_FILE": filepath.Join(t.TempDir(), "missing")},
	} {
		cfg, status, err := loadClassifier(values)
		providerOK(t, err)
		providerEqual(t, cfg.Enabled(), false)
		providerEqual(t, status != "", true)
	}
}

func TestClassifierConfigAcceptanceProviderDefaultAndNoSecretStatus(t *testing.T) {
	cfg, status, err := loadClassifier(map[string]string{"PAIRTASK_CLASSIFIER": "shadow", "PAIRTASK_CLASSIFIER_PROVIDER": "typesafe", "PAIRTASK_CLASSIFIER_KEY_FILE": syntheticKey(t)})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled() || !strings.HasPrefix(cfg.Model, "jev-") {
		t.Fatalf("TypeSafe config uses wrong model/mode: %s %s", cfg.Mode, cfg.Model)
	}
	if cfg.Key != "synthetic-private-marker" || strings.Contains(status, "synthetic-private-marker") {
		t.Fatal("credential loading/status boundary failed")
	}
}

func TestClassifierConfigAcceptanceRejectsNonfiniteThreshold(t *testing.T) {
	for _, threshold := range []string{"NaN", "+Inf", "-Inf", "-0.01", "1.01"} {
		t.Run(threshold, func(t *testing.T) {
			_, _, err := loadClassifier(map[string]string{"PAIRTASK_CLASSIFIER": "shadow", "PAIRTASK_CLASSIFIER_PROVIDER": "typesafe", "PAIRTASK_CLASSIFIER_KEY_FILE": syntheticKey(t), "PAIRTASK_CLASSIFIER_THRESHOLD": threshold})
			if err == nil {
				t.Fatal("invalid threshold accepted")
			}
		})
	}
}

func TestClassifierConfigAcceptanceRejectsUnsafeCredentialFile(t *testing.T) {
	for _, mode := range []os.FileMode{0644, 0640, 0604} {
		path := syntheticKey(t)
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		_, _, err := loadClassifier(map[string]string{"PAIRTASK_CLASSIFIER": "shadow", "PAIRTASK_CLASSIFIER_PROVIDER": "typesafe", "PAIRTASK_CLASSIFIER_KEY_FILE": path})
		if err == nil {
			t.Errorf("mode %o accepted", mode)
		}
	}
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	_, _, err := loadClassifier(map[string]string{"PAIRTASK_CLASSIFIER": "shadow", "PAIRTASK_CLASSIFIER_PROVIDER": "typesafe", "PAIRTASK_CLASSIFIER_KEY_FILE": path})
	if err == nil {
		t.Error("directory accepted as credential file")
	}
}

func TestClassifierConfigAcceptanceRefusesAutoMode(t *testing.T) {
	_, _, err := loadClassifier(map[string]string{"PAIRTASK_CLASSIFIER": "auto"})
	if err == nil {
		t.Fatal("automatic assignment enabled in shadow-only rollout")
	}
}

func TestClassifierConfigAcceptanceRejectsForeignOwnerBeforeReading(t *testing.T) {
	path := syntheticKey(t)
	values := map[string]string{"PAIRTASK_CLASSIFIER": "shadow", "PAIRTASK_CLASSIFIER_PROVIDER": "typesafe", "PAIRTASK_CLASSIFIER_KEY_FILE": path}
	lookup := func(name string) (string, bool) { v, ok := values[name]; return v, ok }
	read := false
	_, _, err := classifier.Load(lookup, classifier.Files{Stat: os.Stat, UID: 1234, Owner: func(fs.FileInfo) (int, bool) { return 5678, true }, ReadFile: func(string) ([]byte, error) { read = true; return []byte("synthetic-private-marker"), nil }})
	if err == nil || read {
		t.Fatalf("foreign-owned key accessed: read=%v err=%v", read, err)
	}
}
