package main

import (
	"fmt"
	"testing"
)

func claimsSurfaceWire(t *testing.T) map[string]any {
	t.Helper()
	out := statusSurfaceWire(t)
	out["schema_version"], out["claims_available"] = 3, true
	out["claims_total"], out["claims_truncated"] = 40, true
	out["claims"] = []any{map[string]any{
		"operator_message_id": "claim-parent", "claims_total": 20, "claims_truncated": true,
		"claims": []any{map[string]any{"operator_message_id": "claim-parent", "agent_id": "current", "task": "review", "role": "owner", "state": "active", "topic": "Review the change", "claimed_at": "2026-10-25T00:00:00Z"}},
	}}
	return out
}

func TestClaimsSurfacesAcceptanceJSONPreservesBothBounds(t *testing.T) {
	code, out := runJSON(t, "--socket", serveStatusSurface(t, claimsSurfaceWire(t)))
	assertStatusEqual(t, "JSON exit", code, 0)
	assertJSONField(t, out, "schema_version", float64(3))
	assertJSONField(t, out, "claims_available", true)
	assertJSONField(t, out, "claims_total", float64(40))
	assertJSONField(t, out, "claims_truncated", true)
	sets, ok := out["claims"].([]any)
	if !ok || len(sets) != 1 {
		t.Fatalf("claim sets missing: %#v", out["claims"])
	}
	parent := sets[0].(map[string]any)
	assertJSONField(t, parent, "operator_message_id", "claim-parent")
	assertJSONField(t, parent, "claims_total", float64(20))
	assertJSONField(t, parent, "claims_truncated", true)
	claim := parent["claims"].([]any)[0].(map[string]any)
	assertJSONField(t, claim, "topic", "Review the change")
}

func TestClaimsSurfacesAcceptanceOlderSchemasClearClaimsOnly(t *testing.T) {
	for _, schema := range []int{1, 2} {
		t.Run(fmt.Sprint(schema), func(t *testing.T) {
			wire := claimsSurfaceWire(t)
			wire["schema_version"] = schema
			code, out := runJSON(t, "--socket", serveStatusSurface(t, wire))
			assertStatusEqual(t, "compatible JSON exit", code, 0)
			assertJSONField(t, out, "claims_available", false)
			assertJSONField(t, out, "claims_total", float64(0))
			assertJSONField(t, out, "claims_truncated", false)
			claims, ok := out["claims"].([]any)
			if !ok || len(claims) != 0 {
				t.Fatalf("unsupported claims not cleared to an array: %#v", out["claims"])
			}
			assertJSONField(t, out, "agent_status_available", schema == 2)
			if schema == 2 {
				assertStatusEqual(t, "v2 declarations survive", len(out["agent_status"].([]any)), 2)
			}
		})
	}
}
