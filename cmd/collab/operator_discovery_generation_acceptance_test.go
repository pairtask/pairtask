package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestDiscoveryGenerationSurvivesIncusReportCLI(t *testing.T) {
	var stderr bytes.Buffer
	_, req, code := parseContactMobility([]string{"discovery", "--report",
		`{"contact_find_generation":7,"contact_found":[],"contact_devices":[]}`, "--json"}, &stderr)
	if code != -1 {
		t.Fatalf("discovery report rejected: code %d: %s", code, stderr.String())
	}
	wire, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal(wire, &sent); err != nil {
		t.Fatal(err)
	}
	if got := sent["contact_find_generation"]; got != float64(7) {
		t.Fatalf("Incus CLI lost the applied scan generation: %s", wire)
	}
	if req.Type != protocol.TypeOperatorContact || req.ContactOp != protocol.ContactOpDiscovery {
		t.Fatalf("report changed operator operation: %+v", req)
	}
}
