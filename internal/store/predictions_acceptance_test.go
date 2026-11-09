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
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	base := store.Prediction{MessageID: "original", Outcome: store.PredictionPredicted, AgentID: "none", Confidence: .9, Probabilities: map[string]float64{"none": .9, "tester": .05}, AbstainProbability: .05, Eligible: []string{"none", "tester"}, Provider: "typesafe", Model: "jev-latest", ModelVersion: "jev-1.14.0", Threshold: .7, LatencyMS: 215, CreatedAt: time.Date(2026, 11, 9, 23, 0, 0, 0, time.UTC)}
	inserted, err := s.RecordPrediction(ctx, base)
	if err != nil || !inserted {
		t.Fatalf("initial write: %v %v", inserted, err)
	}
	var group sync.WaitGroup
	var replacements atomic.Int32
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			changed := base
			changed.AgentID = fmt.Sprintf("replacement-%d", i)
			changed.ModelVersion = "new-model"
			ok, err := s.RecordPrediction(ctx, changed)
			if err != nil {
				t.Errorf("duplicate write: %v", err)
			}
			if ok {
				replacements.Add(1)
			}
		}(i)
	}
	group.Wait()
	if replacements.Load() != 0 {
		t.Fatal("later writers replaced decision")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Prediction(ctx, "original")
	if err != nil || got == nil || !reflect.DeepEqual(*got, base) {
		t.Fatalf("restart lost first decision/provenance: %+v %v", got, err)
	}
	rows, more, err := s.PredictionsSince(ctx, base.CreatedAt, 100)
	if err != nil || more || len(rows) != 1 {
		t.Fatalf("duplicate rows: %d %v", len(rows), err)
	}
	rows, more, err = s.PredictionsSince(ctx, base.CreatedAt.Add(time.Second), 100)
	if err != nil || more || len(rows) != 0 {
		t.Fatalf("since cutoff ignored: %d %v", len(rows), err)
	}
}

func TestClassifierPredictionAcceptanceCompetingFirstWrites(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "race.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var group sync.WaitGroup
	var winners atomic.Int32
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			<-start
			inserted, err := s.RecordPrediction(context.Background(), store.Prediction{MessageID: "same", Outcome: store.PredictionPredicted, AgentID: fmt.Sprintf("agent-%d", i)})
			if err != nil {
				t.Errorf("write: %v", err)
			}
			if inserted {
				winners.Add(1)
			}
		}(i)
	}
	close(start)
	group.Wait()
	if winners.Load() != 1 {
		t.Fatalf("first writers = %d, want 1", winners.Load())
	}
}
