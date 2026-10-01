package aeration

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/wyw14/cry-97/internal/model"
)

const (
	// minFrequency and maxFrequency mirror the blower fleet operating envelope.
	minFrequency = 0.0
	maxFrequency = 60.0
)

// LinePolicy tunes the closed-loop blower decision for one process line.
type LinePolicy struct {
	LineID        model.LineID
	TargetMin     float64       // dissolved oxygen target lower bound, mg/L
	TargetMax     float64       // dissolved oxygen target upper bound, mg/L
	MaxStep       float64       // largest frequency change allowed per decision, Hz
	SafeFrequency float64       // fallback setpoint when the DO reading cannot be trusted, Hz
	IdleFrequency float64       // setpoint when the line has no aeration demand, Hz
	SampleMaxAge  time.Duration // oldest acceptable age of the newest DO sample
}

// DefaultPolicy returns the plant-wide baseline policy for a process line.
func DefaultPolicy(lineID model.LineID) LinePolicy {
	return LinePolicy{
		LineID: lineID, TargetMin: 2.0, TargetMax: 3.5, MaxStep: 4,
		SafeFrequency: 36, IdleFrequency: 0, SampleMaxAge: 2 * time.Minute,
	}
}

func (p LinePolicy) Validate() error {
	switch {
	case p.LineID == "":
		return errors.New("policy process line is required")
	case p.TargetMin < 0 || p.TargetMax > 20 || p.TargetMin >= p.TargetMax:
		return fmt.Errorf("dissolved oxygen target range %.2f-%.2f mg/L is invalid", p.TargetMin, p.TargetMax)
	case p.MaxStep <= 0 || p.MaxStep > maxFrequency:
		return fmt.Errorf("max adjustment step %.2f Hz is outside 0-%.0f Hz", p.MaxStep, maxFrequency)
	case p.SafeFrequency < minFrequency || p.SafeFrequency > maxFrequency:
		return fmt.Errorf("safe frequency %.2f Hz is outside the operating range", p.SafeFrequency)
	case p.IdleFrequency < minFrequency || p.IdleFrequency > maxFrequency:
		return fmt.Errorf("idle frequency %.2f Hz is outside the operating range", p.IdleFrequency)
	case p.SampleMaxAge < time.Second || p.SampleMaxAge > time.Hour:
		return errors.New("sample max age must be between 1s and 1h")
	}
	return nil
}

// PolicyBook keeps the configured policy for every process line.
type PolicyBook struct {
	mu       sync.RWMutex
	policies map[model.LineID]LinePolicy
}

func NewPolicyBook(lines []model.LineID) (*PolicyBook, error) {
	if len(lines) == 0 {
		return nil, errors.New("policy book requires at least one process line")
	}
	book := &PolicyBook{policies: make(map[model.LineID]LinePolicy, len(lines))}
	for _, lineID := range lines {
		book.policies[lineID] = DefaultPolicy(lineID)
	}
	return book, nil
}

func (b *PolicyBook) Get(lineID model.LineID) (LinePolicy, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	policy, ok := b.policies[lineID]
	return policy, ok
}

func (b *PolicyBook) Configure(policy LinePolicy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.policies[policy.LineID]; !ok {
		return fmt.Errorf("process line %s is not found", policy.LineID)
	}
	b.policies[policy.LineID] = policy
	return nil
}

func (b *PolicyBook) All() []LinePolicy {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]LinePolicy, 0, len(b.policies))
	for _, policy := range b.policies {
		result = append(result, policy)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].LineID < result[j].LineID })
	return result
}
