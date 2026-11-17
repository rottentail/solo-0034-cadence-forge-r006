package encoding

import (
	"cadenceforge/internal/domain"
	"cadenceforge/internal/media"
	"fmt"
	"math"
	"strings"
)

func seconds(ms int64) string { return fmt.Sprintf("%.3f", float64(ms)/1000) }

func frameCount(durationMS int64, fps int) int {
	n := int(math.Floor(float64(durationMS)*float64(fps)/1000 + 0.5))
	if n < 1 {
		n = 1
	}
	return n
}

// Arguments builds a real FFmpeg filter graph. Synced plans use separately
// editable picture pieces and continuous sound spans; linear plans keep the
// original compacted layout.
func Arguments(render domain.Render, paths map[string]string, output string) ([]string, error) {
	if render.Plan.Sync != nil {
		return syncedArguments(render, paths, output)
	}
	return linearArguments(render, paths, output)
}

func baseArguments() []string {
	return []string{"-nostdin", "-y", "-hide_banner", "-loglevel", "error", "-filter_complex_threads", "1"}
}

func linearArguments(render domain.Render, paths map[string]string, output string) ([]string, error) {
	r := render.Recipe
	args := baseArguments()
	filters := []string{}
	joins := []string{}
	for i, p := range render.Plan.Placements {
		path, ok := paths[p.SourceID]
		if !ok {
			return nil, fmt.Errorf("source object missing")
		}
		args = append(args, "-protocol_whitelist", "file,pipe", "-format_whitelist", media.Formats, "-i", path)
		audio := fmt.Sprintf("[%d:a:0]atrim=start=%s:end=%s,asetpts=PTS-STARTPTS,aresample=%d,aformat=sample_fmts=fltp:channel_layouts=stereo,volume=%.3fdB[a%d]", i, seconds(p.SourceInMS), seconds(p.SourceOutMS), r.SampleRate, p.GainDB, i)
		if !render.Sources[p.SourceID].Media.Audio {
			audio = fmt.Sprintf("anullsrc=r=%d:cl=stereo,atrim=duration=%s,asetpts=PTS-STARTPTS[a%d]", r.SampleRate, seconds(p.OutputOutMS-p.OutputInMS), i)
		}
		filters = append(filters, audio)
		if r.Format == "mp4" {
			filters = append(filters, fmt.Sprintf("[%d:v:0]trim=start=%s:end=%s,setpts=PTS-STARTPTS,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=%d,format=yuv420p[v%d]", i, seconds(p.SourceInMS), seconds(p.SourceOutMS), r.Width, r.Height, r.Width, r.Height, r.FPS, i))
			joins = append(joins, fmt.Sprintf("[v%d][a%d]", i, i))
		} else {
			joins = append(joins, fmt.Sprintf("[a%d]", i))
		}
	}
	if len(joins) == 0 {
		return nil, fmt.Errorf("empty render")
	}
	if r.Format == "mp4" {
		filters = append(filters, strings.Join(joins, "")+fmt.Sprintf("concat=n=%d:v=1:a=1[vcat][acat]", len(joins)))
	} else {
		filters = append(filters, strings.Join(joins, "")+fmt.Sprintf("concat=n=%d:v=0:a=1[acat]", len(joins)))
	}
	return finishArguments(render, args, filters, output), nil
}

// syncedArguments keeps picture and sound independent. Picture pieces are cut
// at calibration corrections so each piece runs at natural speed; sound spans
// are continuous natural-speed runs, never spliced at a picture correction.
func syncedArguments(render domain.Render, paths map[string]string, output string) ([]string, error) {
	r := render.Recipe
	plan := render.Plan
	if len(plan.Sound) == 0 {
		return nil, fmt.Errorf("synced render has no sound")
	}
	args := baseArguments()
	filters := []string{}
	videoJoins := []string{}
	audioJoins := []string{}
	input := 0
	base := plan.ProgramStartMS
	if r.Format == "mp4" {
		if len(plan.Video) == 0 {
			return nil, fmt.Errorf("MP4 synced render has no picture")
		}
		videoInputs := map[string]int{}
		for i, piece := range plan.Video {
			path, ok := paths[piece.CameraID]
			if !ok {
				return nil, fmt.Errorf("source object missing")
			}
			index, exists := videoInputs[piece.CameraID]
			if !exists {
				args = append(args, "-protocol_whitelist", "file,pipe", "-format_whitelist", media.Formats, "-i", path)
				index = input
				videoInputs[piece.CameraID] = index
				input++
			}
			frames := frameCount(piece.ProgramOutMS-base, r.FPS) - frameCount(piece.ProgramInMS-base, r.FPS)
			if frames < 1 {
				frames = 1
			}
			filters = append(filters, fmt.Sprintf("[%d:v:0]trim=start=%s:end=%s,setpts=PTS-STARTPTS,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=%d,trim=end_frame=%d,setpts=PTS-STARTPTS,format=yuv420p[v%d]", index, seconds(piece.SourceInMS), seconds(piece.SourceOutMS), r.Width, r.Height, r.Width, r.Height, r.FPS, frames, i))
			videoJoins = append(videoJoins, fmt.Sprintf("[v%d]", i))
		}
	}
	// Sound spans cover the program exactly and are contiguous, so their
	// concatenation lands each span at its selected program position.
	audioInputs := map[string]int{}
	for i, span := range plan.Sound {
		if !render.Sources[span.CameraID].Media.Audio {
			return nil, fmt.Errorf("synced sound span has no audio")
		}
		path, ok := paths[span.CameraID]
		if !ok {
			return nil, fmt.Errorf("source object missing")
		}
		index, exists := audioInputs[span.CameraID]
		if !exists {
			args = append(args, "-protocol_whitelist", "file,pipe", "-format_whitelist", media.Formats, "-i", path)
			index = input
			audioInputs[span.CameraID] = index
			input++
		}
		filters = append(filters, fmt.Sprintf("[%d:a:0]atrim=start=%s:end=%s,asetpts=PTS-STARTPTS,aresample=%d,aformat=sample_fmts=fltp:channel_layouts=stereo,volume=%.3fdB[a%d]", index, seconds(span.SourceInMS), seconds(span.SourceOutMS), r.SampleRate, span.GainDB, i))
		audioJoins = append(audioJoins, fmt.Sprintf("[a%d]", i))
	}
	if r.Format == "mp4" {
		filters = append(filters, strings.Join(videoJoins, "")+fmt.Sprintf("concat=n=%d:v=1:a=0[vcat]", len(videoJoins)))
	}
	filters = append(filters, strings.Join(audioJoins, "")+fmt.Sprintf("concat=n=%d:v=0:a=1[acat]", len(audioJoins)))
	return finishArguments(render, args, filters, output), nil
}

func finishArguments(render domain.Render, args, filters []string, output string) []string {
	r := render.Recipe
	if r.Format == "mp4" {
		if r.Normalize {
			filters = append(filters, fmt.Sprintf("[acat]loudnorm=I=%.2f:TP=-1.5:LRA=11,aresample=%d[acatn]", r.TargetLUFS, r.SampleRate))
		} else {
			filters = append(filters, "[acat]anull[acatn]")
		}
		filters = append(filters, "[vcat]null[vout]")
	} else if r.Normalize {
		filters = append(filters, fmt.Sprintf("[acat]loudnorm=I=%.2f:TP=-1.5:LRA=11,aresample=%d[aout]", r.TargetLUFS, r.SampleRate))
	} else {
		filters = append(filters, "[acat]anull[aout]")
	}
	args = append(args, "-filter_complex", strings.Join(filters, ";"))
	if r.Format == "mp4" {
		args = append(args, "-map", "[vout]", "-map", "[acatn]", "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-threads", "1", "-c:a", "aac", "-b:a", "160k", "-movflags", "+faststart")
	} else {
		args = append(args, "-map", "[aout]", "-c:a", "pcm_s16le")
	}
	return append(args, "-ar", fmt.Sprint(r.SampleRate), "-t", seconds(render.Plan.DurationMS), "-map_metadata", "-1", output)
}
