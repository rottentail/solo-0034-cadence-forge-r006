package service

import (
	"cadenceforge/internal/caption"
	"cadenceforge/internal/domain"
	"cadenceforge/internal/timeline"
	"time"
)

func (s *Service) SaveSheet(id string, expected int, input domain.Sheet) (domain.Sheet, error) {
	s.mutation.Lock()
	defer s.mutation.Unlock()
	if id == "" {
		id = ID()
	}
	var out domain.Sheet
	err := s.Store.Update(func(st *domain.State) error {
		versions := st.Sheets[id]
		if len(versions) != expected {
			return domain.Conflict("sheet revision changed")
		}
		if expected == 0 && len(versions) > 0 {
			return domain.Conflict("sheet already exists")
		}
		if expected > 0 && len(versions) == 0 {
			return domain.Missing("sheet", id)
		}
		if err := domain.ValidateSheet(input, st.Sources); err != nil {
			return err
		}
		if input.SyncID != "" {
			if len(st.Calibrations[input.SyncID]) == 0 {
				return domain.Missing("calibration", input.SyncID)
			}
			latest := st.Calibrations[input.SyncID][len(st.Calibrations[input.SyncID])-1]
			if err := domain.ValidateProgramAgainstCalibration(input, latest, st.Sources); err != nil {
				return err
			}
		}
		input.ID = id
		input.Revision = len(versions) + 1
		input.CreatedAt = time.Now().UTC()
		out = input
		st.Sheets[id] = append(versions, input)
		st.Emit(id, "sheet.revised", "immutable timeline revision saved")
		return nil
	})
	return out, err
}

func (s *Service) Preview(sheetID, recipeID, calibrationID string) (domain.Plan, error) {
	st, err := s.Store.Snapshot()
	if err != nil {
		return domain.Plan{}, err
	}
	sheets := st.Sheets[sheetID]
	recipes := st.Recipes[recipeID]
	if len(sheets) == 0 {
		return domain.Plan{}, domain.Missing("sheet", sheetID)
	}
	if len(recipes) == 0 {
		return domain.Plan{}, domain.Missing("recipe", recipeID)
	}
	sheet := sheets[len(sheets)-1]
	recipe := recipes[len(recipes)-1]
	var calibration *domain.Calibration
	if sheet.SyncID != "" || len(sheet.Shots) > 0 || len(sheet.Sound) > 0 {
		id := calibrationID
		if id == "" {
			id = sheet.SyncID
		}
		if id == "" {
			return domain.Plan{}, domain.Invalid("synced preview requires a calibration revision")
		}
		cal, revision, err := selectCalibration(st, id, 0)
		if err != nil {
			return domain.Plan{}, err
		}
		_ = revision
		calibration = &cal
	}
	plan, err := timeline.Build(sheet, recipe, st.Sources, calibration)
	if err != nil {
		return plan, err
	}
	plan.VTT = caption.VTT(caption.Map(sheet, plan))
	return plan, nil
}

func selectCalibration(st domain.State, id string, revision int) (domain.Calibration, int, error) {
	versions := st.Calibrations[id]
	if len(versions) == 0 {
		return domain.Calibration{}, 0, domain.Missing("calibration", id)
	}
	if revision == 0 {
		revision = len(versions)
	}
	if revision < 1 || revision > len(versions) {
		return domain.Calibration{}, 0, domain.Missing("calibration revision", id)
	}
	return versions[revision-1], revision, nil
}
