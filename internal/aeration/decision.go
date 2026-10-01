package aeration

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/wyw14/cry-97/internal/model"
)

// SampleState describes how usable the newest dissolved oxygen reading is.
type SampleState string

const (
	SampleOK      SampleState = "ok"
	SampleMissing SampleState = "missing"
	SampleInvalid SampleState = "invalid"
	SampleStale   SampleState = "stale"
)

// LineContext carries the process-line inputs for one evaluation.
type LineContext struct {
	LineID     model.LineID
	Stage      model.ProcessStage
	HasBatch   bool
	Generation uint64
}

// Decision captures one blower frequency evaluation together with the
// rationale shown on the status page.
type Decision struct {
	LineID            model.LineID       `json:"line_id"`
	BasinID           string             `json:"basin_id"`
	Stage             model.ProcessStage `json:"stage"`
	HasBatch          bool               `json:"has_batch"`
	SampleState       SampleState        `json:"sample_state"`
	SampleValue       float64            `json:"sample_value,omitempty"`
	SampleAgeSeconds  float64            `json:"sample_age_seconds,omitempty"`
	BlowerRunning     bool               `json:"blower_running"`
	PreviousFrequency float64            `json:"previous_frequency"`
	TargetFrequency   float64            `json:"target_frequency"`
	Changed           bool               `json:"changed"`
	TargetMin         float64            `json:"target_min"`
	TargetMax         float64            `json:"target_max"`
	MaxStep           float64            `json:"max_step"`
	Reasons           []string           `json:"reasons"`
	DecidedAt         time.Time          `json:"decided_at"`
}

// Summary returns the primary reason for the decision.
func (d Decision) Summary() string {
	if len(d.Reasons) == 0 {
		return ""
	}
	return d.Reasons[0]
}

// DecisionInput bundles every signal a frequency decision may consider.
type DecisionInput struct {
	Line      LineContext
	Policy    LinePolicy
	Sample    model.Sample
	HasSample bool
	Blower    Blower
	Now       time.Time
}

// Decide computes the next blower frequency. The move is always rate limited
// to the policy max step so the blower can never jump or oscillate widely,
// and falls back to the safe setpoint whenever the DO reading is missing,
// invalid or stale.
func Decide(input DecisionInput) Decision {
	policy := input.Policy
	current := input.Blower.Frequency
	decision := Decision{
		LineID: input.Line.LineID, BasinID: input.Blower.BasinID,
		Stage: input.Line.Stage, HasBatch: input.Line.HasBatch,
		SampleState: SampleMissing, BlowerRunning: input.Blower.Running,
		PreviousFrequency: current, TargetMin: policy.TargetMin,
		TargetMax: policy.TargetMax, MaxStep: policy.MaxStep,
		DecidedAt: input.Now.UTC(),
	}
	sampleState := SampleMissing
	var sampleAge time.Duration
	if input.HasSample {
		sampleAge = input.Now.Sub(input.Sample.ObservedAt)
		switch {
		case !input.Sample.Finite():
			sampleState = SampleInvalid
		case sampleAge > policy.SampleMaxAge:
			sampleState = SampleStale
		default:
			sampleState = SampleOK
		}
	}
	decision.SampleState = sampleState
	if input.HasSample && input.Sample.Finite() {
		decision.SampleValue = input.Sample.Value
		decision.SampleAgeSeconds = math.Round(sampleAge.Seconds()*10) / 10
	}

	desired := current
	switch {
	case !input.Line.HasBatch:
		desired = policy.IdleFrequency
		decision.Reasons = append(decision.Reasons,
			fmt.Sprintf("无活动处理批次，保持待机频率 %.1f Hz", policy.IdleFrequency))
	case input.Line.Stage == model.StageReceiving || input.Line.Stage == model.StageEqualizing:
		desired = policy.SafeFrequency
		decision.Reasons = append(decision.Reasons,
			fmt.Sprintf("批次处于 %s 阶段，保持安全频率 %.1f Hz 预曝气", input.Line.Stage, policy.SafeFrequency))
	case input.Line.Stage != model.StageBiological:
		desired = policy.IdleFrequency
		decision.Reasons = append(decision.Reasons,
			fmt.Sprintf("批次处于 %s 阶段，无曝气需求，保持怠速 %.1f Hz", input.Line.Stage, policy.IdleFrequency))
	default:
		switch sampleState {
		case SampleOK:
			value := input.Sample.Value
			switch {
			case value < policy.TargetMin:
				desired = current + policy.MaxStep
				decision.Reasons = append(decision.Reasons, fmt.Sprintf(
					"溶氧 %.2f mg/L 低于目标下限 %.2f，按最大步长上调 %.1f Hz", value, policy.TargetMin, policy.MaxStep))
			case value > policy.TargetMax:
				desired = current - policy.MaxStep
				decision.Reasons = append(decision.Reasons, fmt.Sprintf(
					"溶氧 %.2f mg/L 高于目标上限 %.2f，按最大步长下调 %.1f Hz", value, policy.TargetMax, policy.MaxStep))
			default:
				decision.Reasons = append(decision.Reasons, fmt.Sprintf(
					"溶氧 %.2f mg/L 处于目标范围 [%.2f, %.2f]，维持当前频率 %.1f Hz", value, policy.TargetMin, policy.TargetMax, current))
			}
		case SampleMissing:
			desired = policy.SafeFrequency
			decision.Reasons = append(decision.Reasons,
				fmt.Sprintf("无有效溶氧样本，保持安全频率 %.1f Hz", policy.SafeFrequency))
		case SampleInvalid:
			desired = policy.SafeFrequency
			decision.Reasons = append(decision.Reasons,
				fmt.Sprintf("溶氧样本无效（非有限值），保持安全频率 %.1f Hz", policy.SafeFrequency))
		case SampleStale:
			desired = policy.SafeFrequency
			decision.Reasons = append(decision.Reasons, fmt.Sprintf(
				"溶氧样本过期（%.0f 秒前，上限 %.0f 秒），保持安全频率 %.1f Hz",
				sampleAge.Seconds(), policy.SampleMaxAge.Seconds(), policy.SafeFrequency))
		}
	}

	limited := clamp(desired, current-policy.MaxStep, current+policy.MaxStep)
	if limited != desired {
		decision.Reasons = append(decision.Reasons, fmt.Sprintf(
			"目标 %.1f Hz 超出单步调节范围，受最大步长 %.1f Hz 限制", desired, policy.MaxStep))
	}
	target := math.Round(clamp(limited, minFrequency, maxFrequency)*10) / 10
	decision.TargetFrequency = target
	decision.Changed = target != current
	return decision
}

func clamp(value, low, high float64) float64 {
	return math.Min(math.Max(value, low), high)
}

// DecisionLog keeps the most recent decision per process line for the
// status page.
type DecisionLog struct {
	mu     sync.RWMutex
	latest map[model.LineID]Decision
}

func NewDecisionLog() *DecisionLog {
	return &DecisionLog{latest: make(map[model.LineID]Decision)}
}

func (l *DecisionLog) Record(decision Decision) {
	l.mu.Lock()
	l.latest[decision.LineID] = decision
	l.mu.Unlock()
}

func (l *DecisionLog) All() []Decision {
	l.mu.RLock()
	defer l.mu.RUnlock()
	result := make([]Decision, 0, len(l.latest))
	for _, decision := range l.latest {
		result = append(result, decision)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].LineID < result[j].LineID })
	return result
}
