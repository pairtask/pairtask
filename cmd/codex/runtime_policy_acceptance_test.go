package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRuntimePolicyApprovesOnlyScopedCollaborationStatusAndClaimCalls(t *testing.T) {
	endpoint, _ := runtimeFixture(t)
	config, err := endpoint.config()
	terminalCheck(t, err)
	encoded, err := json.Marshal(config)
	terminalCheck(t, err)
	var wire map[string]json.RawMessage
	terminalCheck(t, json.Unmarshal(encoded, &wire))
	var policies map[string]map[string]string
	terminalCheck(t, json.Unmarshal(wire["tools"], &policies))
	want := map[string]map[string]string{
		"send":            {"approval_mode": "approve"},
		"acknowledge":     {"approval_mode": "approve"},
		"receive":         {"approval_mode": "approve"},
		"wait_reply":      {"approval_mode": "approve"},
		"listener_status": {"approval_mode": "approve"},
		"status":          {"approval_mode": "approve"},
		"read_status":     {"approval_mode": "approve"},
		"claim":           {"approval_mode": "approve"},
		"release_claim":   {"approval_mode": "approve"},
		"finish_claim":    {"approval_mode": "approve"},
		"read_claims":     {"approval_mode": "approve"},
		"join_room":       {"approval_mode": "approve"},
		"leave_room":      {"approval_mode": "approve"},
		"list_rooms":      {"approval_mode": "approve"},
	}
	if !reflect.DeepEqual(policies, want) {
		t.Fatalf("runtime tool policy = %v, want the fourteen scoped approvals %v", policies, want)
	}
	// No server-wide grant, global execution policy, or sandbox override may
	// accompany this narrowly scoped tool configuration.
	for key := range wire {
		switch key {
		case "command", "args", "enabled", "required", "tools":
		default:
			t.Errorf("unexpected runtime configuration setting %q", key)
		}
	}
	if config["enabled"] != true || config["required"] != true {
		t.Fatal("tool policy weakened required runtime startup")
	}
}
