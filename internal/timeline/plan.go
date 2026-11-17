package timeline

import (
	"cadenceforge/internal/domain"
	"cadenceforge/internal/sync"
)

// Build creates the frozen source-to-output plan. Linear sheets keep the
// original compacted layout; synced sheets derive every source interval from
// the explicitly supplied frozen calibration.
func Build(sheet domain.Sheet, recipe domain.Recipe, sources map[string]domain.Source, calibration *domain.Calibration) (domain.Plan, error) {
	if err := domain.ValidateSheet(sheet, sources); err != nil {
		return domain.Plan{}, err
	}
	if err := domain.ValidateRecipe(recipe); err != nil {
		return domain.Plan{}, err
	}
	synced := sheet.SyncID != "" || len(sheet.Shots) > 0 || len(sheet.Sound) > 0
	if !synced {
		if calibration != nil {
			return domain.Plan{}, domain.Invalid("linear sheet cannot reference a calibration")
		}
		return buildLinear(sheet, recipe, sources)
	}
	if calibration == nil {
		return domain.Plan{}, domain.Invalid("synced sheet requires an explicit calibration revision")
	}
	if err := domain.ValidateCalibration(*calibration, sources); err != nil {
		return domain.Plan{}, err
	}
	if err := domain.ValidateProgramAgainstCalibration(sheet, *calibration, sources); err != nil {
		return domain.Plan{}, err
	}
	return buildSynced(sheet, recipe, sources, *calibration)
}

func buildLinear(sheet domain.Sheet, recipe domain.Recipe, sources map[string]domain.Source) (domain.Plan, error) {
	plan := domain.Plan{Recipe: recipe, Placements: []domain.Placement{}, Video: []domain.VideoPlacement{}, Sound: []domain.SoundPlacement{}}
	for _, span := range sheet.Spans {
		source := sources[span.SourceID]
		if recipe.Format == "mp4" && !source.Media.Video {
			return plan, domain.Invalid("MP4 span requires video: %s", source.ID)
		}
		if recipe.Format == "wav" && !source.Media.Audio {
			return plan, domain.Invalid("WAV span requires audio: %s", source.ID)
		}
		duration := span.OutMS - span.InMS
		plan.Placements = append(plan.Placements, domain.Placement{SourceID: source.ID, SourceInMS: span.InMS, SourceOutMS: span.OutMS, OutputInMS: plan.DurationMS, OutputOutMS: plan.DurationMS + duration, GainDB: span.GainDB})
		plan.DurationMS += duration
	}
	if recipe.Format == "mp4" {
		for i, p := range plan.Placements {
			plan.Video = append(plan.Video, domain.VideoPlacement{ShotIndex: i, CameraID: p.SourceID, ProgramInMS: p.OutputInMS, ProgramOutMS: p.OutputOutMS, SourceInMS: p.SourceInMS, SourceOutMS: p.SourceOutMS})
			plan.Sound = append(plan.Sound, domain.SoundPlacement{SpanIndex: i, CameraID: p.SourceID, ProgramInMS: p.OutputInMS, ProgramOutMS: p.OutputOutMS, SourceInMS: p.SourceInMS, SourceOutMS: p.SourceOutMS, GainDB: p.GainDB, OffsetMS: p.SourceInMS - p.OutputInMS})
		}
	} else {
		for i, p := range plan.Placements {
			plan.Sound = append(plan.Sound, domain.SoundPlacement{SpanIndex: i, CameraID: p.SourceID, ProgramInMS: p.OutputInMS, ProgramOutMS: p.OutputOutMS, SourceInMS: p.SourceInMS, SourceOutMS: p.SourceOutMS, GainDB: p.GainDB, OffsetMS: p.SourceInMS - p.OutputInMS})
		}
	}
	return plan, nil
}

func buildSynced(sheet domain.Sheet, recipe domain.Recipe, sources map[string]domain.Source, calibration domain.Calibration) (domain.Plan, error) {
	pair := sync.NewPair(calibration)
	programIn := sheet.Shots[0].ProgramInMS
	programOut := sheet.Shots[len(sheet.Shots)-1].ProgramOutMS
	// One program-wide correspondence shared by picture and sound.
	mapping, err := sync.Build(pair, programIn, programOut)
	if err != nil {
		return domain.Plan{}, domain.Invalid("calibration mapping: %s", err)
	}
	plan := domain.Plan{Recipe: recipe, Placements: []domain.Placement{}, Video: []domain.VideoPlacement{}, Sound: []domain.SoundPlacement{}, DurationMS: programOut - programIn, ProgramStartMS: programIn}
	fact := newSyncFact(calibration)
	plan.Sync = fact
	outputOf := func(program int64) int64 { return program - programIn }

	if recipe.Format == "mp4" {
		for i, shot := range sheet.Shots {
			if !sources[shot.CameraID].Media.Video {
				return plan, domain.Invalid("MP4 shot requires video: %s", shot.CameraID)
			}
			// Picture selection and sound arrangement share the correspondence,
			// but only a camera shot is cut at a camera-clock splice. The
			// master camera is the program clock, so it is never split.
			pieces := picturePieces(mapping, pair, shot.CameraID, shot.ProgramInMS, shot.ProgramOutMS)
			for _, piece := range pieces {
				// Source bounds come from the piece's own constant offset. Asking
				// the map for the exclusive end would land on the next piece and
				// silently stretch the interval.
				sourceIn := piece.ProgramIn + piece.Offset
				sourceOut := piece.ProgramOut + piece.Offset
				if sourceIn < 0 || sourceOut > sources[shot.CameraID].Media.DurationMS || sourceOut <= sourceIn {
					return plan, domain.Invalid("shot %d maps outside its camera duration", i)
				}
				plan.Video = append(plan.Video, domain.VideoPlacement{ShotIndex: i, CameraID: shot.CameraID, ProgramInMS: piece.ProgramIn, ProgramOutMS: piece.ProgramOut, SourceInMS: sourceIn, SourceOutMS: sourceOut, Correspondence: piece.Offset, Correction: piece.Correction, SkipMS: piece.SkipMS})
				plan.Placements = append(plan.Placements, domain.Placement{SourceID: shot.CameraID, SourceInMS: sourceIn, SourceOutMS: sourceOut, OutputInMS: outputOf(piece.ProgramIn), OutputOutMS: outputOf(piece.ProgramOut)})
				if shot.CameraID == pair.Camera && piece.Correction {
					fact.TotalSkippedMS += abs64(piece.SkipMS)
				}
			}
		}
		if len(plan.Video) == 0 {
			return plan, domain.Invalid("MP4 synced sheet has no picture")
		}
	}

	for i, span := range sheet.Sound {
		source := sources[span.CameraID]
		if recipe.Format == "wav" && !source.Media.Audio {
			return plan, domain.Invalid("WAV sound span requires audio: %s", span.CameraID)
		}
		sourceIn, ok := mapping.SourceAt(span.CameraID, span.ProgramInMS)
		if !ok {
			return plan, domain.Invalid("sound span %d has no confirmed mapping", i)
		}
		// A sound span is one continuous field recording. Holding the offset at
		// its start preserves natural speed and avoids an audible splice; the
		// resulting drift residual is reported, not silently absorbed.
		sourceOut := sourceIn + (span.ProgramOutMS - span.ProgramInMS)
		if sourceIn < 0 || sourceOut > source.Media.DurationMS {
			return plan, domain.Invalid("sound span %d maps outside its source duration", i)
		}
		offset := int64(0)
		startResidual, endResidual := int64(0), int64(0)
		if span.CameraID == pair.Camera {
			// The span holds the offset of its first program instant; both ends
			// are reported because drift accumulates across the span.
			if piece, exists := mapping.PieceAt(span.ProgramInMS); exists {
				offset = piece.Offset
			}
			startResidual = mapping.ResidualAt(span.CameraID, span.ProgramInMS)
			endResidual = mapping.ResidualAt(span.CameraID, span.ProgramOutMS-1)
		}
		residual := mapping.MaxResidual(span.CameraID, span.ProgramInMS, span.ProgramOutMS)
		plan.Sound = append(plan.Sound, domain.SoundPlacement{SpanIndex: i, CameraID: span.CameraID, ProgramInMS: span.ProgramInMS, ProgramOutMS: span.ProgramOutMS, SourceInMS: sourceIn, SourceOutMS: sourceOut, GainDB: span.GainDB, OffsetMS: offset, StartResidual: startResidual, EndResidual: endResidual, MaxResidualMS: residual, CrossesPicture: span.CameraID == pair.Camera && len(picturePieces(mapping, pair, span.CameraID, span.ProgramInMS, span.ProgramOutMS)) > 1})
		plan.Placements = append(plan.Placements, domain.Placement{SourceID: span.CameraID, SourceInMS: sourceIn, SourceOutMS: sourceOut, OutputInMS: outputOf(span.ProgramInMS), OutputOutMS: outputOf(span.ProgramOutMS), GainDB: span.GainDB})
		if span.CameraID == pair.Camera {
			if residual > 0 {
				fact.SoundInexact = append(fact.SoundInexact, domain.ResidualInterval{ProgramInMS: span.ProgramInMS, ProgramOutMS: span.ProgramOutMS, MaxResidualMS: residual, Range: "sound", Reason: "continuous sound held at one natural-speed offset; residual is the drift not removed"})
			}
			if residual > fact.SoundMaxResidualMS {
				fact.SoundMaxResidualMS = residual
			}
		}
	}
	if len(plan.Sound) == 0 {
		return plan, domain.Invalid("synced sheet has no sound")
	}
	finishSyncFact(fact, mapping, pair, plan, programIn, programOut)
	fact.Notes = deliveryNotes(fact)
	return plan, nil
}

// picturePieces clips the shared correspondence to a shot. The master camera is
// the program clock, so its shot is one piece. A drifting-camera shot is split
// at global picture splices only when the splice actually falls inside it.
func picturePieces(mapping sync.Map, pair sync.Pair, cameraID string, in, out int64) []sync.Piece {
	if cameraID == pair.Master {
		return []sync.Piece{{ProgramIn: in, ProgramOut: out, Offset: 0}}
	}
	pieces := []sync.Piece{}
	for _, piece := range mapping.Pieces {
		lo, hi := max(in, piece.ProgramIn), min(out, piece.ProgramOut)
		if hi <= lo {
			continue
		}
		clipped := piece
		clipped.ProgramIn, clipped.ProgramOut = lo, hi
		if lo != piece.ProgramIn {
			clipped.Correction = false
			clipped.SkipMS = 0
		}
		pieces = append(pieces, clipped)
	}
	return pieces
}

func newSyncFact(calibration domain.Calibration) *domain.SyncFact {
	return &domain.SyncFact{
		CalibrationID:    calibration.ID,
		Revision:         calibration.Revision,
		MasterSourceID:   calibration.MasterSourceID,
		CameraSourceID:   calibration.CameraSourceID,
		Policy:           "natural-speed-anchored-correspondence",
		NaturalSpeed:     true,
		TempoChange:      false,
		ProgramSource:    "master source clock",
		ToleranceMS:      calibration.ToleranceMS,
		Anchors:          []domain.SyncAnchorFact{},
		Gaps:             []domain.SyncGapFact{},
		Corrections:      []domain.SyncCorrectionFact{},
		InexactIntervals: []domain.ResidualInterval{},
		Pieces:           []domain.SyncPieceFact{},
		SoundInexact:     []domain.ResidualInterval{},
		Notes:            []string{},
	}
}

func finishSyncFact(fact *domain.SyncFact, mapping sync.Map, pair sync.Pair, plan domain.Plan, programIn, programOut int64) {
	for _, piece := range mapping.Pieces {
		fact.Pieces = append(fact.Pieces, domain.SyncPieceFact{ProgramInMS: piece.ProgramIn, ProgramOutMS: piece.ProgramOut, OffsetMS: piece.Offset, Correction: piece.Correction, SkipMS: piece.SkipMS})
	}
	for _, a := range pair.Anchors {
		if a.MasterMS < programIn || a.MasterMS > programOut {
			continue
		}
		mapped, _ := mapping.SourceAt(pair.Camera, a.MasterMS)
		fact.Anchors = append(fact.Anchors, domain.SyncAnchorFact{MasterMS: a.MasterMS, CameraMS: a.CameraMS, DriftMS: a.CameraMS - a.MasterMS, MappedMS: mapped, ResidualMS: mapping.ResidualAt(pair.Camera, a.MasterMS), Exact: mapped == a.CameraMS})
	}
	for _, piece := range mapping.Corrections() {
		fact.Corrections = append(fact.Corrections, domain.SyncCorrectionFact{ProgramMS: piece.ProgramIn, CameraBeforeMS: piece.ProgramIn + piece.Offset - piece.SkipMS, CameraAfterMS: piece.ProgramIn + piece.Offset, SkippedCameraMS: piece.SkipMS})
		fact.InexactIntervals = append(fact.InexactIntervals, domain.ResidualInterval{ProgramInMS: piece.GapMasterIn, ProgramOutMS: piece.GapMasterOut, MaxResidualMS: mapping.MaxResidual(pair.Camera, piece.GapMasterIn, piece.GapMasterOut), Range: "picture", Reason: "drift removed by one natural-speed splice at the gap midpoint; residual is what remains"})
		if residual := mapping.MaxResidual(pair.Camera, piece.GapMasterIn, piece.GapMasterOut); residual > fact.MaxResidualMS {
			fact.MaxResidualMS = residual
		}
	}
	for i := 0; i+1 < len(pair.Anchors); i++ {
		a, b := pair.Anchors[i], pair.Anchors[i+1]
		if b.MasterMS <= programIn || a.MasterMS >= programOut {
			continue
		}
		gap := domain.SyncGapFact{MasterInMS: a.MasterMS, MasterOutMS: b.MasterMS, DriftMS: (b.CameraMS - b.MasterMS) - (a.CameraMS - a.MasterMS), MaxResidualMS: mapping.MaxResidual(pair.Camera, a.MasterMS, b.MasterMS)}
		for _, piece := range mapping.Corrections() {
			if piece.GapMasterIn == a.MasterMS && piece.GapMasterOut == b.MasterMS {
				gap.SpliceAtMS = piece.ProgramIn
				gap.SpliceMagnitude = piece.SkipMS
			}
		}
		fact.Gaps = append(fact.Gaps, gap)
	}
	// Report which inexact gap actually affects the delivered picture. A gap
	// outside every camera shot has a residual in the correspondence but no
	// visible seam, so callers can distinguish structural from delivered.
	for i := range fact.Gaps {
		applies := false
		for _, placement := range plan.Video {
			if placement.ProgramOutMS <= fact.Gaps[i].MasterInMS || placement.ProgramInMS >= fact.Gaps[i].MasterOutMS {
				continue
			}
			if placement.Correction {
				applies = true
				break
			}
		}
		fact.Gaps[i].AffectsDeliveredPicture = applies
	}
}

func deliveryNotes(fact *domain.SyncFact) []string {
	notes := []string{
		"Picture runs at natural speed: no tempo change and no source resampling beyond the delivery sample rate.",
		"Every confirmed anchor maps exactly, and both program boundaries use the confirmed correspondence.",
		"Drift between exact points is removed by one splice at the gap midpoint; the note lists the residual left in each gap.",
		"Continuous sound keeps one natural-speed offset per span and reports its residual separately.",
	}
	if len(fact.Corrections) == 0 && fact.MaxResidualMS == 0 {
		notes = append(notes, "The selected program required no splice; both ends are exact.")
	} else {
		notes = append(notes, "Inexact intervals state the residual that only a tempo change could remove; they are not claimed as exact.")
	}
	return notes
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
