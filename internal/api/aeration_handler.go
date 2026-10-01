package api

import (
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/cry-97/internal/aeration"
	"github.com/wyw14/cry-97/internal/model"
)

func (s *Server) aerationStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"blowers":   s.plant.StatusBlowers(),
		"windows":   s.plant.StatusAerationWindows(),
		"decisions": s.plant.StatusAerationDecisions(),
		"policies":  s.plant.StatusAerationPolicies(),
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
	window, command, err := s.plant.SubmitAeration(r.Context(), lineID, chi.URLParam(r, "basinID"), payload)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"window": window, "command": command})
}

type aerationConfigRequest struct {
	MinDO         *float64                                  `json:"min_do,omitempty"`
	MaxDO         *float64                                  `json:"max_do,omitempty"`
	SafeFrequency *float64                                  `json:"safe_frequency,omitempty"`
	MaxStep       *float64                                  `json:"max_step_hz,omitempty"`
	SampleMaxAge  string                                    `json:"sample_max_age,omitempty"`
	StageBands    map[model.ProcessStage]aeration.StageBand `json:"stage_bands,omitempty"`
}

func (s *Server) aerationConfig(w http.ResponseWriter, r *http.Request) {
	lineID, err := model.ParseLineID(chi.URLParam(r, "lineID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var input aerationConfigRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	patch := aeration.PolicyUpdate{
		MinDO: input.MinDO, MaxDO: input.MaxDO, SafeFrequency: input.SafeFrequency,
		MaxStep: input.MaxStep, StageBands: input.StageBands,
	}
	if input.SampleMaxAge != "" {
		maxAge, err := time.ParseDuration(input.SampleMaxAge)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		patch.SampleMaxAge = &maxAge
	}
	policy, err := s.plant.ConfigureAeration(lineID, patch)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusOK, policy.View())
}
