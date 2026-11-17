package service

import (
	"cadenceforge/internal/domain"
	"time"
)

func (s *Service) SaveCalibration(id string, expected int, input domain.Calibration) (domain.Calibration, error) {
	input.Policy = "natural-speed-anchored-correspondence"
	if err := domain.ValidateCalibration(input, mustSources(s)); err != nil {
		return domain.Calibration{}, err
	}
	s.mutation.Lock()
	defer s.mutation.Unlock()
	if id == "" {
		id = ID()
	}
	var out domain.Calibration
	err := s.Store.Update(func(st *domain.State) error {
		versions := st.Calibrations[id]
		if len(versions) != expected {
			return domain.Conflict("calibration revision changed")
		}
		if expected == 0 && len(versions) > 0 {
			return domain.Conflict("calibration already exists")
		}
		if expected > 0 && len(versions) == 0 {
			return domain.Missing("calibration", id)
		}
		if err := domain.ValidateCalibration(input, st.Sources); err != nil {
			return err
		}
		input.ID = id
		input.Revision = len(versions) + 1
		input.CreatedAt = time.Now().UTC()
		out = input
		st.Calibrations[id] = append(versions, input)
		st.Emit(id, "calibration.revised", "immutable confirmed source correspondence saved")
		return nil
	})
	return out, err
}

func mustSources(s *Service) map[string]domain.Source {
	st, err := s.Store.Snapshot()
	if err != nil {
		return map[string]domain.Source{}
	}
	return st.Sources
}
