package main

import (
	"log/slog"
	"net/http"
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
// in the log what the classifier does and how it reaches the provider.
func attachClassifier(h *hub.Hub, cfg config, log *slog.Logger) error {
	settings := cfg.Classifier
	if !settings.Enabled() {
		log.Info(cfg.ClassifierStatus)
		return nil
	}
	transport, route := classifierTransport(cfg)
	credential := classifier.Credential{Provider: settings.Provider, Account: settings.Account, Key: settings.Key}
	client, err := classifier.NewClient(credential, settings.Timeout, transport)
	if err != nil {
		return err
	}
	h.SetClassifier(&hub.Classifier{Provider: settings.Provider, Model: settings.Model, Threshold: settings.Threshold, Roles: settings.Roles, Ask: client.Ask})
	log.Info(cfg.ClassifierStatus, "threshold", settings.Threshold, "route", route,
		"note", "shadow: predictions are recorded, none applied; the threshold is uncalibrated")
	return nil
}

// classifierTransport is the provider's route, shared with the notifier.
func classifierTransport(cfg config) (http.RoundTripper, string) { return outboundTransport(cfg) }
