package classifier_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/classifier"
)

// Explicit opt-in; never uses real operator text or makes a normal CI request.
func TestClassifierLiveAcceptanceTypeSafeSynthetic(t *testing.T) {
	if os.Getenv("PAIRTASK_CLASSIFIER_LIVE_SMOKE") != "1" {
		t.Skip("explicit live smoke opt-in required")
	}
	key := os.Getenv("TYPESAFE_API_KEY")
	if key == "" {
		t.Fatal("live smoke requested without a TypeSafe credential")
	}
	client, err := classifier.NewClient(classifier.ProviderTypeSafe, "", key, 15*time.Second, nil)
	if err != nil {
		t.Fatal("cannot construct canonical TypeSafe client")
	}
	started := time.Now()
	answer, err := client.Ask(context.Background(), syntheticRequest())
	if err != nil {
		t.Fatalf("canonical TypeSafe request failed: %v", err)
	}
	if _, valid := syntheticRequest().Criteria[answer.Choice]; !valid {
		t.Fatal("provider returned a choice outside the synthetic roster")
	}
	if answer.Model != "jev-1.14.0" {
		t.Fatal("provider did not report the pinned requested model")
	}
	t.Logf("canonical TypeSafe synthetic request succeeded: model=jev-1.14.0 choice=%s confidence=%.3f latency_ms=%d", answer.Choice, answer.Confidence, time.Since(started).Milliseconds())
}
