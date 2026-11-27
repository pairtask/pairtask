package desktop

import "testing"

// started stores what a copy started with name stores, as Start does.
func started(t *testing.T, dir, name string) Settings {
	t.Helper()
	now, _, err := storeSettings(dir, Settings{StartedName: name})
	if err != nil {
		t.Fatal(err)
	}
	return now
}

func named(t *testing.T, dir, name string) {
	t.Helper()
	if _, _, err := storeSettings(dir, Settings{Name: name}); err != nil {
		t.Fatal(err)
	}
}

func TestANameSetInTheAppHoldsAcrossTheExtensionsStarts(t *testing.T) {
	dir := t.TempDir()
	if got := started(t, dir, "workspace").Name; got != "workspace" {
		t.Fatalf("a first start named the node %q", got)
	}
	named(t, dir, "siamion-home-desktop")
	if got := started(t, dir, "workspace").Name; got != "siamion-home-desktop" {
		t.Fatalf("the extension's unchanged name undid the app's: %q", got)
	}
	if got := started(t, dir, "home").Name; got != "home" {
		t.Fatalf("a changed extension name didn't apply: %q", got)
	}
}

func TestStoredSettingsFromBeforeKeepTheirName(t *testing.T) {
	dir := t.TempDir()
	if err := writeSettings(dir, Settings{Name: "kept"}); err != nil {
		t.Fatal(err)
	}
	now := started(t, dir, "workspace")
	if now.Name != "kept" || now.StartedName != "workspace" {
		t.Fatalf("after the upgrade's first start: %+v", now)
	}
}
