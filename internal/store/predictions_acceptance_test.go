package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/store"
)

func TestClassifierPredictionAcceptanceFirstWriterSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "predictions.db")
	s := openPredictionStore(t, path)
	base := store.Prediction{MessageID: "original", Outcome: store.PredictionPredicted, AgentID: "none", Confidence: .9, Probabilities: map[string]float64{"none": .9, "tester": .05}, AbstainProbability: .05, Eligible: []string{"none", "tester"}, Provider: "typesafe", Model: "jev-latest", ModelVersion: "jev-1.14.0", Threshold: .7, LatencyMS: 215, CreatedAt: time.Date(2026, 11, 9, 23, 0, 0, 0, time.UTC)}
	inserted, err := s.RecordPrediction(context.Background(), base)
	predictionOK(t, err)
	predictionEqual(t, inserted, true)
	predictionEqual(t, writePredictionsTogether(t, s, competingPredictions(base)), int32(0))
	predictionOK(t, s.Close())
	s = openPredictionStore(t, path)
	got, err := s.Prediction(context.Background(), "original")
	predictionOK(t, err)
	predictionEqual(t, got, &base)
	checkPredictionCutoff(t, s, base.CreatedAt)
}

func openPredictionStore(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(path)
	predictionOK(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}
func predictionOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func predictionEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
func competingPredictions(base store.Prediction) []store.Prediction {
	var predictions []store.Prediction
	for i := 0; i < 8; i++ {
		changed := base
		changed.AgentID = fmt.Sprintf("replacement-%d", i)
		changed.ModelVersion = "new-model"
		predictions = append(predictions, changed)
	}
	return predictions
}
func writePredictionsTogether(t *testing.T, s *store.Store, predictions []store.Prediction) int32 {
	t.Helper()
	var group sync.WaitGroup
	var winners atomic.Int32
	start := make(chan struct{})
	for _, p := range predictions {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			inserted, err := s.RecordPrediction(context.Background(), p)
			predictionOK(t, err)
			if inserted {
				winners.Add(1)
			}
		}()
	}
	close(start)
	group.Wait()
	return winners.Load()
}
func checkPredictionCutoff(t *testing.T, s *store.Store, at time.Time) {
	t.Helper()
	rows, more, err := s.PredictionsSince(context.Background(), at, 100)
	predictionOK(t, err)
	predictionEqual(t, more, false)
	predictionEqual(t, len(rows), 1)
	rows, more, err = s.PredictionsSince(context.Background(), at.Add(time.Second), 100)
	predictionOK(t, err)
	predictionEqual(t, more, false)
	predictionEqual(t, len(rows), 0)
}

func TestClassifierPredictionAcceptanceCompetingFirstWrites(t *testing.T) {
	s := openPredictionStore(t, filepath.Join(t.TempDir(), "race.db"))
	predictions := competingPredictions(store.Prediction{MessageID: "same", Outcome: store.PredictionPredicted})
	predictionEqual(t, writePredictionsTogether(t, s, predictions), int32(1))
}
