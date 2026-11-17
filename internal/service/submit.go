package service

import (
	"cadenceforge/internal/caption"
	"cadenceforge/internal/domain"
	"cadenceforge/internal/timeline"
	"strings"
	"time"
)

type Request struct {
	SheetID             string `json:"sheet_id"`
	SheetRevision       int    `json:"sheet_revision"`
	RecipeID            string `json:"recipe_id"`
	RecipeRevision      int    `json:"recipe_revision"`
	CalibrationID       string `json:"calibration_id,omitempty"`
	CalibrationRevision int    `json:"calibration_revision,omitempty"`
}

func (s *Service) Submit(key string, request Request) (domain.Render, error) {
	if strings.TrimSpace(key) == "" || len(key) > 128 {
		return domain.Render{}, domain.Invalid("Idempotency-Key requires 1..128 bytes")
	}
	if request.SheetRevision < 1 || request.RecipeRevision < 1 {
		return domain.Render{}, domain.Invalid("explicit positive revisions are required")
	}
	if request.CalibrationRevision < 0 || (request.CalibrationRevision > 0 && request.CalibrationID == "") {
		return domain.Render{}, domain.Invalid("calibration revision requires a calibration id")
	}
	s.mutation.Lock()
	defer s.mutation.Unlock()
	var out domain.Render
	err := s.Store.Update(func(st *domain.State) error {
		fingerprint := hash(request)
		for _, r := range st.Renders {
			if r.Key == key {
				if r.RequestHash != fingerprint {
					return domain.Conflict("idempotency key has different input")
				}
				out = r
				return nil
			}
		}
		pending := 0
		for _, r := range st.Renders {
			if r.State == "queued" || r.State == "running" {
				pending++
			}
		}
		if pending >= s.Config.QueueLimit {
			return domain.Capacity("render queue is full")
		}
		sheets := st.Sheets[request.SheetID]
		recipes := st.Recipes[request.RecipeID]
		if request.SheetRevision > len(sheets) {
			return domain.Missing("sheet revision", request.SheetID)
		}
		if request.RecipeRevision > len(recipes) {
			return domain.Missing("recipe revision", request.RecipeID)
		}
		sheet := sheets[request.SheetRevision-1]
		recipe := recipes[request.RecipeRevision-1]
		sources := map[string]domain.Source{}
		for _, p := range sheet.Spans {
			sources[p.SourceID] = st.Sources[p.SourceID]
		}
		for _, c := range sheet.Cues {
			sources[c.SourceID] = st.Sources[c.SourceID]
		}
		for _, shot := range sheet.Shots {
			sources[shot.CameraID] = st.Sources[shot.CameraID]
		}
		for _, span := range sheet.Sound {
			sources[span.CameraID] = st.Sources[span.CameraID]
		}
		var calibration *domain.Calibration
		synced := sheet.SyncID != "" || len(sheet.Shots) > 0 || len(sheet.Sound) > 0
		if synced {
			id := request.CalibrationID
			if id == "" {
				id = sheet.SyncID
			}
			if id == "" {
				return domain.Invalid("synced render requires an explicit calibration")
			}
			cal, _, err := selectCalibration(*st, id, request.CalibrationRevision)
			if err != nil {
				return err
			}
			if sheet.SyncID != "" && cal.ID != sheet.SyncID {
				return domain.Conflict("sheet revision belongs to a different calibration")
			}
			for _, camera := range []string{cal.MasterSourceID, cal.CameraSourceID} {
				if _, ok := sources[camera]; !ok {
					sources[camera] = st.Sources[camera]
				}
			}
			calibration = &cal
		} else if request.CalibrationID != "" {
			return domain.Invalid("linear sheet cannot reference a calibration")
		}
		plan, err := timeline.Build(sheet, recipe, sources, calibration)
		if err != nil {
			return err
		}
		plan.VTT = caption.VTT(caption.Map(sheet, plan))
		now := time.Now().UTC()
		id := ID()
		out = domain.Render{ID: id, Key: key, RequestHash: fingerprint, Sheet: sheet, Recipe: recipe, Calibration: calibration, Sources: sources, Plan: plan, State: "queued", Attempts: []domain.Attempt{}, CreatedAt: now, UpdatedAt: now}
		st.Renders[id] = out
		st.Emit(id, "render.queued", "frozen source, calibration and recipe revisions")
		return nil
	})
	if err == nil {
		s.Notify()
	}
	return out, err
}
