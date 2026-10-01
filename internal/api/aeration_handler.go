package api

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/cry-97/internal/aeration"
	"github.com/wyw14/cry-97/internal/model"
)

// policyView is the wire form of an aeration.LinePolicy with the sample max
// age expressed in seconds.
type policyView struct {
	LineID              model.LineID `json:"line_id"`
	TargetMin           float64      `json:"target_min"`
	TargetMax           float64      `json:"target_max"`
	MaxStep             float64      `json:"max_step"`
	SafeFrequency       float64      `json:"safe_frequency"`
	IdleFrequency       float64      `json:"idle_frequency"`
	SampleMaxAgeSeconds float64      `json:"sample_max_age_seconds"`
}

func toPolicyView(policy aeration.LinePolicy) policyView {
	return policyView{
		LineID: policy.LineID, TargetMin: policy.TargetMin, TargetMax: policy.TargetMax,
		MaxStep: policy.MaxStep, SafeFrequency: policy.SafeFrequency, IdleFrequency: policy.IdleFrequency,
		SampleMaxAgeSeconds: policy.SampleMaxAge.Seconds(),
	}
}

// policyRequest accepts partial updates; nil fields keep their current value.
type policyRequest struct {
	TargetMin           *float64 `json:"target_min"`
	TargetMax           *float64 `json:"target_max"`
	MaxStep             *float64 `json:"max_step"`
	SafeFrequency       *float64 `json:"safe_frequency"`
	IdleFrequency       *float64 `json:"idle_frequency"`
	SampleMaxAgeSeconds *float64 `json:"sample_max_age_seconds"`
}

func (s *Server) aerationStatus(w http.ResponseWriter, _ *http.Request) {
	policies := s.plant.AerationPolicies()
	views := make([]policyView, 0, len(policies))
	for _, policy := range policies {
		views = append(views, toPolicyView(policy))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"blowers": s.plant.StatusBlowers(), "windows": s.plant.StatusAerationWindows(),
		"decisions": s.plant.StatusAerationDecisions(), "policies": views,
	})
}

func (s *Server) aerationSample(w http.ResponseWriter, r *http.Request) {
	lineID, err := model.ParseLineID(chi.URLParam(r, "lineID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	outcome, err := s.plant.SubmitAeration(r.Context(), lineID, chi.URLParam(r, "basinID"), payload)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusAccepted, outcome)
}

func (s *Server) updateAerationPolicy(w http.ResponseWriter, r *http.Request) {
	lineID, err := model.ParseLineID(chi.URLParam(r, "lineID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var input policyRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	policy, ok := s.plant.AerationPolicy(lineID)
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("process line is not found"))
		return
	}
	if input.TargetMin != nil {
		policy.TargetMin = *input.TargetMin
	}
	if input.TargetMax != nil {
		policy.TargetMax = *input.TargetMax
	}
	if input.MaxStep != nil {
		policy.MaxStep = *input.MaxStep
	}
	if input.SafeFrequency != nil {
		policy.SafeFrequency = *input.SafeFrequency
	}
	if input.IdleFrequency != nil {
		policy.IdleFrequency = *input.IdleFrequency
	}
	if input.SampleMaxAgeSeconds != nil {
		policy.SampleMaxAge = time.Duration(*input.SampleMaxAgeSeconds * float64(time.Second))
	}
	if err := s.plant.ConfigureAerationPolicy(policy); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusOK, toPolicyView(policy))
}
