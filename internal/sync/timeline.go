// Package sync derives one program-wide clock correspondence for two field
// recordings whose clocks drift apart.
//
// Chosen policy: natural speed with anchored correspondence.
//
// Program time is the master camera's source clock. Every selected source
// interval is used at its recorded rate: there is no atempo/rubberband
// processing and no sample-rate conversion beyond the delivery recipe's output
// rate. The correspondence is built once for the whole program and is shared by
// picture selection and sound arrangement, so changing a shot or a sound span
// never changes the confirmed mapping.
//
// Every confirmed anchor inside the program maps exactly, and both program
// boundaries map to the interpolated confirmed correspondence, so the same
// opening and closing events align. Between two exact points whose offsets
// differ by Δ, one source-clock jump of Δ is placed at their midpoint. The jump
// removes Δ of camera material when the camera runs fast and repeats Δ when it
// runs slow. Exactness at both ends and natural speed in between are
// incompatible with removing the drift inside the interval; the midpoint
// placement divides the residual equally, bounding it at |Δ|/2, and the
// residual intervals are reported rather than hidden.
package sync

import (
	"fmt"
	"sort"

	"cadenceforge/internal/domain"
)

// Pair is the frozen calibration used by one deliverable.
type Pair struct {
	Calibration domain.Calibration
	Anchors     []domain.SyncAnchor
	Master      string
	Camera      string
}

func NewPair(cal domain.Calibration) Pair {
	return Pair{Calibration: cal, Anchors: domain.SortedAnchors(cal.Anchors), Master: cal.MasterSourceID, Camera: cal.CameraSourceID}
}

func (p Pair) Known(id string) bool { return id == p.Master || id == p.Camera }

// Drift is the rounded camera clock minus master clock at a program instant.
func (p Pair) Drift(program int64) int64 {
	return domain.RoundHalfAway(domain.DriftAt(p.Anchors, program))
}

// GapAt returns the confirmed anchor pair surrounding a program instant.
func (p Pair) GapAt(program int64) (domain.SyncAnchor, domain.SyncAnchor) {
	anchors := p.Anchors
	if program <= anchors[0].MasterMS {
		return anchors[0], anchors[0]
	}
	last := anchors[len(anchors)-1]
	if program >= last.MasterMS {
		return last, last
	}
	for i := 0; i+1 < len(anchors); i++ {
		if program >= anchors[i].MasterMS && program <= anchors[i+1].MasterMS {
			return anchors[i], anchors[i+1]
		}
	}
	return last, last
}

// Piece is one constant-offset run. Within [ProgramIn, ProgramOut) the camera
// clock is program+Offset, so camera material runs at its natural speed. A
// correction piece begins with a source-clock jump of SkipMS.
type Piece struct {
	ProgramIn    int64 `json:"program_in_ms"`
	ProgramOut   int64 `json:"program_out_ms"`
	Offset       int64 `json:"offset_ms"`
	Correction   bool  `json:"correction"`
	SkipMS       int64 `json:"skip_ms"`
	GapMasterIn  int64 `json:"gap_master_in_ms"`
	GapMasterOut int64 `json:"gap_master_out_ms"`
}

func (s Piece) SourceIn() int64  { return s.ProgramIn + s.Offset }
func (s Piece) SourceOut() int64 { return s.ProgramOut + s.Offset }

// Map is the single program-wide correspondence shared by picture and sound.
type Map struct {
	Pair   Pair
	Start  int64
	End    int64
	Pieces []Piece
}

// Build creates the global correspondence for program interval [start,end).
// start and end must lie inside the confirmed anchor span; anything else would
// be unconfirmed extrapolation.
func Build(pair Pair, start, end int64) (Map, error) {
	if start >= end {
		return Map{}, fmt.Errorf("program interval is empty")
	}
	anchors := pair.Anchors
	if len(anchors) < 2 {
		return Map{}, fmt.Errorf("calibration requires at least two confirmed anchors")
	}
	if start < anchors[0].MasterMS || end > anchors[len(anchors)-1].MasterMS {
		return Map{}, fmt.Errorf("program lies outside confirmed anchors")
	}
	m := Map{Pair: pair, Start: start, End: end}
	type exact struct {
		program int64
		offset  int64
	}
	// Exact points: the two program boundaries at their interpolated confirmed
	// correspondence, plus every confirmed anchor inside the program.
	points := []exact{{program: start, offset: pair.Drift(start)}}
	for _, a := range anchors {
		if a.MasterMS > start && a.MasterMS < end {
			points = append(points, exact{program: a.MasterMS, offset: a.CameraMS - a.MasterMS})
		}
	}
	points = append(points, exact{program: end, offset: pair.Drift(end)})
	sort.Slice(points, func(i, j int) bool { return points[i].program < points[j].program })
	for i := 0; i+1 < len(points); i++ {
		a, b := points[i], points[i+1]
		if b.program <= a.program {
			continue
		}
		if a.offset == b.offset {
			m.appendPiece(a.program, b.program, a.offset, false, 0)
			continue
		}
		mid := a.program + (b.program-a.program)/2
		if mid <= a.program {
			m.appendPiece(a.program, b.program, b.offset, true, b.offset-a.offset)
			continue
		}
		m.appendPiece(a.program, mid, a.offset, false, 0)
		m.appendPiece(mid, b.program, b.offset, true, b.offset-a.offset)
	}
	if len(m.Pieces) == 0 {
		return Map{}, fmt.Errorf("no mapping pieces")
	}
	return m, nil
}

func (m *Map) appendPiece(in, out, offset int64, correction bool, skip int64) {
	if out <= in {
		return
	}
	a, b := m.Pair.GapAt(in)
	piece := Piece{ProgramIn: in, ProgramOut: out, Offset: offset, Correction: correction, SkipMS: skip, GapMasterIn: a.MasterMS, GapMasterOut: b.MasterMS}
	if n := len(m.Pieces); n > 0 {
		last := &m.Pieces[n-1]
		if last.ProgramOut == piece.ProgramIn && last.Offset == piece.Offset && !piece.Correction {
			last.ProgramOut = piece.ProgramOut
			return
		}
	}
	m.Pieces = append(m.Pieces, piece)
}

func (m Map) PieceAt(program int64) (Piece, bool) {
	for _, s := range m.Pieces {
		if program >= s.ProgramIn && program < s.ProgramOut {
			return s, true
		}
	}
	if program == m.End && len(m.Pieces) > 0 {
		return m.Pieces[len(m.Pieces)-1], true
	}
	return Piece{}, false
}

// SourceAt maps a program instant to a camera's source clock. The master
// camera is the program clock itself.
func (m Map) SourceAt(cameraID string, program int64) (int64, bool) {
	if cameraID == m.Pair.Master {
		if program < m.Start || program > m.End {
			return 0, false
		}
		return program, true
	}
	piece, ok := m.PieceAt(program)
	if !ok {
		return 0, false
	}
	return program + piece.Offset, true
}

// ResidualAt is the camera-clock mismatch against the linearly interpolated
// confirmed correspondence. It is zero at confirmed anchors and on the master.
func (m Map) ResidualAt(cameraID string, program int64) int64 {
	if cameraID == m.Pair.Master {
		return 0
	}
	piece, ok := m.PieceAt(program)
	if !ok {
		return 0
	}
	return piece.Offset - m.Pair.Drift(program)
}

// MaxResidual returns the largest absolute residual over [in,out).
func (m Map) MaxResidual(cameraID string, in, out int64) int64 {
	if cameraID == m.Pair.Master || out <= in {
		return 0
	}
	var worst int64
	for _, piece := range m.Pieces {
		lo, hi := max(in, piece.ProgramIn), min(out, piece.ProgramOut)
		if hi <= lo {
			continue
		}
		for _, sample := range []int64{lo, hi - 1} {
			if r := abs(m.ResidualAt(cameraID, sample)); r > worst {
				worst = r
			}
		}
	}
	return worst
}

// Corrections lists the global splices in program order.
func (m Map) Corrections() []Piece {
	out := []Piece{}
	for _, piece := range m.Pieces {
		if piece.Correction {
			out = append(out, piece)
		}
	}
	return out
}

type ProgramInterval struct {
	InMS, OutMS int64
}

// Invert maps a camera source interval back to the program intervals in which
// that material is used. Material removed by a splice has no program interval;
// material repeated by a negative splice yields two.
func (m Map) Invert(cameraID string, sourceIn, sourceOut int64) []ProgramInterval {
	if cameraID == m.Pair.Master {
		lo, hi := max(sourceIn, m.Start), min(sourceOut, m.End)
		if hi <= lo {
			return nil
		}
		return []ProgramInterval{{InMS: lo, OutMS: hi}}
	}
	out := []ProgramInterval{}
	for _, piece := range m.Pieces {
		lo, hi := max(sourceIn, piece.SourceIn()), min(sourceOut, piece.SourceOut())
		if hi <= lo {
			continue
		}
		pi := ProgramInterval{InMS: lo - piece.Offset, OutMS: hi - piece.Offset}
		pi.InMS, pi.OutMS = max(pi.InMS, m.Start), min(pi.OutMS, m.End)
		if pi.OutMS > pi.InMS {
			out = append(out, pi)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InMS < out[j].InMS })
	return out
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
