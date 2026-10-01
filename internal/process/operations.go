package process

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/aeration"
	"github.com/wyw14/cry-97/internal/dosing"
	"github.com/wyw14/cry-97/internal/model"
	"github.com/wyw14/cry-97/internal/settling"
)

// aerationLineContext builds the decision inputs derived from the process
// line: the active batch stage when one exists, plus the command generation.
func (p *Plant) aerationLineContext(lineID model.LineID) (aeration.LineContext, error) {
	p.mu.RLock()
	state, ok := p.lines[lineID]
	p.mu.RUnlock()
	if !ok || state.Emergency {
		return aeration.LineContext{}, errors.New("process line cannot accept aeration control")
	}
	line := aeration.LineContext{LineID: lineID, Stage: state.Stage, Generation: state.Generation}
	if batch, hasBatch := p.ActiveBatch(lineID); hasBatch && batch.IsActive() {
		line.Stage = batch.Stage
		line.HasBatch = true
	}
	return line, nil
}

func (p *Plant) SubmitAeration(ctx context.Context, lineID model.LineID, basinID string, payload []byte) (aeration.SubmitOutcome, error) {
	line, err := p.aerationLineContext(lineID)
	if err != nil {
		return aeration.SubmitOutcome{}, err
	}
	return p.aeration.SubmitWindow(ctx, lineID, basinID, payload, line, p.now())
}

// EvaluateAeration runs one closed-loop blower decision for every healthy
// line. Lines under emergency isolation are left to the interlock stop.
func (p *Plant) EvaluateAeration(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var errs []error
	for _, state := range p.Lines() {
		if state.Emergency {
			continue
		}
		line, err := p.aerationLineContext(state.ID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, _, err := p.aeration.Evaluate(ctx, state.ID, "basin-"+string(state.ID), line, p.now()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (p *Plant) ChangeDose(ctx context.Context, lineID model.LineID, batchID uuid.UUID, chemical string, rate float64) (model.DeviceCommand, error) {
	p.mu.RLock()
	state, ok := p.lines[lineID]
	p.mu.RUnlock()
	if !ok || state.Emergency {
		return model.DeviceCommand{}, errors.New("process line cannot change dosing")
	}
	change, err := dosing.NewChange(lineID, batchID, chemical, rate, state.Generation, p.now())
	if err != nil {
		return model.DeviceCommand{}, err
	}
	return p.dosing.ChangeRate(ctx, change, p.now())
}

func (p *Plant) UpdateBlanket(lineID model.LineID, level float64) (settling.Snapshot, error) {
	state, ok := p.settling[lineID]
	if !ok {
		return settling.Snapshot{}, errors.New("settling line is not found")
	}
	return state.UpdateBlanket(level, 2.8, p.now()), nil
}

func (p *Plant) ReceiveSample(ctx context.Context, sample model.Sample) error {
	if err := p.receiver.Receive(ctx, sample, p.now()); err != nil {
		return err
	}
	return p.sampler.Publish(sample)
}

func (p *Plant) BeginSettling(lineID model.LineID) (settling.Cycle, error) {
	state, ok := p.settling[lineID]
	if !ok {
		return settling.Cycle{}, errors.New("settling line is not found")
	}
	state.BeginCycle(p.now())
	return p.cycles.Start(lineID, p.now())
}

func (p *Plant) AdvanceSettling(lineID model.LineID) (settling.Cycle, error) {
	return p.cycles.Advance(lineID, p.now())
}

func (p *Plant) Alarms() []model.Alarm {
	return p.alarms.All()
}

func (p *Plant) StatusBlowers() []aeration.Blower {
	return p.aeration.Blowers()
}

func (p *Plant) StatusAerationWindows() []aeration.Window {
	return p.aeration.Windows()
}

func (p *Plant) StatusAerationDecisions() []aeration.Decision {
	return p.aeration.Decisions()
}

func (p *Plant) AerationPolicies() []aeration.LinePolicy {
	return p.aeration.Policies()
}

func (p *Plant) AerationPolicy(lineID model.LineID) (aeration.LinePolicy, bool) {
	return p.aeration.Policy(lineID)
}

func (p *Plant) ConfigureAerationPolicy(policy aeration.LinePolicy) error {
	return p.aeration.ConfigurePolicy(policy)
}

func (p *Plant) AcknowledgeAlarm(id uuid.UUID) (model.Alarm, error) {
	return p.alarms.Acknowledge(id)
}

func (p *Plant) RecoverAlarm(id uuid.UUID) (model.Alarm, error) {
	return p.alarms.Recover(id, p.now())
}

func (p *Plant) Clock() time.Time {
	return p.now().UTC()
}
