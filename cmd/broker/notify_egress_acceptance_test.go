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
	if transport == nil {
		t.Fatalf("configured egress was not selected: route=%q", route)
	}
	if route == "direct" {
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
	assertSlackForwarder(t, observed, secret, err)
}

func assertSlackForwarder(t *testing.T, observed <-chan classifierEgressObservation, secret string, postErr error) {
	t.Helper()
	select {
	case got := <-observed:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.destination != "hooks.slack.com:443" {
			t.Fatalf("forwarder destination = %q", got.destination)
		}
		assertSlackTLSHello(t, got.first)
		assertSlackSecretAbsent(t, got.first, secret, postErr)
	case <-time.After(3 * time.Second):
		t.Fatal("Slack notice never reached connector egress")
	}
}

func assertSlackTLSHello(t *testing.T, first []byte) {
	t.Helper()
	if len(first) == 0 {
		t.Fatal("first tunneled bytes are empty")
	}
	if first[0] != 0x16 {
		t.Fatalf("first tunneled bytes are not a TLS handshake: %x", first)
	}
}

func assertSlackSecretAbsent(t *testing.T, first []byte, secret string, postErr error) {
	t.Helper()
	if bytes.Contains(first, []byte(secret)) {
		t.Fatal("Slack webhook secret crossed egress")
	}
	if strings.Contains(postErr.Error(), secret) {
		t.Fatal("Slack webhook secret appeared in its error")
	}
}

func TestSlackStandaloneTransportRemainsDirect(t *testing.T) {
	transport, route := outboundTransport(config{})
	if transport != nil {
		t.Fatalf("standalone transport = %T, route = %q", transport, route)
	}
	if route != "direct" {
		t.Fatalf("standalone transport = %T, route = %q", transport, route)
	}
}
