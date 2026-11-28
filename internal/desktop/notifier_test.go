package desktop

import "testing"

func TestTheAppTakingNotificationsSilencesTheNodeAcrossExtensionStarts(t *testing.T) {
	dir := t.TempDir()
	on := true
	if _, _, err := storeSettings(dir, Settings{StartedName: "mac", Notify: &on}); err != nil {
		t.Fatal(err)
	}
	n := &Node{cfg: Config{Dir: dir}}
	if out, err := n.setNotifier(SetNotifierArgs{App: true}); err != nil || !out.App {
		t.Fatalf("set_notifier app: %+v %v", out, err)
	}
	// The Claude Desktop extension starts again with its notifications on.
	now, _, err := storeSettings(dir, Settings{StartedName: "mac", Notify: &on})
	if err != nil {
		t.Fatal(err)
	}
	if now.notifies() {
		t.Fatal("the node notifies per message although the app took notifications over")
	}
	if out, _ := n.setNotifier(SetNotifierArgs{App: false}); out.App {
		t.Fatal("notifications weren't given back to the node")
	}
	if s, _ := ReadSettings(dir); !s.notifies() {
		t.Fatal("given back, the node still doesn't notify")
	}
}
