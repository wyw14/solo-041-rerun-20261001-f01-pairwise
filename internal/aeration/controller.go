package aeration

import (
	"context"
	"errors"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-97/internal/model"
	"github.com/wyw14/cry-97/internal/sensor"
)

// 溶解氧读数物理有效区间（mg/L），超出即视为无效传感器信号。
const (
	minValidDO = 0.0
	maxValidDO = 20.0
)

// LineContextFunc 返回某条处理线当前的批次阶段与命令代际。
type LineContextFunc func(model.LineID) (stage model.ProcessStage, generation uint64, ok bool)

type Controller struct {
	mu        sync.Mutex
	decoder   *sensor.BatchDecoder
	windows   *WindowBook
	blowers   *BlowerFleet
	policies  *PolicyBook
	engine    *ControlEngine
	sampler   *sensor.Sampler
	lineCtx   LineContextFunc
	readings  map[model.LineID]doReading
	decisions map[model.LineID]Decision
}

type doReading struct {
	value float64
	at    time.Time
	valid bool
}

func NewController(
	decoder *sensor.BatchDecoder,
	windows *WindowBook,
	blowers *BlowerFleet,
	policies *PolicyBook,
	sampler *sensor.Sampler,
	lineCtx LineContextFunc,
) (*Controller, error) {
	if decoder == nil || windows == nil || blowers == nil || policies == nil || lineCtx == nil {
		return nil, errors.New("aeration controller dependencies are required")
	}
	return &Controller{
		decoder: decoder, windows: windows, blowers: blowers, policies: policies,
		engine: NewControlEngine(), sampler: sampler, lineCtx: lineCtx,
		readings: make(map[model.LineID]doReading), decisions: make(map[model.LineID]Decision),
	}, nil
}

func (c *Controller) SubmitWindow(ctx context.Context, lineID model.LineID, basinID string, payload []byte, generation uint64, now time.Time) (Window, *model.DeviceCommand, error) {
	if err := ctx.Err(); err != nil {
		return Window{}, nil, err
	}
	values, err := c.decoder.Decode(payload)
	if err != nil {
		return Window{}, nil, err
	}
	if _, _, ok := c.lineCtx(lineID); !ok {
		return Window{}, nil, errors.New("process line is unknown")
	}
	window, err := c.windows.Store(basinID, values, now)
	if err != nil {
		return Window{}, nil, err
	}
	c.rememberWindow(lineID, window)
	command, err := c.evaluate(ctx, lineID, basinID, generation, now)
	if err != nil {
		return Window{}, nil, err
	}
	return window, command, nil
}

// OnSample 处理采样器推送给订阅者的单个溶解氧样本，使其同样触发频率决策。
// 这样带观测时间戳的 DO 样本过期或无效时，风机也会回到安全设定。
func (c *Controller) OnSample(ctx context.Context, sample model.Sample, now time.Time) (*model.DeviceCommand, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if sample.Kind != model.SampleDissolvedOxygen {
		return nil, nil
	}
	_, generation, ok := c.lineCtx(sample.LineID)
	if !ok {
		return nil, errors.New("process line is unknown")
	}
	basinID := sample.BasinID
	if basinID == "" {
		basinID = "basin-" + string(sample.LineID)
	}
	c.rememberSample(sample)
	return c.evaluate(ctx, sample.LineID, basinID, generation, now)
}

// Configure 更新某条处理线的目标范围、安全频率或最大调节步长。
func (c *Controller) Configure(lineID model.LineID, patch PolicyUpdate) (LinePolicy, error) {
	return c.policies.Update(lineID, patch)
}

func (c *Controller) Windows() []Window {
	return c.windows.All()
}

func (c *Controller) Blowers() []Blower {
	return c.blowers.Snapshot()
}

func (c *Controller) Policies() []PolicyView {
	policies := c.policies.All()
	views := make([]PolicyView, 0, len(policies))
	for _, policy := range policies {
		views = append(views, policy.View())
	}
	return views
}

// Decisions 返回每条处理线最近一次频率决策，按处理线排序。
func (c *Controller) Decisions() []Decision {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]Decision, 0, len(c.decisions))
	for _, decision := range c.decisions {
		result = append(result, decision)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].LineID < result[j].LineID })
	return result
}

// evaluate 汇总阶段、最近 DO 样本、窗口稳定性与风机状态，执行一次决策并按需下发命令。
func (c *Controller) evaluate(ctx context.Context, lineID model.LineID, basinID string, generation uint64, now time.Time) (*model.DeviceCommand, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stage, _, ok := c.lineCtx(lineID)
	if !ok {
		return nil, errors.New("process line is unknown")
	}
	blower, exists := c.resolveBlower(lineID, basinID)
	if !exists {
		return nil, errors.New("blower for basin is unknown")
	}
	reading, hasReading := c.latestDissolvedOxygen(lineID)
	window, hasWindow := c.windows.Get(basinID)
	input := ControlInput{
		LineID: lineID, Stage: stage,
		HasDO: hasReading, DissolvedOxygen: reading.value, SampleObservedAt: reading.at,
		SampleValid:  hasReading && reading.valid,
		WindowStable: !hasWindow || window.Stable,
		CurrentFreq:  blower.Frequency, BlowerRunning: blower.Running, Now: now,
	}
	decision := c.engine.Decide(input, c.policies.Get(lineID))

	var command *model.DeviceCommand
	if decision.Changed {
		issued, err := model.NewDeviceCommand(lineID, uuid.New(), blower.ID, model.DeviceBlower, generation, "set", now)
		if err != nil {
			return nil, err
		}
		issued.Setpoint = decision.TargetFrequency
		if _, err := c.blowers.Apply(issued, now); err != nil {
			return nil, err
		}
		command = &issued
	}
	c.mu.Lock()
	c.decisions[lineID] = decision
	c.mu.Unlock()
	return command, nil
}

func (c *Controller) resolveBlower(lineID model.LineID, basinID string) (Blower, bool) {
	if blower, ok := c.blowers.Get("blower-" + basinID); ok {
		return blower, true
	}
	return c.blowers.Get("blower-basin-" + string(lineID))
}

// rememberWindow 把本批次窗口的平均溶氧作为最近样本记录下来；较旧的窗口不覆盖较新的读数。
func (c *Controller) rememberWindow(lineID model.LineID, window Window) {
	valid := true
	for _, value := range window.Values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < minValidDO || value > maxValidDO {
			valid = false
			break
		}
	}
	c.mu.Lock()
	if current, ok := c.readings[lineID]; ok && current.at.After(window.StoredAt) {
		c.mu.Unlock()
		return
	}
	c.readings[lineID] = doReading{value: window.Average, at: window.StoredAt, valid: valid}
	c.mu.Unlock()
}

// rememberSample 记录采样器上报的单个溶解氧样本；较旧的读数不覆盖较新的。
func (c *Controller) rememberSample(sample model.Sample) {
	valid := sample.Finite() && sample.Value >= minValidDO && sample.Value <= maxValidDO
	observed := sample.ObservedAt.UTC()
	c.mu.Lock()
	if current, ok := c.readings[sample.LineID]; ok && current.at.After(observed) {
		c.mu.Unlock()
		return
	}
	c.readings[sample.LineID] = doReading{value: sample.Value, at: observed, valid: valid}
	c.mu.Unlock()
}

// latestDissolvedOxygen 合并窗口批次与采样器上报的 DO 样本，取观测时间最新者。
func (c *Controller) latestDissolvedOxygen(lineID model.LineID) (doReading, bool) {
	c.mu.Lock()
	best, ok := c.readings[lineID]
	c.mu.Unlock()
	if c.sampler == nil {
		return best, ok
	}
	for _, sample := range c.sampler.LineSnapshot(lineID) {
		if sample.Kind != model.SampleDissolvedOxygen {
			continue
		}
		valid := sample.Finite() && sample.Value >= minValidDO && sample.Value <= maxValidDO
		if !ok || sample.ObservedAt.After(best.at) {
			best = doReading{value: sample.Value, at: sample.ObservedAt.UTC(), valid: valid}
			ok = true
		}
	}
	return best, ok
}
