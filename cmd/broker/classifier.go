package main

import (
	"log/slog"
	"os"

	"github.com/pairtask/pairtask/internal/classifier"
	settings "github.com/pairtask/pairtask/internal/config"
	"github.com/pairtask/pairtask/internal/hub"
)

// The shadow classifier (#339): off unless PAIRTASK_CLASSIFIER=shadow with
// a credential file; then the hub records, and never applies, a
// prediction for each untagged operator message to everyone. The
// credential is read here once and handed to the client; it is never
// logged.

// loadClassifier reads the settings; an invalid explicit value stops the
// broker, a missing credential only disables the classifier.
func loadClassifier(lookup settings.LookupFunc) (classifier.Config, string, error) {
	return classifier.Load(lookup, classifier.Files{ReadFile: os.ReadFile, Stat: os.Stat})
}

// attachClassifier gives the hub a client when shadow mode is on, and says
// in the log what the classifier does.
func attachClassifier(h *hub.Hub, cfg classifier.Config, status string, log *slog.Logger) error {
	if !cfg.Enabled() {
		log.Info(status)
		return nil
	}
	client, err := classifier.NewClient(cfg.Provider, cfg.Account, cfg.Key, cfg.Timeout, nil)
	if err != nil {
		return err
	}
	h.SetClassifier(&hub.Classifier{Provider: cfg.Provider, Model: cfg.Model, Threshold: cfg.Threshold, Roles: cfg.Roles, Ask: client.Ask})
	log.Info(status, "threshold", cfg.Threshold, "note", "shadow: predictions are recorded, none applied; the threshold is uncalibrated")
	return nil
}
