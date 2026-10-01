package aeration

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/wyw14/cry-97/internal/model"
)

type Blower struct {
	ID        string    `json:"id"`
	BasinID   string    `json:"basin_id"`
	Frequency float64   `json:"frequency"`
	Running   bool      `json:"running"`
	UpdatedAt time.Time `json:"updated_at"`
}

type BlowerFleet struct {
	mu      sync.RWMutex
	blowers map[string]Blower
}

func NewBlowerFleet(blowers []Blower) (*BlowerFleet, error) {
	if len(blowers) == 0 {
		return nil, errors.New("at least one blower is required")
	}
	fleet := &BlowerFleet{blowers: make(map[string]Blower, len(blowers))}
	for _, blower := range blowers {
		if blower.ID == "" || blower.BasinID == "" {
			return nil, errors.New("blower identity is required")
		}
		fleet.blowers[blower.ID] = blower
	}
	return fleet, nil
}

func (f *BlowerFleet) Get(id string) (Blower, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	blower, ok := f.blowers[id]
	return blower, ok
}

func (f *BlowerFleet) Apply(command model.DeviceCommand, now time.Time) (Blower, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	blower, ok := f.blowers[command.DeviceID]
	if !ok || command.Kind != model.DeviceBlower {
		return Blower{}, errors.New("blower command target is unknown")
	}
	if command.Setpoint < 0 || command.Setpoint > 60 {
		return Blower{}, errors.New("blower frequency is outside operating range")
	}
	blower.Frequency = command.Setpoint
	blower.Running = command.Setpoint > 0
	blower.UpdatedAt = now.UTC()
	f.blowers[blower.ID] = blower
	return blower, nil
}

func (f *BlowerFleet) Snapshot() []Blower {
	f.mu.RLock()
	defer f.mu.RUnlock()
	result := make([]Blower, 0, len(f.blowers))
	for _, blower := range f.blowers {
		result = append(result, blower)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
