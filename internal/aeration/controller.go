package aeration

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/model"
	"github.com/wyw14/cry-97/internal/sensor"
)

type Controller struct {
	decoder   *sensor.BatchDecoder
	windows   *WindowBook
	blowers   *BlowerFleet
	policies  *PolicyBook
	decisions *DecisionLog
	mu        sync.RWMutex
	latest    map[model.LineID]model.Sample
}

func NewController(decoder *sensor.BatchDecoder, windows *WindowBook, blowers *BlowerFleet, policies *PolicyBook) (*Controller, error) {
	if decoder == nil || windows == nil || blowers == nil || policies == nil {
		return nil, errors.New("aeration controller dependencies are required")
	}
	return &Controller{
		decoder: decoder, windows: windows, blowers: blowers, policies: policies,
		decisions: NewDecisionLog(), latest: make(map[model.LineID]model.Sample),
	}, nil
}

// ObserveSample records the newest dissolved oxygen reading for a line. Older
// observations arriving late never overwrite a fresher reading.
func (c *Controller) ObserveSample(sample model.Sample) {
	if sample.LineID == "" || sample.Kind != model.SampleDissolvedOxygen || sample.ObservedAt.IsZero() {
		return
	}
	c.mu.Lock()
	existing, ok := c.latest[sample.LineID]
	if !ok || sample.ObservedAt.After(existing.ObservedAt) {
		c.latest[sample.LineID] = sample
	}
	c.mu.Unlock()
}

func (c *Controller) latestSample(lineID model.LineID) (model.Sample, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	sample, ok := c.latest[lineID]
	return sample, ok
}

// SubmitOutcome is the result of ingesting one dissolved oxygen window.
type SubmitOutcome struct {
	Window   Window               `json:"window"`
	Decision Decision             `json:"decision"`
	Command  *model.DeviceCommand `json:"command,omitempty"`
}

func (c *Controller) SubmitWindow(ctx context.Context, lineID model.LineID, basinID string, payload []byte, line LineContext, now time.Time) (SubmitOutcome, error) {
	if err := ctx.Err(); err != nil {
		return SubmitOutcome{}, err
	}
	values, err := c.decoder.Decode(payload)
	if err != nil {
		return SubmitOutcome{}, err
	}
	window, err := c.windows.Store(basinID, values)
	if err != nil {
		return SubmitOutcome{}, err
	}
	// The newest value in the window is a fresh dissolved oxygen observation.
	c.ObserveSample(model.NewSample(lineID, basinID, model.SampleDissolvedOxygen, 0, values[len(values)-1], "mg/L", now))
	decision, command, err := c.Evaluate(ctx, lineID, basinID, line, now)
	if err != nil {
		return SubmitOutcome{}, err
	}
	return SubmitOutcome{Window: window, Decision: decision, Command: command}, nil
}

// Evaluate runs one closed-loop decision for a basin: batch stage, newest
// valid dissolved oxygen sample and current blower state in, one rate-limited
// frequency setpoint out. Every evaluation is recorded with its rationale.
func (c *Controller) Evaluate(ctx context.Context, lineID model.LineID, basinID string, line LineContext, now time.Time) (Decision, *model.DeviceCommand, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, nil, err
	}
	policy, ok := c.policies.Get(lineID)
	if !ok {
		return Decision{}, nil, errors.New("aeration policy is not configured for the process line")
	}
	blower, ok := c.blowers.Get("blower-" + basinID)
	if !ok {
		return Decision{}, nil, errors.New("blower is not found for the basin")
	}
	sample, hasSample := c.latestSample(lineID)
	line.LineID = lineID
	decision := Decide(DecisionInput{
		Line: line, Policy: policy, Sample: sample, HasSample: hasSample, Blower: blower, Now: now,
	})
	c.decisions.Record(decision)
	if !decision.Changed {
		return decision, nil, nil
	}
	command, err := model.NewDeviceCommand(lineID, uuid.New(), blower.ID, model.DeviceBlower, line.Generation, "set", now)
	if err != nil {
		return Decision{}, nil, err
	}
	command.Setpoint = decision.TargetFrequency
	command.Metadata["reason"] = decision.Summary()
	if _, err := c.blowers.Apply(command, now); err != nil {
		return Decision{}, nil, err
	}
	return decision, &command, nil
}

func (c *Controller) Windows() []Window {
	return c.windows.All()
}

func (c *Controller) Blowers() []Blower {
	return c.blowers.Snapshot()
}

func (c *Controller) Decisions() []Decision {
	return c.decisions.All()
}

func (c *Controller) Policies() []LinePolicy {
	return c.policies.All()
}

func (c *Controller) Policy(lineID model.LineID) (LinePolicy, bool) {
	return c.policies.Get(lineID)
}

func (c *Controller) ConfigurePolicy(policy LinePolicy) error {
	return c.policies.Configure(policy)
}
