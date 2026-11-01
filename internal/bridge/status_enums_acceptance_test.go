package bridge

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

type statusEnumSchema struct {
	Type       string                        `json:"type"`
	Required   []string                      `json:"required"`
	Properties map[string]statusEnumProperty `json:"properties"`
}

type statusEnumProperty struct {
	Type        string   `json:"type"`
	Enum        []string `json:"enum"`
	MaxLength   int      `json:"maxLength"`
	Description string   `json:"description"`
}

func listedStatusEnumSchema(t *testing.T) statusEnumSchema {
	t.Helper()
	// Discovery must work without registering or even contacting a broker.
	client := lazyAgent(t, filepath.Join(t.TempDir(), "no-broker.sock"), "enum-worker")
	session := connectMCP(t, client)
	listed, err := session.ListTools(context.Background(), nil)
	requireNoError(t, err)
	for _, tool := range listed.Tools {
		if tool.Name != "status" {
			continue
		}
		data, err := json.Marshal(tool.InputSchema)
		requireNoError(t, err)
		var schema statusEnumSchema
		requireNoError(t, json.Unmarshal(data, &schema))
		return schema
	}
	t.Fatal("status tool is missing from passive discovery")
	return statusEnumSchema{}
}

func TestStatusEnumsAcceptanceDiscoveryPublishesProtocolValues(t *testing.T) {
	schema := listedStatusEnumSchema(t)
	assertEqual(t, "schema type", schema.Type, "object")
	if !reflect.DeepEqual(schema.Required, []string{"state"}) {
		t.Fatalf("only state is required, got %v", schema.Required)
	}
	for _, field := range []struct {
		name string
		want []string
	}{
		{"state", protocol.StatusStates},
		{"waiting_on", protocol.StatusWaiting},
	} {
		property := schema.Properties[field.name]
		assertEqual(t, "enum property type", property.Type, "string")
		if !reflect.DeepEqual(property.Enum, field.want) {
			t.Errorf("%s enum = %v, want %v", field.name, property.Enum, field.want)
		}
	}
}

func TestStatusEnumsAcceptanceKeepsFreeTextFieldsBounded(t *testing.T) {
	schema := listedStatusEnumSchema(t)
	assertEqual(t, "status property count", len(schema.Properties), 5)
	for _, name := range []string{"task", "step", "ref"} {
		property := schema.Properties[name]
		assertEqual(t, name+" type", property.Type, "string")
		assertEqual(t, name+" maximum", property.MaxLength, protocol.StatusFieldBytes)
		if len(property.Enum) != 0 {
			t.Errorf("free-text %s became an enum: %v", name, property.Enum)
		}
		if property.Description == "" {
			t.Errorf("%s lost its documentation", name)
		}
	}
}
