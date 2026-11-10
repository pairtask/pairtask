package main

import (
	"log/slog"
	"os"

	settings "github.com/pairtask/pairtask/internal/config"
	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/notify"
)

// Decision notifications (#352): off unless PAIRTASK_NOTIFY=slack with a
// webhook secret file; then the hub tells the operator in Slack when an
// agent asks for a decision. The secret is read here once and handed to
// the webhook client; it is never logged.

// loadNotifier reads the settings; an invalid explicit value stops the
// broker, a missing secret only disables notifications.
func loadNotifier(lookup settings.LookupFunc) (notify.Config, string, error) {
	return notify.Load(lookup, notify.Files{ReadFile: os.ReadFile, Stat: os.Stat})
}

// attachNotifier gives the hub the webhook when Slack mode is on, and says
// in the log what is on and how Slack is reached. The notice names the
// workspace by its pairing name unless a label is set.
func attachNotifier(h *hub.Hub, cfg config, log *slog.Logger) error {
	settings := cfg.Notify
	if !settings.Enabled() {
		log.Info(cfg.NotifyStatus)
		return nil
	}
	transport, route := outboundTransport(cfg)
	webhook, err := notify.NewSlackWebhook(settings.Secret, transport, settings.Timeout)
	if err != nil {
		return err
	}
	workspace := settings.Workspace
	if workspace == "" {
		workspace = cfg.PairingName
	}
	h.SetNotifier(&hub.Notifier{Workspace: workspace, Hidden: settings.Hidden, Post: webhook.Post})
	log.Info(cfg.NotifyStatus, "route", route, "hidden", settings.Hidden, "workspace", workspace)
	return nil
}
