package bridge

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func fileRefsRequest(t *testing.T, workspace string, files []map[string]any) SendRequest {
	t.Helper()
	data, err := json.Marshal(map[string]any{"to": "worker", "text": "Reference supplied by caller", "workspace": workspace, "message_id": "files-send", "files": files})
	requireNoError(t, err)
	var request SendRequest
	requireNoError(t, json.Unmarshal(data, &request))
	return request
}

func fileRefsSample() map[string]any {
	return map[string]any{"path": "/does-not-exist/opaque.bin", "size": float64(7), "sha256": strings.Repeat("a", 64)}
}

func TestFileRefsBridgeAcceptanceCallerMetadataSurvivesWithoutOpeningPath(t *testing.T) {
	files := []map[string]any{fileRefsSample()}
	request := fileRefsRequest(t, "paired", files)
	m, err := request.message()
	requireNoError(t, err)
	data, err := json.Marshal(m)
	requireNoError(t, err)
	var frame struct {
		Files   []map[string]any `json:"files"`
		Payload struct {
			Files []map[string]any `json:"files"`
		} `json:"payload"`
	}
	requireNoError(t, json.Unmarshal(data, &frame))
	got := frame.Files
	if got == nil {
		got = frame.Payload.Files
	}
	if !reflect.DeepEqual(got, files) {
		t.Fatalf("caller metadata was dropped or changed: got %v", got)
	}
}

func TestFileRefsBridgeAcceptanceLocalMessageCannotCarryPairedReferences(t *testing.T) {
	request := fileRefsRequest(t, "", []map[string]any{fileRefsSample()})
	if _, err := request.message(); err == nil {
		t.Fatal("local message accepted paired-only file metadata")
	}
}

func TestFileRefsBridgeAcceptanceInvalidMetadataRejectedBeforeWrite(t *testing.T) {
	for _, field := range []string{"path", "size", "sha256", "count"} {
		t.Run(field, func(t *testing.T) {
			ref := fileRefsSample()
			switch field {
			case "path":
				ref[field] = "bad\npath"
			case "size":
				ref[field] = float64(-1)
			case "sha256":
				ref[field] = "not-a-digest"
			}
			refs := []map[string]any{ref}
			if field == "count" {
				for len(refs) < 9 {
					refs = append(refs, ref)
				}
			}
			request := fileRefsRequest(t, "paired", refs)
			if _, err := request.message(); err == nil {
				t.Fatal("invalid file metadata accepted")
			}
		})
	}
}
