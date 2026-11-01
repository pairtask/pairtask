package protocol_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestWorkspaceLocalNamesProtocolAcceptancePreservesBothNamesOnWire(t *testing.T) {
	key := "sha256:" + strings.Repeat("a", 64)
	input, err := json.Marshal(map[string]string{"name": "Peer self-name", "local_name": "Local workstation", "key": key})
	if err != nil {
		t.Fatal(err)
	}
	var view protocol.WorkspaceView
	if err := json.Unmarshal(input, &view); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]string{"name": "Peer self-name", "local_name": "Local workstation", "key": key} {
		var got string
		if err := json.Unmarshal(fields[field], &got); err != nil {
			t.Fatalf("missing or invalid %s: %v", field, err)
		}
		if got != want {
			t.Fatalf("%s = %q, want %q", field, got, want)
		}
	}
}

func TestWorkspaceLocalNamesProtocolAcceptanceOmitsEmptyLocalName(t *testing.T) {
	for _, input := range []string{`{"name":"Peer self-name","key":"peer-key"}`, `{"name":"Peer self-name","local_name":"","key":"peer-key"}`} {
		var view protocol.WorkspaceView
		if err := json.Unmarshal([]byte(input), &view); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		if _, held := fields["local_name"]; held {
			t.Fatalf("empty local_name must be omitted: %s", encoded)
		}
		if view.Name != "Peer self-name" {
			t.Fatalf("self-name changed: %q", view.Name)
		}
	}
}
