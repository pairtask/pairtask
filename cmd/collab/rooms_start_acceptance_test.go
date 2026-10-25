package main

import (
	"slices"
	"testing"
)

func roomStartArgs(t *testing.T, args []string) []string {
	t.Helper()
	var rooms []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		if args[i] == "--room" && i+1 < len(args) {
			rooms = append(rooms, args[i+1])
			i++
		}
	}
	return rooms
}

func TestRoomsStartAcceptancePassesRepeatedRoomsToTransport(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			plan := buildStartForTest(t, parseStartForTest(t, agent, "--room", "alpha", "--room", "beta", "--", "--room", "native-value"), startTestLayout(t))
			args := plan.Argv
			if agent == "claude" {
				config := decodeStartConfig(t, plan.Config)
				raw := config["mcpServers"].(map[string]any)["pairtask"].(map[string]any)["args"].([]any)
				args = nil
				for _, v := range raw {
					args = append(args, v.(string))
				}
			}
			assertStartEqual(t, "transport rooms", roomStartArgs(t, args), []string{"alpha", "beta"})
			if !slices.Contains(plan.Argv, "native-value") {
				t.Fatal("native args lost")
			}
		})
	}
}

func TestRoomsStartAcceptanceRejectsInvalidRoomNames(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		for _, room := range []string{"square", "Bad", "bad/name"} {
			if _, err := parseStart([]string{agent, "--room", room}); err == nil {
				t.Fatalf("accepted invalid room %q", room)
			}
		}
	}
}
