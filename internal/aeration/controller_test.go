package aeration

import (
	"context"
	"testing"
	"time"

	"github.com/wyw14/cry-97/internal/model"
	"github.com/wyw14/cry-97/internal/sensor"
)

func newTestController(t *testing.T) *Controller {
	t.Helper()
	decoder, err := sensor.NewBatchDecoder(24)
	if err != nil {
		t.Fatal(err)
	}
	windows, err := NewWindowBook(5)
	if err != nil {
		t.Fatal(err)
	}
	fleet, err := NewBlowerFleet([]Blower{{ID: "blower-basin-line-1", BasinID: "basin-line-1"}})
	if err != nil {
		t.Fatal(err)
	}
	policies, err := NewPolicyBook([]model.LineID{"line-1"})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := NewController(decoder, windows, fleet, policies)
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func biologicalLine() LineContext {
	return LineContext{LineID: "line-1", Stage: model.StageBiological, HasBatch: true, Generation: 2}
}

func TestSubmitWindowDrivesStepLimitedCommand(t *testing.T) {
	controller := newTestController(t)
	outcome, err := controller.SubmitWindow(context.Background(), "line-1", "basin-line-1", []byte("[1.0,1.1,1.2]"), biologicalLine(), decisionNow)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Command == nil {
		t.Fatalf("expected a command for low dissolved oxygen, got %+v", outcome.Decision)
	}
	if outcome.Command.Setpoint != 4 {
		t.Fatalf("expected one max step from 0 to 4 Hz, got %.1f", outcome.Command.Setpoint)
	}
	blower, _ := controller.blowers.Get("blower-basin-line-1")
	if blower.Frequency != 4 || !blower.Running {
		t.Fatalf("expected blower at 4 Hz running, got %+v", blower)
	}
	if outcome.Decision.SampleState != SampleOK || outcome.Decision.SampleValue != 1.2 {
		t.Fatalf("expected the newest window value as the fresh sample, got %+v", outcome.Decision)
	}
}

func TestSubmitWindowHoldsInsideTargetRange(t *testing.T) {
	controller := newTestController(t)
	outcome, err := controller.SubmitWindow(context.Background(), "line-1", "basin-line-1", []byte("[2.4,2.5,2.6]"), biologicalLine(), decisionNow)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Command != nil || outcome.Decision.Changed {
		t.Fatalf("expected no command inside the target range, got %+v", outcome.Decision)
	}
}

func TestEvaluateFallsBackToSafeFrequencyWhenSampleStale(t *testing.T) {
	controller := newTestController(t)
	stale := model.NewSample("line-1", "basin-line-1", model.SampleDissolvedOxygen, 3, 0.4, "mg/L", decisionNow.Add(-10*time.Minute))
	controller.ObserveSample(stale)
	decision, command, err := controller.Evaluate(context.Background(), "line-1", "basin-line-1", biologicalLine(), decisionNow)
	if err != nil {
		t.Fatal(err)
	}
	if decision.SampleState != SampleStale {
		t.Fatalf("expected stale sample state, got %s", decision.SampleState)
	}
	// Safe frequency is 36 Hz, reached step by step from 0.
	if command == nil || command.Setpoint != 4 {
		t.Fatalf("expected a step-limited move toward the safe frequency, got %+v", command)
	}
}

func TestEvaluateIdlesWithoutActiveBatch(t *testing.T) {
	controller := newTestController(t)
	line := LineContext{LineID: "line-1", Stage: model.StageClosed, HasBatch: false, Generation: 4}
	decision, command, err := controller.Evaluate(context.Background(), "line-1", "basin-line-1", line, decisionNow)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Changed || command != nil {
		t.Fatalf("expected the blower to stay at the idle frequency, got %+v", decision)
	}
}

func TestDecisionsAreRecordedForStatus(t *testing.T) {
	controller := newTestController(t)
	if _, err := controller.SubmitWindow(context.Background(), "line-1", "basin-line-1", []byte("[2.4,2.5]"), biologicalLine(), decisionNow); err != nil {
		t.Fatal(err)
	}
	decisions := controller.Decisions()
	if len(decisions) != 1 || decisions[0].LineID != "line-1" {
		t.Fatalf("expected one recorded decision, got %+v", decisions)
	}
	if len(decisions[0].Reasons) == 0 {
		t.Fatal("expected a recorded rationale")
	}
}
