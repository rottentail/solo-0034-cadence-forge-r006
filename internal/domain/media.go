package domain

import "time"

type Media struct {
	DurationMS int64  `json:"duration_ms"`
	Video      bool   `json:"video"`
	Audio      bool   `json:"audio"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
	VideoCodec string `json:"video_codec"`
	AudioCodec string `json:"audio_codec"`
}
type Source struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Digest    string    `json:"digest"`
	Bytes     int64     `json:"bytes"`
	Media     Media     `json:"media"`
	CreatedAt time.Time `json:"created_at"`
}
type Span struct {
	SourceID string  `json:"source_id"`
	InMS     int64   `json:"in_ms"`
	OutMS    int64   `json:"out_ms"`
	GainDB   float64 `json:"gain_db"`
}
type Cue struct {
	SourceID string `json:"source_id"`
	StartMS  int64  `json:"start_ms"`
	EndMS    int64  `json:"end_ms"`
	Text     string `json:"text"`
}

// SyncAnchor is one confirmed observation of the same event in the master
// clock and the second field camera clock.
type SyncAnchor struct {
	MasterMS int64 `json:"master_ms"`
	CameraMS int64 `json:"camera_ms"`
}

// Calibration is an immutable revision of the confirmed source-time
// correspondence between two field recordings. Program time is the master
// camera source clock.
type Calibration struct {
	ID             string       `json:"id"`
	Revision       int          `json:"revision"`
	Name           string       `json:"name"`
	MasterSourceID string       `json:"master_source_id"`
	CameraSourceID string       `json:"camera_source_id"`
	Anchors        []SyncAnchor `json:"anchors"`
	Policy         string       `json:"policy"`
	ToleranceMS    int64        `json:"tolerance_ms"`
	CreatedAt      time.Time    `json:"created_at"`
}

// Shot selects a camera over a program interval. Program time is the master
// source clock; the source interval is derived from the frozen calibration.
type Shot struct {
	CameraID     string `json:"camera_id"`
	ProgramInMS  int64  `json:"program_in_ms"`
	ProgramOutMS int64  `json:"program_out_ms"`
}

// SoundSpan selects a continuous sound source over a program interval. Spans
// must tile the program contiguously.
type SoundSpan struct {
	CameraID     string  `json:"camera_id"`
	ProgramInMS  int64   `json:"program_in_ms"`
	ProgramOutMS int64   `json:"program_out_ms"`
	GainDB       float64 `json:"gain_db"`
}

type Sheet struct {
	ID        string      `json:"id"`
	Revision  int         `json:"revision"`
	Name      string      `json:"name"`
	Spans     []Span      `json:"spans"`
	Cues      []Cue       `json:"cues"`
	SyncID    string      `json:"sync_id,omitempty"`
	Shots     []Shot      `json:"shots,omitempty"`
	Sound     []SoundSpan `json:"sound,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
}
type Recipe struct {
	ID         string  `json:"id"`
	Revision   int     `json:"revision"`
	Name       string  `json:"name"`
	Format     string  `json:"format"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	FPS        int     `json:"fps"`
	SampleRate int     `json:"sample_rate"`
	Normalize  bool    `json:"normalize"`
	TargetLUFS float64 `json:"target_lufs"`
}
type Placement struct {
	SourceID    string  `json:"source_id"`
	SourceInMS  int64   `json:"source_in_ms"`
	SourceOutMS int64   `json:"source_out_ms"`
	OutputInMS  int64   `json:"output_in_ms"`
	OutputOutMS int64   `json:"output_out_ms"`
	GainDB      float64 `json:"gain_db"`
}

// VideoPlacement is one contiguous piece of program picture. A shot that
// crosses a calibration correction is split into several pieces so that the
// source interval remains natural speed.
type VideoPlacement struct {
	ShotIndex      int    `json:"shot_index"`
	CameraID       string `json:"camera_id"`
	ProgramInMS    int64  `json:"program_in_ms"`
	ProgramOutMS   int64  `json:"program_out_ms"`
	SourceInMS     int64  `json:"source_in_ms"`
	SourceOutMS    int64  `json:"source_out_ms"`
	Correspondence int64  `json:"correspondence_offset_ms"`
	Correction     bool   `json:"correction"`
	SkipMS         int64  `json:"skip_ms"`
}
type SoundPlacement struct {
	SpanIndex      int     `json:"span_index"`
	CameraID       string  `json:"camera_id"`
	ProgramInMS    int64   `json:"program_in_ms"`
	ProgramOutMS   int64   `json:"program_out_ms"`
	SourceInMS     int64   `json:"source_in_ms"`
	SourceOutMS    int64   `json:"source_out_ms"`
	GainDB         float64 `json:"gain_db"`
	OffsetMS       int64   `json:"offset_ms"`
	StartResidual  int64   `json:"start_residual_ms"`
	EndResidual    int64   `json:"end_residual_ms"`
	MaxResidualMS  int64   `json:"max_residual_ms"`
	CrossesPicture bool    `json:"crosses_picture_correction"`
}
type SyncAnchorFact struct {
	MasterMS   int64 `json:"master_ms"`
	CameraMS   int64 `json:"camera_ms"`
	DriftMS    int64 `json:"drift_ms"`
	MappedMS   int64 `json:"mapped_camera_ms"`
	ResidualMS int64 `json:"residual_ms"`
	Exact      bool  `json:"exact"`
}
type SyncGapFact struct {
	MasterInMS              int64 `json:"master_in_ms"`
	MasterOutMS             int64 `json:"master_out_ms"`
	DriftMS                 int64 `json:"drift_ms"`
	MaxResidualMS           int64 `json:"max_residual_ms"`
	SpliceAtMS              int64 `json:"splice_at_master_ms,omitempty"`
	SpliceMagnitude         int64 `json:"splice_magnitude_ms"`
	AffectsDeliveredPicture bool  `json:"affects_delivered_picture"`
}
type SyncCorrectionFact struct {
	ProgramMS       int64 `json:"program_ms"`
	CameraBeforeMS  int64 `json:"camera_before_ms"`
	CameraAfterMS   int64 `json:"camera_after_ms"`
	SkippedCameraMS int64 `json:"skipped_camera_ms"`
	ShotIndex       int   `json:"shot_index"`
}
type ResidualInterval struct {
	ProgramInMS   int64  `json:"program_in_ms"`
	ProgramOutMS  int64  `json:"program_out_ms"`
	MaxResidualMS int64  `json:"max_residual_ms"`
	Range         string `json:"range"`
	Reason        string `json:"reason"`
}

// SyncPieceFact is one constant-offset run of the program-wide correspondence
// shared by picture, sound and captions. Camera time = program time + Offset.
type SyncPieceFact struct {
	ProgramInMS  int64 `json:"program_in_ms"`
	ProgramOutMS int64 `json:"program_out_ms"`
	OffsetMS     int64 `json:"offset_ms"`
	Correction   bool  `json:"correction"`
	SkipMS       int64 `json:"skip_ms"`
}
type SyncFact struct {
	CalibrationID      string               `json:"calibration_id"`
	Revision           int                  `json:"revision"`
	MasterSourceID     string               `json:"master_source_id"`
	CameraSourceID     string               `json:"camera_source_id"`
	Policy             string               `json:"policy"`
	NaturalSpeed       bool                 `json:"natural_speed"`
	TempoChange        bool                 `json:"tempo_change"`
	ProgramSource      string               `json:"program_clock"`
	ToleranceMS        int64                `json:"tolerance_ms"`
	Anchors            []SyncAnchorFact     `json:"anchors"`
	Gaps               []SyncGapFact        `json:"gaps"`
	Corrections        []SyncCorrectionFact `json:"corrections"`
	InexactIntervals   []ResidualInterval   `json:"inexact_intervals"`
	Pieces             []SyncPieceFact      `json:"pieces"`
	MaxResidualMS      int64                `json:"max_residual_ms"`
	TotalSkippedMS     int64                `json:"total_skipped_camera_ms"`
	SoundMaxResidualMS int64                `json:"sound_max_residual_ms"`
	SoundInexact       []ResidualInterval   `json:"sound_inexact_intervals"`
	Notes              []string             `json:"delivery_notes"`
}
type Plan struct {
	DurationMS     int64            `json:"duration_ms"`
	ProgramStartMS int64            `json:"program_start_ms,omitempty"`
	Placements     []Placement      `json:"placements"`
	Video          []VideoPlacement `json:"video_placements,omitempty"`
	Sound          []SoundPlacement `json:"sound_placements,omitempty"`
	Sync           *SyncFact        `json:"sync,omitempty"`
	Recipe         Recipe           `json:"recipe"`
	VTT            string           `json:"vtt"`
}
