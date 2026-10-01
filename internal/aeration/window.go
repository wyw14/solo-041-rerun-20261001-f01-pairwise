package aeration

import (
	"errors"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/wyw14/cry-97/internal/sensor"
)

type Window struct {
	BasinID  string    `json:"basin_id"`
	Values   []float64 `json:"values"`
	Average  float64   `json:"average"`
	Stable   bool      `json:"stable"`
	StoredAt time.Time `json:"stored_at"`
}

type storedWindow struct {
	values []float64
	at     time.Time
}

type WindowBook struct {
	mu      sync.RWMutex
	limit   int
	windows map[string]storedWindow
}

func NewWindowBook(limit int) (*WindowBook, error) {
	if limit < 3 {
		return nil, errors.New("aeration window must contain at least three values")
	}
	return &WindowBook{limit: limit, windows: make(map[string]storedWindow)}, nil
}

func (b *WindowBook) Store(basinID string, values []float64, now time.Time) (Window, error) {
	if basinID == "" || len(values) == 0 {
		return Window{}, errors.New("basin and samples are required")
	}
	start := 0
	if len(values) > b.limit {
		start = len(values) - b.limit
	}
	owned := sensor.CloneValues(values[start:])
	stamp := now.UTC()
	b.mu.Lock()
	b.windows[basinID] = storedWindow{values: owned, at: stamp}
	b.mu.Unlock()
	return evaluate(basinID, owned, stamp), nil
}

func (b *WindowBook) All() []Window {
	b.mu.RLock()
	result := make([]Window, 0, len(b.windows))
	for basinID, window := range b.windows {
		result = append(result, evaluate(basinID, append([]float64(nil), window.values...), window.at))
	}
	b.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].BasinID < result[j].BasinID })
	return result
}

// Get 返回某生化池最近一次采样窗口；从未提交过时 ok 为 false。
func (b *WindowBook) Get(basinID string) (Window, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	window, ok := b.windows[basinID]
	if !ok {
		return Window{}, false
	}
	return evaluate(basinID, append([]float64(nil), window.values...), window.at), true
}

func evaluate(basinID string, values []float64, storedAt time.Time) Window {
	var total float64
	minimum, maximum := math.Inf(1), math.Inf(-1)
	for _, value := range values {
		total += value
		minimum = math.Min(minimum, value)
		maximum = math.Max(maximum, value)
	}
	average := total / float64(len(values))
	return Window{
		BasinID: basinID, Values: append([]float64(nil), values...), Average: average,
		Stable: maximum-minimum <= 0.35, StoredAt: storedAt,
	}
}
