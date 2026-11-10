package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/notify"
)

func TestSlackNoticeUsesConnectorEgressWithTLSEndingInBroker(t *testing.T) {
	path, observed := captureClassifierEgress(t)
	transport, route := outboundTransport(config{PairingEgressSocket: path})
	if transport == nil || route == "direct" {
		t.Fatalf("configured egress was not selected: route=%q", route)
	}
	const secret = "private-webhook-token"
	webhook, err := notify.NewSlackWebhook("https://hooks.slack.com/services/T/B/"+secret, transport, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = webhook.Post(context.Background(), notify.Notice{Workspace: "desk", Agent: "codex-1", Summary: "Choose now", Anchor: "ask-1"})
	if err == nil {
		t.Fatal("fake forwarder provided no Slack reply")
	}
	select {
	case got := <-observed:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.destination != "hooks.slack.com:443" {
			t.Fatalf("forwarder destination = %q", got.destination)
		}
		if len(got.first) == 0 || got.first[0] != 0x16 {
			t.Fatalf("first tunneled bytes are not a TLS handshake: %x", got.first)
		}
		if bytes.Contains(got.first, []byte(secret)) || strings.Contains(err.Error(), secret) {
			t.Fatal("Slack webhook secret crossed egress or appeared in its error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Slack notice never reached connector egress")
	}
}

func TestSlackStandaloneTransportRemainsDirect(t *testing.T) {
	transport, route := outboundTransport(config{})
	if transport != nil || route != "direct" {
		t.Fatalf("standalone transport = %T, route = %q", transport, route)
	}
}
