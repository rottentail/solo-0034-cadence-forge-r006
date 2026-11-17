package caption

import (
	"cadenceforge/internal/domain"
	"fmt"
	"html"
	"sort"
	"strings"
)

type Interval struct {
	StartMS, EndMS int64
	Text           string
	Order          int
}

// Map projects source subtitle cues into output time. Linear plans intersect
// cues with placements. Synced plans map each cue through the frozen
// program-wide correspondence: a cue cut by a calibration splice appears as the
// pieces actually audible/visible, and material dropped by a splice yields no
// interval. Cues never invent time outside the programs they belong to.
func Map(sheet domain.Sheet, plan domain.Plan) []Interval {
	out := []Interval{}
	if plan.Sync != nil {
		return mapSynced(sheet, plan)
	}
	for _, p := range plan.Placements {
		for i, c := range sheet.Cues {
			if c.SourceID != p.SourceID {
				continue
			}
			start := max(c.StartMS, p.SourceInMS)
			end := min(c.EndMS, p.SourceOutMS)
			if end <= start {
				continue
			}
			out = append(out, Interval{StartMS: p.OutputInMS + start - p.SourceInMS, EndMS: p.OutputInMS + end - p.SourceInMS, Text: c.Text, Order: i})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartMS != out[j].StartMS {
			return out[i].StartMS < out[j].StartMS
		}
		return out[i].Order < out[j].Order
	})
	return out
}

func mapSynced(sheet domain.Sheet, plan domain.Plan) []Interval {
	out := []Interval{}
	for i, cue := range sheet.Cues {
		for _, span := range plan.Sound {
			if span.CameraID != cue.SourceID {
				continue
			}
			start := max(cue.StartMS, span.SourceInMS)
			end := min(cue.EndMS, span.SourceOutMS)
			if end <= start {
				continue
			}
			// A sound span is one natural-speed run at a fixed offset, so a
			// source interval maps to exactly one output interval. Cues dropped
			// by a splice on the other camera are simply never in this span.
			out = append(out, Interval{StartMS: span.ProgramInMS - plan.ProgramStartMS + start - span.SourceInMS, EndMS: span.ProgramInMS - plan.ProgramStartMS + end - span.SourceInMS, Text: cue.Text, Order: i})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartMS != out[j].StartMS {
			return out[i].StartMS < out[j].StartMS
		}
		return out[i].Order < out[j].Order
	})
	return out
}
func stamp(ms int64) string {
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, (ms/60000)%60, (ms/1000)%60, ms%1000)
}
func VTT(intervals []Interval) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for i, c := range intervals {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, stamp(c.StartMS), stamp(c.EndMS), strings.ReplaceAll(html.EscapeString(c.Text), "\n\n", "\n"))
	}
	return b.String()
}
