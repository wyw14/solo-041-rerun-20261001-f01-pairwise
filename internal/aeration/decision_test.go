package aeration

import (
	"math"
	"testing"
	"time"

	"github.com/wyw14/cry-97/internal/model"
)

var decisionNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func baseInput() DecisionInput {
	return DecisionInput{
		Line:      LineContext{LineID: "line-1", Stage: model.StageBiological, HasBatch: true, Generation: 3},
		Policy:    DefaultPolicy("line-1"),
		Sample:    model.NewSample("line-1", "basin-line-1", model.SampleDissolvedOxygen, 7, 2.5, "mg/L", decisionNow),
		HasSample: true,
		Blower:    Blower{ID: "blower-basin-line-1", BasinID: "basin-line-1", Frequency: 36, Running: true},
		Now:       decisionNow,
	}
}

func TestDecideHoldsInsideTargetRange(t *testing.T) {
	decision := Decide(baseInput())
	if decision.Changed || decision.TargetFrequency != 36 {
		t.Fatalf("expected hold at 36 Hz, got %+v", decision)
	}
	if decision.SampleState != SampleOK {
		t.Fatalf("expected sample state ok, got %s", decision.SampleState)
	}
	if len(decision.Reasons) == 0 {
		t.Fatal("expected a rationale")
	}
}

func TestDecideStepsUpBelowTarget(t *testing.T) {
	input := baseInput()
	input.Sample.Value = 1.6
	decision := Decide(input)
	if !decision.Changed || decision.TargetFrequency != 40 {
		t.Fatalf("expected +4 Hz step to 40, got %+v", decision)
	}
}

func TestDecideStepsDownAboveTarget(t *testing.T) {
	input := baseInput()
	input.Sample.Value = 4.2
	decision := Decide(input)
	if !decision.Changed || decision.TargetFrequency != 32 {
		t.Fatalf("expected -4 Hz step to 32, got %+v", decision)
	}
}

func TestDecideClampsToOperatingRange(t *testing.T) {
	input := baseInput()
	input.Sample.Value = 1.0
	input.Blower.Frequency = 59
	decision := Decide(input)
	if decision.TargetFrequency != 60 {
		t.Fatalf("expected clamp at 60 Hz, got %+v", decision)
	}
}

func TestDecideMissingSampleMovesTowardSafeFrequency(t *testing.T) {
	input := baseInput()
	input.HasSample = false
	input.Blower.Frequency = 48
	decision := Decide(input)
	if decision.SampleState != SampleMissing {
		t.Fatalf("expected missing sample state, got %s", decision.SampleState)
	}
	// Safe frequency is 36 but the move is rate limited to one max step.
	if decision.TargetFrequency != 44 {
		t.Fatalf("expected step-limited move to 44 Hz, got %.1f", decision.TargetFrequency)
	}
}

func TestDecideStaleSampleKeepsSafeSetpoint(t *testing.T) {
	input := baseInput()
	input.Sample.ObservedAt = decisionNow.Add(-3 * time.Minute)
	decision := Decide(input)
	if decision.SampleState != SampleStale {
		t.Fatalf("expected stale sample state, got %s", decision.SampleState)
	}
	if decision.TargetFrequency != 36 {
		t.Fatalf("expected safe frequency 36 Hz, got %.1f", decision.TargetFrequency)
	}
}

func TestDecideInvalidSampleKeepsSafeSetpoint(t *testing.T) {
	input := baseInput()
	input.Sample.Value = math.NaN()
	decision := Decide(input)
	if decision.SampleState != SampleInvalid {
		t.Fatalf("expected invalid sample state, got %s", decision.SampleState)
	}
	if decision.TargetFrequency != 36 {
		t.Fatalf("expected safe frequency 36 Hz, got %.1f", decision.TargetFrequency)
	}
	if math.IsNaN(decision.SampleValue) {
		t.Fatal("decision must not carry a NaN sample value into JSON")
	}
}

func TestDecideWithoutBatchIdles(t *testing.T) {
	input := baseInput()
	input.Line.HasBatch = false
	decision := Decide(input)
	if decision.TargetFrequency != 32 {
		t.Fatalf("expected step-limited move toward idle, got %.1f", decision.TargetFrequency)
	}
}

func TestDecideStageHandling(t *testing.T) {
	cases := []struct {
		stage model.ProcessStage
		want  float64
	}{
		{model.StageReceiving, 36},
		{model.StageEqualizing, 36},
		{model.StageSettling, 32},  // idle 0, step limited from 36
		{model.StageQualified, 32}, // idle 0, step limited from 36
		{model.StageDischarging, 32},
	}
	for _, tc := range cases {
		input := baseInput()
		input.Line.Stage = tc.stage
		decision := Decide(input)
		if decision.TargetFrequency != tc.want {
			t.Fatalf("stage %s: expected %.1f Hz, got %.1f", tc.stage, tc.want, decision.TargetFrequency)
		}
	}
}

func TestDecideNeverOscillatesBeyondMaxStep(t *testing.T) {
	input := baseInput()
	frequency := input.Blower.Frequency
	// Alternate extreme readings; every move must stay within one max step.
	for i := 0; i < 40; i++ {
		if i%2 == 0 {
			input.Sample.Value = 0.5
		} else {
			input.Sample.Value = 8.0
		}
		input.Sample.ObservedAt = decisionNow
		input.Blower.Frequency = frequency
		decision := Decide(input)
		if delta := math.Abs(decision.TargetFrequency - frequency); delta > input.Policy.MaxStep {
			t.Fatalf("iteration %d: move of %.1f Hz exceeds max step", i, delta)
		}
		if decision.TargetFrequency < 0 || decision.TargetFrequency > 60 {
			t.Fatalf("iteration %d: target %.1f Hz outside operating range", i, decision.TargetFrequency)
		}
		frequency = decision.TargetFrequency
	}
}
