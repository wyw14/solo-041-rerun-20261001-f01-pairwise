package aeration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wyw14/cry-97/internal/model"
	"github.com/wyw14/cry-97/internal/sensor"
)

func testPolicy() LinePolicy {
	policy := DefaultPolicy()
	policy.LineID = "line-1"
	return policy
}

func TestDecideRaiseWithinMaxStep(t *testing.T) {
	engine := NewControlEngine()
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	decision := engine.Decide(ControlInput{
		LineID: "line-1", Stage: model.StageBiological,
		HasDO: true, DissolvedOxygen: 1.2, SampleObservedAt: now.Add(-10 * time.Second), SampleValid: true,
		WindowStable: true, CurrentFreq: 36, BlowerRunning: true, Now: now,
	}, testPolicy())

	if decision.Action != ActionRaise {
		t.Fatalf("expected raise, got %s (%s)", decision.Action, decision.Reason)
	}
	if decision.TargetFrequency != 42 {
		t.Fatalf("expected target 42 Hz, got %v", decision.TargetFrequency)
	}
	if !decision.Changed {
		t.Fatal("expected a frequency change")
	}
}

func TestDecideLowerWithinMaxStep(t *testing.T) {
	engine := NewControlEngine()
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	decision := engine.Decide(ControlInput{
		LineID: "line-1", Stage: model.StageBiological,
		HasDO: true, DissolvedOxygen: 5.0, SampleObservedAt: now.Add(-10 * time.Second), SampleValid: true,
		WindowStable: true, CurrentFreq: 36, BlowerRunning: true, Now: now,
	}, testPolicy())

	if decision.Action != ActionLower {
		t.Fatalf("expected lower, got %s (%s)", decision.Action, decision.Reason)
	}
	if decision.TargetFrequency != 30 {
		t.Fatalf("expected target 30 Hz, got %v", decision.TargetFrequency)
	}
}

func TestDecideHoldInRange(t *testing.T) {
	engine := NewControlEngine()
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	decision := engine.Decide(ControlInput{
		LineID: "line-1", Stage: model.StageBiological,
		HasDO: true, DissolvedOxygen: 2.8, SampleObservedAt: now.Add(-10 * time.Second), SampleValid: true,
		WindowStable: true, CurrentFreq: 36, BlowerRunning: true, Now: now,
	}, testPolicy())

	if decision.Action != ActionHold || decision.Changed {
		t.Fatalf("expected unchanged hold, got %s changed=%v", decision.Action, decision.Changed)
	}
}

func TestDecideHoldsSafeFrequencyWhenSampleMissing(t *testing.T) {
	engine := NewControlEngine()
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	decision := engine.Decide(ControlInput{
		LineID: "line-1", Stage: model.StageBiological,
		HasDO: false, WindowStable: true, CurrentFreq: 36, BlowerRunning: true, Now: now,
	}, testPolicy())

	if decision.Changed || decision.Action == ActionRaise || decision.Action == ActionLower {
		t.Fatalf("missing sample must keep safe setting, got %s changed=%v", decision.Action, decision.Changed)
	}
	if decision.TargetFrequency != 36 {
		t.Fatalf("expected safe frequency 36, got %v", decision.TargetFrequency)
	}
}

func TestDecideHoldsSafeFrequencyWhenSampleInvalid(t *testing.T) {
	engine := NewControlEngine()
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	decision := engine.Decide(ControlInput{
		LineID: "line-1", Stage: model.StageBiological,
		HasDO: true, DissolvedOxygen: 25, SampleObservedAt: now.Add(-5 * time.Second), SampleValid: false,
		WindowStable: true, CurrentFreq: 36, BlowerRunning: true, Now: now,
	}, testPolicy())

	if decision.Changed {
		t.Fatalf("invalid sample must not move frequency, got target %v", decision.TargetFrequency)
	}
}

func TestDecideHoldsSafeFrequencyWhenSampleExpired(t *testing.T) {
	engine := NewControlEngine()
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	decision := engine.Decide(ControlInput{
		LineID: "line-1", Stage: model.StageBiological,
		HasDO: true, DissolvedOxygen: 1.0, SampleObservedAt: now.Add(-5 * time.Minute), SampleValid: true,
		WindowStable: true, CurrentFreq: 36, BlowerRunning: true, Now: now,
	}, testPolicy())

	if decision.Changed {
		t.Fatalf("stale low-DO reading must not trigger a raise, got target %v", decision.TargetFrequency)
	}
}

func TestDecideSuspendsBlowerOutsideAerationStages(t *testing.T) {
	engine := NewControlEngine()
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	decision := engine.Decide(ControlInput{
		LineID: "line-1", Stage: model.StageSettling,
		HasDO: true, DissolvedOxygen: 2.8, SampleObservedAt: now.Add(-5 * time.Second), SampleValid: true,
		WindowStable: true, CurrentFreq: 36, BlowerRunning: true, Now: now,
	}, testPolicy())

	if decision.Action != ActionSuspend || decision.TargetFrequency != 0 {
		t.Fatalf("expected suspend to 0 Hz during settling, got %s target=%v", decision.Action, decision.TargetFrequency)
	}
}

func TestDecideThrottlesImmediateReverse(t *testing.T) {
	engine := NewControlEngine()
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	base := ControlInput{
		LineID: "line-1", Stage: model.StageBiological,
		HasDO: true, SampleObservedAt: now.Add(-5 * time.Second), SampleValid: true,
		WindowStable: true, BlowerRunning: true, Now: now,
	}

	first := base
	first.DissolvedOxygen, first.CurrentFreq = 1.0, 36.0
	up := engine.Decide(first, testPolicy())
	if !up.Changed || up.TargetFrequency != 42 {
		t.Fatalf("expected first raise to 42, got %v", up.TargetFrequency)
	}

	second := base
	second.DissolvedOxygen, second.CurrentFreq, second.Now = 5.0, 42.0, now.Add(5*time.Second)
	second.SampleObservedAt = second.Now.Add(-5 * time.Second)
	down := engine.Decide(second, testPolicy())
	if down.Action != ActionThrottled || down.Changed {
		t.Fatalf("expected immediate reverse throttled, got %s changed=%v (%s)", down.Action, down.Changed, down.Reason)
	}

	later := second
	later.Now = now.Add(31 * time.Second)
	later.SampleObservedAt = later.Now.Add(-5 * time.Second)
	released := engine.Decide(later, testPolicy())
	if !released.Changed || released.TargetFrequency != 36 {
		t.Fatalf("expected reverse allowed after cooldown, got %s target=%v", released.Action, released.TargetFrequency)
	}
}

func TestDecideThrottlesConsecutiveLargeSteps(t *testing.T) {
	engine := NewControlEngine()
	policy := testPolicy()
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	base := ControlInput{
		LineID: "line-1", Stage: model.StageBiological,
		HasDO: true, SampleValid: true, WindowStable: true, BlowerRunning: true,
	}

	first := base
	first.DissolvedOxygen, first.CurrentFreq, first.Now, first.SampleObservedAt = 0.5, 36.0, now, now.Add(-5*time.Second)
	up := engine.Decide(first, policy)
	if !up.Changed {
		t.Fatal("expected first large raise to apply")
	}

	second := base
	second.DissolvedOxygen, second.CurrentFreq, second.Now, second.SampleObservedAt = 0.5, 42.0, now.Add(10*time.Second), now.Add(-5*time.Second)
	again := engine.Decide(second, policy)
	if again.Action != ActionThrottled || again.Changed {
		t.Fatalf("expected back-to-back large step throttled, got %s changed=%v", again.Action, again.Changed)
	}
}

func TestDecideResetsDamperAcrossStageChange(t *testing.T) {
	engine := NewControlEngine()
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)

	up := engine.Decide(ControlInput{
		LineID: "line-1", Stage: model.StageBiological,
		HasDO: true, DissolvedOxygen: 1.0, SampleObservedAt: now.Add(-5 * time.Second), SampleValid: true,
		WindowStable: true, CurrentFreq: 36, BlowerRunning: true, Now: now,
	}, testPolicy())
	if !up.Changed || up.TargetFrequency != 42 {
		t.Fatalf("expected raise to 42, got %v", up.TargetFrequency)
	}

	// 阶段变化（如同线新批次重新进入曝气阶段），即使方向相反也不应被旧冷却期拦截。
	down := engine.Decide(ControlInput{
		LineID: "line-1", Stage: model.StageReceiving,
		HasDO: true, DissolvedOxygen: 5.0, SampleObservedAt: now.Add(4 * time.Second), SampleValid: true,
		WindowStable: true, CurrentFreq: 42, BlowerRunning: true, Now: now.Add(5 * time.Second),
	}, testPolicy())
	if !down.Changed || down.Action != ActionLower || down.TargetFrequency != 36 {
		t.Fatalf("expected reverse allowed after stage change, got %s target=%v", down.Action, down.TargetFrequency)
	}
}

func TestDecideActivatesStoppedBlowerAtSafeFrequency(t *testing.T) {
	engine := NewControlEngine()
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	decision := engine.Decide(ControlInput{
		LineID: "line-1", Stage: model.StageBiological,
		HasDO: true, DissolvedOxygen: 0.8, SampleObservedAt: now.Add(-5 * time.Second), SampleValid: true,
		WindowStable: true, CurrentFreq: 0, BlowerRunning: false, Now: now,
	}, testPolicy())

	if decision.Action != ActionActivate {
		t.Fatalf("expected activate, got %s", decision.Action)
	}
	if decision.TargetFrequency != 36 {
		t.Fatalf("expected safe restart at 36 Hz, got %v", decision.TargetFrequency)
	}
}

func TestPolicyValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*LinePolicy)
	}{
		{"empty line", func(p *LinePolicy) { p.LineID = "" }},
		{"inverted range", func(p *LinePolicy) { p.MinDO, p.MaxDO = 4.0, 2.0 }},
		{"unsafe frequency", func(p *LinePolicy) { p.SafeFrequency = 90 }},
		{"zero step", func(p *LinePolicy) { p.MaxStep = 0 }},
		{"zero age", func(p *LinePolicy) { p.SampleMaxAge = 0 }},
		{"bad stage band", func(p *LinePolicy) {
			p.StageBands[model.StageReceiving] = StageBand{MinDO: 3, MaxDO: 1, SafeFrequency: 36}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := testPolicy()
			tc.mutate(&policy)
			if err := policy.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestPolicyBookUpdateAndPerLineDefaults(t *testing.T) {
	book, err := NewPolicyBook(DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	lineTwo := model.LineID("line-2")
	got := book.Get(lineTwo)
	if got.LineID != lineTwo || got.MaxStep != 6 {
		t.Fatalf("unexpected default policy: %+v", got)
	}
	maxStep := 4.0
	updated, err := book.Update(lineTwo, PolicyUpdate{MaxStep: &maxStep})
	if err != nil {
		t.Fatal(err)
	}
	if updated.MaxStep != 4 || book.Get("line-1").MaxStep != 6 {
		t.Fatal("per-line policy update leaked across lines")
	}
}

func TestControllerSubmitsStableWindowAndIssuesCommand(t *testing.T) {
	controller, fleet := newTestController(t)
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	window, command, err := controller.SubmitWindow(
		context.Background(), "line-1", "basin-line-1",
		[]byte(`[1.0,1.1,1.0,1.05,1.0]`), 1, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !window.Stable {
		t.Fatal("expected stable window")
	}
	if command == nil || command.Setpoint != 42 {
		t.Fatalf("expected raise command to 42 Hz, got %+v", command)
	}
	blower, ok := fleet.Get("blower-basin-line-1")
	if !ok || blower.Frequency != 42 || !blower.Running {
		t.Fatalf("fleet not updated: %+v", blower)
	}
	decisions := controller.Decisions()
	if len(decisions) != 1 || decisions[0].Reason == "" {
		t.Fatal("expected one decision with a human-readable reason")
	}
}

func TestControllerThrottlesUnstableWindow(t *testing.T) {
	controller, _ := newTestController(t)
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	_, command, err := controller.SubmitWindow(
		context.Background(), "line-1", "basin-line-1",
		[]byte(`[1.0,3.0,1.0,3.0,1.0]`), 1, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if command != nil {
		t.Fatalf("unstable window must not issue a command, got %+v", command)
	}
	if got := controller.Decisions()[0].Action; got != ActionThrottled {
		t.Fatalf("expected throttled decision, got %s", got)
	}
}

func TestControllerKeepsSafeSettingForInvalidWindow(t *testing.T) {
	controller, _ := newTestController(t)
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	_, command, err := controller.SubmitWindow(
		context.Background(), "line-1", "basin-line-1",
		[]byte(`[25,25,25,25,25]`), 1, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if command != nil {
		t.Fatalf("invalid sensor values must not change frequency, got %+v", command)
	}
}

func TestControllerExpiredSampleTriggersSafeHold(t *testing.T) {
	controller, _ := newTestController(t)
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	command, err := controller.OnSample(context.Background(), model.Sample{
		LineID: "line-1", BasinID: "basin-line-1", Kind: model.SampleDissolvedOxygen,
		Sequence: 1, Value: 1.0, ObservedAt: now.Add(-5 * time.Minute),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if command != nil {
		t.Fatalf("expired signal must hold safe frequency, got command %+v", command)
	}
	if decision := controller.Decisions()[0]; !strings.Contains(decision.Reason, "过期") {
		t.Fatalf("expected expiry reason, got %q", decision.Reason)
	}
}

func TestControllerFreshDoSampleDrivesCommand(t *testing.T) {
	controller, _ := newTestController(t)
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	command, err := controller.OnSample(context.Background(), model.Sample{
		LineID: "line-1", BasinID: "basin-line-1", Kind: model.SampleDissolvedOxygen,
		Sequence: 1, Value: 1.0, ObservedAt: now.Add(-5 * time.Second),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if command == nil || command.Setpoint != 42 {
		t.Fatalf("expected fresh low-DO sample to raise to 42 Hz, got %+v", command)
	}
}

func TestControllerIgnoresNonDoSamples(t *testing.T) {
	controller, _ := newTestController(t)
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	command, err := controller.OnSample(context.Background(), model.Sample{
		LineID: "line-1", BasinID: "basin-line-1", Kind: model.SampleWaterLevel,
		Sequence: 1, Value: 8.0, ObservedAt: now,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if command != nil {
		t.Fatalf("non-DO sample must not drive blower, got %+v", command)
	}
}

func TestControllerRespectsCurrentStage(t *testing.T) {
	stage := model.StageBiological
	controller, _, _ := newTestControllerWithStage(t, func(model.LineID) (model.ProcessStage, uint64, bool) {
		return stage, 1, true
	})
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	if _, command, err := controller.SubmitWindow(
		context.Background(), "line-1", "basin-line-1",
		[]byte(`[2.8,2.8,2.8,2.8,2.8]`), 1, now,
	); err != nil {
		t.Fatal(err)
	} else if command != nil {
		t.Fatal("in-range biological window should hold")
	}

	stage = model.StageSettling
	command, err := controller.OnSample(context.Background(), model.Sample{
		LineID: "line-1", BasinID: "basin-line-1", Kind: model.SampleDissolvedOxygen,
		Sequence: 2, Value: 2.8, ObservedAt: now,
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if command == nil || command.Setpoint != 0 {
		t.Fatalf("settling stage must suspend blower, got %+v", command)
	}
}

func newTestController(t *testing.T) (*Controller, *BlowerFleet) {
	t.Helper()
	controller, fleet, _ := newTestControllerWithStage(t, func(model.LineID) (model.ProcessStage, uint64, bool) {
		return model.StageBiological, 1, true
	})
	return controller, fleet
}

func newTestControllerWithStage(t *testing.T, lineCtx LineContextFunc) (*Controller, *BlowerFleet, *sensor.Sampler) {
	t.Helper()
	decoder, err := sensor.NewBatchDecoder(24)
	if err != nil {
		t.Fatal(err)
	}
	windows, err := NewWindowBook(10)
	if err != nil {
		t.Fatal(err)
	}
	fleet, err := NewBlowerFleet([]Blower{{ID: "blower-basin-line-1", BasinID: "basin-line-1", Frequency: 36, Running: true}})
	if err != nil {
		t.Fatal(err)
	}
	policies, err := NewPolicyBook(DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	samplerState := sensor.NewSampler()
	controller, err := NewController(decoder, windows, fleet, policies, samplerState, lineCtx)
	if err != nil {
		t.Fatal(err)
	}
	return controller, fleet, samplerState
}
