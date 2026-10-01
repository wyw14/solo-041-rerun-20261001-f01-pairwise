package aeration

import (
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/wyw14/cry-97/internal/model"
)

// 风机允许的频率工作区间，与 BlowerFleet.Apply 的校验保持一致。
const (
	MinFrequency = 0.0
	MaxFrequency = 60.0
)

// Action 描述一次曝气频率决策的动作类别。
type Action string

const (
	ActionHold      Action = "hold"      // 维持当前安全频率
	ActionRaise     Action = "raise"     // 提高频率
	ActionLower     Action = "lower"     // 降低频率
	ActionActivate  Action = "activate"  // 风机停止但需要供气，按安全频率启动
	ActionSuspend   Action = "suspend"   // 非曝气阶段，停止供气
	ActionThrottled Action = "throttled" // 期望调节但被防抖/限幅规则节流
)

// StageBand 是某个批次阶段专用的溶解氧目标范围与安全频率。
type StageBand struct {
	MinDO         float64 `json:"min_do"`
	MaxDO         float64 `json:"max_do"`
	SafeFrequency float64 `json:"safe_frequency"`
}

// LinePolicy 是每条处理线可独立配置的曝气策略。
type LinePolicy struct {
	LineID        model.LineID                     `json:"line_id"`
	MinDO         float64                          `json:"min_do"`
	MaxDO         float64                          `json:"max_do"`
	SafeFrequency float64                          `json:"safe_frequency"`
	MaxStep       float64                          `json:"max_step_hz"`
	SampleMaxAge  time.Duration                    `json:"-"`
	StageBands    map[model.ProcessStage]StageBand `json:"stage_bands,omitempty"`
}

// PolicyView 是 LinePolicy 的 JSON 视图（Duration 不直接序列化）。
type PolicyView struct {
	LineID        model.LineID                     `json:"line_id"`
	MinDO         float64                          `json:"min_do"`
	MaxDO         float64                          `json:"max_do"`
	SafeFrequency float64                          `json:"safe_frequency"`
	MaxStep       float64                          `json:"max_step_hz"`
	SampleMaxAge  string                           `json:"sample_max_age"`
	StageBands    map[model.ProcessStage]StageBand `json:"stage_bands,omitempty"`
}

func (p LinePolicy) View() PolicyView {
	return PolicyView{
		LineID: p.LineID, MinDO: p.MinDO, MaxDO: p.MaxDO, SafeFrequency: p.SafeFrequency,
		MaxStep: p.MaxStep, SampleMaxAge: p.SampleMaxAge.String(), StageBands: cloneStageBands(p.StageBands),
	}
}

// PolicyUpdate 是状态页/API 调整每线策略时的入参。
type PolicyUpdate struct {
	MinDO         *float64                         `json:"min_do,omitempty"`
	MaxDO         *float64                         `json:"max_do,omitempty"`
	SafeFrequency *float64                         `json:"safe_frequency,omitempty"`
	MaxStep       *float64                         `json:"max_step_hz,omitempty"`
	SampleMaxAge  *time.Duration                   `json:"sample_max_age,omitempty"`
	StageBands    map[model.ProcessStage]StageBand `json:"stage_bands,omitempty"`
}

// PolicyBook 保存每条处理线的曝气策略，缺省配置由 DefaultPolicy 提供。
type PolicyBook struct {
	mu       sync.RWMutex
	defaults LinePolicy
	policies map[model.LineID]LinePolicy
}

func NewPolicyBook(defaults LinePolicy) (*PolicyBook, error) {
	// 默认策略是模板，LineID 在 Get 时按线填充，校验时不要求其存在。
	template := defaults
	template.LineID = "default"
	if err := template.Validate(); err != nil {
		return nil, err
	}
	return &PolicyBook{defaults: defaults, policies: make(map[model.LineID]LinePolicy)}, nil
}

// DefaultPolicy 是安全侧保守的缺省策略：溶氧 2.0~3.5 mg/L，
// 传感器失效时保持 36 Hz，单次最多调节 6 Hz，样本 2 分钟内有效。
func DefaultPolicy() LinePolicy {
	return LinePolicy{
		MinDO: 2.0, MaxDO: 3.5, SafeFrequency: 36, MaxStep: 6,
		SampleMaxAge: 2 * time.Minute,
		StageBands: map[model.ProcessStage]StageBand{
			model.StageBiological: {MinDO: 2.0, MaxDO: 3.5, SafeFrequency: 36},
		},
	}
}

func (b *PolicyBook) Get(lineID model.LineID) LinePolicy {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if policy, ok := b.policies[lineID]; ok {
		return policy
	}
	policy := b.defaults
	policy.LineID = lineID
	return policy
}

func (b *PolicyBook) Update(lineID model.LineID, patch PolicyUpdate) (LinePolicy, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	policy, ok := b.policies[lineID]
	if !ok {
		policy = b.defaults
		policy.LineID = lineID
	}
	if patch.MinDO != nil {
		policy.MinDO = *patch.MinDO
	}
	if patch.MaxDO != nil {
		policy.MaxDO = *patch.MaxDO
	}
	if patch.SafeFrequency != nil {
		policy.SafeFrequency = *patch.SafeFrequency
	}
	if patch.MaxStep != nil {
		policy.MaxStep = *patch.MaxStep
	}
	if patch.SampleMaxAge != nil {
		policy.SampleMaxAge = *patch.SampleMaxAge
	}
	if patch.StageBands != nil {
		policy.StageBands = cloneStageBands(patch.StageBands)
	}
	if err := policy.Validate(); err != nil {
		return LinePolicy{}, err
	}
	b.policies[lineID] = policy
	return policy, nil
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

func (p LinePolicy) Validate() error {
	if p.LineID == "" {
		return errors.New("aeration policy requires a line id")
	}
	if p.MinDO <= 0 || p.MaxDO <= 0 || p.MinDO >= p.MaxDO {
		return errors.New("aeration dissolved oxygen range is invalid")
	}
	if p.SafeFrequency < MinFrequency || p.SafeFrequency > MaxFrequency {
		return errors.New("aeration safe frequency is outside operating range")
	}
	if p.MaxStep <= 0 || p.MaxStep > MaxFrequency {
		return errors.New("aeration maximum step must be positive and within operating range")
	}
	if p.SampleMaxAge <= 0 {
		return errors.New("aeration sample max age must be positive")
	}
	for stage, band := range p.StageBands {
		if band.MinDO <= 0 || band.MaxDO <= 0 || band.MinDO >= band.MaxDO {
			return errors.New("aeration stage dissolved oxygen range is invalid: " + string(stage))
		}
		if band.SafeFrequency < MinFrequency || band.SafeFrequency > MaxFrequency {
			return errors.New("aeration stage safe frequency is outside operating range: " + string(stage))
		}
	}
	return nil
}

// BandFor 返回指定阶段生效的目标范围与安全频率；未配置阶段覆盖时使用基础范围。
func (p LinePolicy) BandFor(stage model.ProcessStage) (minDO, maxDO, safe float64) {
	if band, ok := p.StageBands[stage]; ok {
		return band.MinDO, band.MaxDO, band.SafeFrequency
	}
	return p.MinDO, p.MaxDO, p.SafeFrequency
}

func cloneStageBands(source map[model.ProcessStage]StageBand) map[model.ProcessStage]StageBand {
	if source == nil {
		return nil
	}
	cloned := make(map[model.ProcessStage]StageBand, len(source))
	for stage, band := range source {
		cloned[stage] = band
	}
	return cloned
}

// ControlInput 汇集一次频率决策所需的全部上下文。
type ControlInput struct {
	LineID           model.LineID
	Stage            model.ProcessStage
	HasDO            bool      // 是否存在最近溶解氧样本
	DissolvedOxygen  float64   // 最近一次有效 DO 读数（mg/L）
	SampleObservedAt time.Time // 最近 DO 样本的观测时间
	SampleValid      bool      // 读数是否有限且在合理物理范围内
	WindowStable     bool      // 本批次采样窗口是否稳定
	CurrentFreq      float64   // 风机当前频率
	BlowerRunning    bool      // 风机当前是否运行
	Now              time.Time
}

// Decision 记录一次频率决策的结果与理由，供状态页展示与审计。
type Decision struct {
	LineID           model.LineID       `json:"line_id"`
	Stage            model.ProcessStage `json:"stage"`
	Action           Action             `json:"action"`
	TargetFrequency  float64            `json:"target_frequency"`
	CurrentFrequency float64            `json:"current_frequency"`
	DesiredFrequency float64            `json:"desired_frequency"`
	DissolvedOxygen  *float64           `json:"dissolved_oxygen,omitempty"`
	SampleObservedAt *time.Time         `json:"sample_observed_at,omitempty"`
	MinDO            float64            `json:"min_do"`
	MaxDO            float64            `json:"max_do"`
	MaxStep          float64            `json:"max_step_hz"`
	Changed          bool               `json:"changed"`
	Reason           string             `json:"reason"`
	DecidedAt        time.Time          `json:"decided_at"`
	safety           bool               // 安全动作（停机/安全回退/安全启动），跳过步长与防抖约束
}

// damper 记录每个方向的连续调节情况，用于抑制来回振荡。
type damper struct {
	lastDirection int                // +1 升频，-1 降频，0 停止
	lastChangeAt  time.Time          // 最近一次实际改变频率的时刻
	lastStage     model.ProcessStage // 上次决策所处的批次阶段，阶段切换时重置防抖
}

// ControlEngine 在策略约束下把控制上下文转成带限幅/防抖的频率决策。
type ControlEngine struct {
	mu              sync.Mutex
	reverseCooldown time.Duration
	largeStepRatio  float64
	largeCooldown   time.Duration
	dampers         map[model.LineID]damper
}

func NewControlEngine() *ControlEngine {
	return &ControlEngine{
		reverseCooldown: 30 * time.Second,
		largeStepRatio:  0.6, // 实际步长超过最大步长 60% 视为大幅调节
		largeCooldown:   45 * time.Second,
		dampers:         make(map[model.LineID]damper),
	}
}

// Decide 执行一次决策。它不会直接操作风机，只描述应当下发的目标频率。
func (e *ControlEngine) Decide(input ControlInput, policy LinePolicy) Decision {
	now := input.Now.UTC()
	decision := Decision{
		LineID: input.LineID, Stage: input.Stage, Action: ActionHold,
		CurrentFrequency: input.CurrentFreq, DesiredFrequency: input.CurrentFreq,
		TargetFrequency: input.CurrentFreq, MinDO: policy.MinDO, MaxDO: policy.MaxDO,
		MaxStep: policy.MaxStep, DecidedAt: now,
	}
	minDO, maxDO, safe := policy.BandFor(input.Stage)
	decision.MinDO, decision.MaxDO = minDO, maxDO
	if input.HasDO && input.SampleValid {
		do := input.DissolvedOxygen
		decision.DissolvedOxygen = &do
		observed := input.SampleObservedAt.UTC()
		decision.SampleObservedAt = &observed
	}

	// 非曝气阶段：不供气，避免在沉淀/排放等阶段扰动水体。
	if !aerationActive(input.Stage) {
		decision.DesiredFrequency = 0
		decision.Action = ActionSuspend
		decision.safety = true
		decision.Reason = stageSuspendReason(input.Stage)
		return e.finalize(policy, decision, input, now)
	}

	// 传感器缺失、无效或过期：保持安全设定，不依据不可信信号调节。
	if !input.HasDO {
		return e.safeHold(policy, decision, input, safe, "没有可用的溶解氧样本，保持安全频率")
	}
	if !input.SampleValid {
		return e.safeHold(policy, decision, input, safe, "溶解氧样本无效（读数超出物理范围），保持安全频率")
	}
	if age := now.Sub(input.SampleObservedAt.UTC()); age > policy.SampleMaxAge {
		decision.Reason = "溶解氧样本已过期 " + age.Round(time.Second).String() + "，保持安全频率"
		return e.safeHold(policy, decision, input, safe, decision.Reason)
	}

	switch {
	case input.DissolvedOxygen < minDO:
		decision.DesiredFrequency = clamp(input.CurrentFreq+policy.MaxStep, MinFrequency, MaxFrequency)
		decision.Action = ActionRaise
		decision.Reason = "溶解氧 " + formatFloat(input.DissolvedOxygen) + " 低于目标下限 " + formatFloat(minDO) + "，在最大步长内提高频率"
	case input.DissolvedOxygen > maxDO:
		decision.DesiredFrequency = clamp(input.CurrentFreq-policy.MaxStep, MinFrequency, MaxFrequency)
		decision.Action = ActionLower
		decision.Reason = "溶解氧 " + formatFloat(input.DissolvedOxygen) + " 高于目标上限 " + formatFloat(maxDO) + "，在最大步长内降低频率"
	default:
		decision.DesiredFrequency = input.CurrentFreq
		decision.Action = ActionHold
		decision.Reason = "溶解氧 " + formatFloat(input.DissolvedOxygen) + " 位于目标范围 " + formatFloat(minDO) + "~" + formatFloat(maxDO) + "，保持当前频率"
	}
	if !input.WindowStable && decision.Action != ActionHold {
		decision.Action = ActionThrottled
		decision.Reason += "；采样窗口尚未稳定，本次暂缓调节"
		decision.DesiredFrequency = input.CurrentFreq
	}
	if !input.BlowerRunning && decision.Action == ActionRaise {
		// 风机已停止却出现低氧：安全优先，直接按安全频率恢复供气。
		decision.DesiredFrequency = safe
		decision.Action = ActionActivate
		decision.safety = true
		decision.Reason = "风机处于停止状态且溶解氧偏低，按安全频率 " + formatFloat(safe) + " Hz 重启"
	}
	return e.finalize(policy, decision, input, now)
}

// safeHold 在信号不可信时把风机带到/保持在策略的安全频率。
func (e *ControlEngine) safeHold(policy LinePolicy, decision Decision, input ControlInput, safe float64, reason string) Decision {
	decision.DesiredFrequency = safe
	decision.Reason = reason
	decision.safety = true
	if !input.BlowerRunning {
		decision.Action = ActionActivate
		decision.Reason += "；风机停止，按安全频率启动"
	}
	return e.finalize(policy, decision, input, decision.DecidedAt)
}

// finalize 应用最大步长限幅与方向/大幅调节防抖，并判定是否真的下发命令。
// 标记为 safety 的动作（停机、安全回退、安全启动）直接执行，不受步长与防抖限制。
func (e *ControlEngine) finalize(policy LinePolicy, decision Decision, input ControlInput, now time.Time) Decision {
	desired := clamp(decision.DesiredFrequency, MinFrequency, MaxFrequency)
	decision.DesiredFrequency = desired
	delta := desired - input.CurrentFreq
	if nearlyZero(delta) {
		decision.TargetFrequency = input.CurrentFreq
		decision.Changed = false
		if decision.safety && decision.Action == ActionSuspend {
			// 已停机，无需再下发。
			decision.Action = ActionHold
		}
		return decision
	}

	// 安全动作：直接到目标，不受步长与防抖限制；但仍记录方向，
	// 防止安全动作之后紧接着的闭环调节立即反向甩动。
	if decision.safety {
		direction := 1
		if delta < 0 {
			direction = -1
		}
		e.mu.Lock()
		e.dampers[input.LineID] = damper{lastDirection: direction, lastChangeAt: now, lastStage: input.Stage}
		e.mu.Unlock()
		decision.TargetFrequency = desired
		decision.Changed = true
		return decision
	}

	direction := 1
	if delta < 0 {
		direction = -1
	}
	step := clampMagnitude(delta, policy.MaxStep)

	e.mu.Lock()
	state := e.dampers[input.LineID]
	// 进入新阶段（含同线新批次重新进入曝气阶段）时清除历史防抖，避免跨阶段误节流。
	if state.lastStage != "" && state.lastStage != input.Stage {
		state = damper{}
	}
	throttle := ""
	if state.lastDirection != 0 && direction != state.lastDirection && now.Sub(state.lastChangeAt) < e.reverseCooldown {
		throttle = "与上次调节方向相反且处于反向冷却期，暂缓以避免振荡"
	} else if direction == state.lastDirection && absFloat(step) >= policy.MaxStep*e.largeStepRatio && now.Sub(state.lastChangeAt) < e.largeCooldown {
		// 只约束同方向的连续大幅动作；反向已由上面的冷却期处理，避免叠加导致放行过晚。
		throttle = "连续大幅调节间隔不足，本次暂缓以避免振荡"
	}
	if throttle != "" {
		e.mu.Unlock()
		decision.Action = ActionThrottled
		decision.TargetFrequency = input.CurrentFreq
		decision.Changed = false
		decision.Reason += "；" + throttle
		return decision
	}
	e.dampers[input.LineID] = damper{lastDirection: direction, lastChangeAt: now, lastStage: input.Stage}
	e.mu.Unlock()

	target := clamp(input.CurrentFreq+step, MinFrequency, MaxFrequency)
	decision.TargetFrequency = target
	decision.Changed = !nearlyZero(target - input.CurrentFreq)
	if absFloat(target-input.CurrentFreq) < absFloat(delta)-1e-9 {
		decision.Reason += "；目标变化已按最大步长 " + formatFloat(policy.MaxStep) + " Hz 限幅"
	}
	return decision
}

// aerationActive 判定某批次阶段是否需要曝气。
func aerationActive(stage model.ProcessStage) bool {
	switch stage {
	case model.StageReceiving, model.StageEqualizing, model.StageBiological:
		return true
	default:
		return false
	}
}

func stageSuspendReason(stage model.ProcessStage) string {
	switch stage {
	case model.StageSettling, model.StageQualified, model.StageDischarging:
		return "当前处于 " + string(stage) + " 阶段，停止曝气供气"
	case model.StageClosed, model.StageIsolated:
		return "处理线已 " + string(stage) + "，停止曝气供气"
	default:
		return "当前阶段 " + string(stage) + " 不需要曝气，保持停机"
	}
}

func clamp(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func clampMagnitude(value, maxMagnitude float64) float64 {
	if value > maxMagnitude {
		return maxMagnitude
	}
	if value < -maxMagnitude {
		return -maxMagnitude
	}
	return value
}

func absFloat(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

func nearlyZero(value float64) bool {
	return absFloat(value) < 1e-9
}

func formatFloat(value float64) string {
	// 统一保留两位小数，避免决策理由里出现长尾浮点。
	return strconv.FormatFloat(value, 'f', 2, 64)
}
