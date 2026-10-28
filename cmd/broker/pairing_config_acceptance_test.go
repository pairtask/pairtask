package main

import (
	"strings"
	"testing"
)

func TestPairingBrokerAcceptanceExplicitOptInAndDistinctSockets(t *testing.T) {
	for _, tc := range []struct {
		name, socket, egress, pairingName string
		bad, exchange, dial               bool
	}{
		{name: "disabled by default"},
		{name: "name alone does not enable", pairingName: "Alice"},
		{name: "inbound only", socket: "/tmp/pairing.sock", exchange: true},
		{name: "outbound only", egress: "direct", exchange: true, dial: true},
		{name: "both directions", socket: "/tmp/pairing.sock", egress: "direct", exchange: true, dial: true},
		{name: "unsupported route", egress: "connector", bad: true},
		{name: "agent socket collision", socket: "/tmp/sub/../agent.sock", bad: true},
		{name: "operator socket collision", socket: "/tmp/sub/../operator.sock", bad: true},
		{name: "control in name", pairingName: "Alice\nBob", bad: true},
		{name: "name byte bound", pairingName: strings.Repeat("x", 65), bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{
				"PAIRTASK_SOCKET_PATH":          "/tmp/agent.sock",
				"PAIRTASK_OPERATOR_SOCKET_PATH": "/tmp/operator.sock",
				"PAIRTASK_OPERATOR_UID":         "1000",
				"PAIRTASK_PAIRING_SOCKET":       tc.socket,
				"PAIRTASK_PAIRING_EGRESS":       tc.egress,
				"PAIRTASK_PAIRING_NAME":         tc.pairingName,
			}
			cfg, err := loadConfig(func(key string) (string, bool) { value, found := env[key]; return value, found })
			if (err != nil) != tc.bad {
				t.Fatalf("config error %v, want rejected=%v", err, tc.bad)
			}
			if tc.bad {
				return
			}
			wired := pairingConfig(cfg)
			if (wired.Exchanger != nil) != tc.exchange || (wired.Dial != nil) != tc.dial {
				t.Fatalf("pairing opt-in changed: exchange=%v dial=%v", wired.Exchanger != nil, wired.Dial != nil)
			}
			if wired.Name != tc.pairingName || cfg.PairingSocketPath != tc.socket {
				t.Fatal("pairing name or endpoint lost")
			}
		})
	}
}
