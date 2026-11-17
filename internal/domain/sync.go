package domain

import (
	"math"
	"sort"
	"strings"
)

const maxSyncAnchors = 64

// ValidateCalibration accepts only confirmed before/after observations for two
// distinct field recordings. Both clocks must advance monotonically, because a
// backwards camera clock has no natural-speed correction that preserves order.
func ValidateCalibration(c Calibration, sources map[string]Source) error {
	if strings.TrimSpace(c.Name) == "" || len(c.Name) > 160 {
		return Invalid("calibration name must contain 1..160 bytes")
	}
	if c.MasterSourceID == c.CameraSourceID {
		return Invalid("calibration requires two distinct sources")
	}
	master, ok := sources[c.MasterSourceID]
	if !ok {
		return Missing("master source", c.MasterSourceID)
	}
	camera, ok := sources[c.CameraSourceID]
	if !ok {
		return Missing("camera source", c.CameraSourceID)
	}
	if !master.Media.Audio || !camera.Media.Audio {
		return Invalid("calibration requires audio in both sources")
	}
	if len(c.Anchors) < 2 || len(c.Anchors) > maxSyncAnchors {
		return Invalid("calibration requires 2..%d anchors", maxSyncAnchors)
	}
	if c.ToleranceMS < 1 || c.ToleranceMS > 500 {
		return Invalid("sync tolerance must be 1..500ms")
	}
	previousMaster, previousCamera := int64(-1), int64(-1)
	for i, a := range c.Anchors {
		if a.MasterMS < 0 || a.MasterMS > master.Media.DurationMS {
			return Invalid("anchor %d is outside the master duration", i)
		}
		if a.CameraMS < 0 || a.CameraMS > camera.Media.DurationMS {
			return Invalid("anchor %d is outside the camera duration", i)
		}
		if a.MasterMS <= previousMaster {
			return Invalid("anchor %d does not advance master time", i)
		}
		if a.CameraMS <= previousCamera {
			return Invalid("anchor %d does not advance camera time", i)
		}
		previousMaster, previousCamera = a.MasterMS, a.CameraMS
	}
	return nil
}

func SortedAnchors(anchors []SyncAnchor) []SyncAnchor {
	out := append([]SyncAnchor(nil), anchors...)
	sort.Slice(out, func(i, j int) bool { return out[i].MasterMS < out[j].MasterMS })
	return out
}

// DriftAt returns camera_time - master_time interpolated between confirmed
// anchors. Callers validate that t is inside the anchor span.
func DriftAt(anchors []SyncAnchor, t int64) float64 {
	if t <= anchors[0].MasterMS {
		return float64(anchors[0].CameraMS - anchors[0].MasterMS)
	}
	last := anchors[len(anchors)-1]
	if t >= last.MasterMS {
		return float64(last.CameraMS - last.MasterMS)
	}
	lo, hi := 0, len(anchors)-1
	for lo+1 < hi {
		mid := (lo + hi) / 2
		if anchors[mid].MasterMS <= t {
			lo = mid
		} else {
			hi = mid
		}
	}
	a, b := anchors[lo], anchors[hi]
	span := float64(b.MasterMS - a.MasterMS)
	position := float64(t-a.MasterMS) / span
	return float64(a.CameraMS-a.MasterMS) + position*float64((b.CameraMS-b.MasterMS)-(a.CameraMS-a.MasterMS))
}

func RoundHalfAway(value float64) int64 {
	// math.Round rounds halves away from zero and leaves exact integers alone.
	return int64(math.Round(value))
}

// ValidateSyncedSheet checks the dual-camera structure that does not depend on
// a particular calibration revision. Anchor coverage and source bounds are
// checked when a plan is built against a frozen revision.
func ValidateSyncedSheet(s Sheet, sources map[string]Source) error {
	if len(s.Spans) != 0 {
		return Invalid("synced sheet uses shots and sound, not linear spans")
	}
	if len(s.Shots) == 0 || len(s.Shots) > 64 {
		return Invalid("a synced sheet requires 1..64 picture shots")
	}
	if len(s.Sound) == 0 || len(s.Sound) > 64 {
		return Invalid("a synced sheet requires 1..64 sound spans")
	}
	var duration int64
	var programEnd int64
	for i, shot := range s.Shots {
		if _, ok := sources[shot.CameraID]; !ok {
			return Missing("shot source", shot.CameraID)
		}
		if shot.ProgramInMS < 0 || shot.ProgramOutMS <= shot.ProgramInMS {
			return Invalid("shot %d has an invalid program interval", i)
		}
		if shot.ProgramOutMS-shot.ProgramInMS < 100 && len(s.Shots) > 1 {
			return Invalid("shot %d is shorter than 100ms", i)
		}
		if i > 0 && shot.ProgramInMS != programEnd {
			return Invalid("picture shots must tile the program without gaps")
		}
		duration += shot.ProgramOutMS - shot.ProgramInMS
		programEnd = shot.ProgramOutMS
	}
	if duration > 120000 {
		return Invalid("sheet exceeds 120 seconds")
	}
	if duration < 100 {
		return Invalid("synced program is shorter than 100ms")
	}
	var soundEnd int64
	for i, span := range s.Sound {
		if _, ok := sources[span.CameraID]; !ok {
			return Missing("sound source", span.CameraID)
		}
		if span.ProgramInMS < 0 || span.ProgramOutMS <= span.ProgramInMS {
			return Invalid("sound span %d has an invalid program interval", i)
		}
		if i > 0 && span.ProgramInMS != soundEnd {
			return Invalid("sound spans must be contiguous; provide the continuous program soundtrack")
		}
		gain := span.GainDB
		if math.IsNaN(gain) || math.IsInf(gain, 0) || gain < -60 || gain > 12 {
			return Invalid("sound span %d has invalid gain", i)
		}
		soundEnd = span.ProgramOutMS
	}
	if s.Sound[0].ProgramInMS != s.Shots[0].ProgramInMS || soundEnd != programEnd {
		return Invalid("sound spans must cover exactly the picture program")
	}
	if len(s.Cues) > 500 {
		return Invalid("sheet exceeds 500 cues")
	}
	for i, c := range s.Cues {
		source, ok := sources[c.SourceID]
		if !ok {
			return Missing("cue source", c.SourceID)
		}
		if c.StartMS < 0 || c.EndMS <= c.StartMS || c.EndMS > source.Media.DurationMS {
			return Invalid("cue %d has invalid interval", i)
		}
		if !validCueText(c.Text) {
			return Invalid("cue %d has invalid text", i)
		}
	}
	return nil
}

func validCueText(text string) bool {
	if strings.TrimSpace(text) == "" || len(text) > 2000 || strings.Contains(text, "-->") || strings.ContainsAny(text, "\x00\r") {
		return false
	}
	for _, r := range text {
		if r == 0 {
			return false
		}
	}
	return true
}

// ValidateProgramAgainstCalibration verifies that every selected camera is one
// of the confirmed pair and that the program lies inside the anchor span. A
// program outside confirmed anchors would be an unverified extrapolation.
func ValidateProgramAgainstCalibration(s Sheet, cal Calibration, sources map[string]Source) error {
	if s.SyncID != cal.ID {
		return Invalid("sheet sync_id does not match the selected calibration")
	}
	anchors := SortedAnchors(cal.Anchors)
	start, end := s.Shots[0].ProgramInMS, s.Shots[len(s.Shots)-1].ProgramOutMS
	if start < anchors[0].MasterMS || end > anchors[len(anchors)-1].MasterMS {
		return Invalid("program must lie inside confirmed calibration anchors")
	}
	for i, shot := range s.Shots {
		if shot.CameraID != cal.MasterSourceID && shot.CameraID != cal.CameraSourceID {
			return Invalid("shot %d selects a camera outside this calibration", i)
		}
		if shot.CameraID == cal.CameraSourceID && !sources[shot.CameraID].Media.Video {
			return Invalid("shot %d camera has no video", i)
		}
	}
	for i, span := range s.Sound {
		if span.CameraID != cal.MasterSourceID && span.CameraID != cal.CameraSourceID {
			return Invalid("sound span %d selects a source outside this calibration", i)
		}
		if !sources[span.CameraID].Media.Audio {
			return Invalid("sound span %d source has no audio", i)
		}
	}
	return nil
}
