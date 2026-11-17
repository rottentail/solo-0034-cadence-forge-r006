package timeline

import (
	"testing"

	"cadenceforge/internal/domain"
)

func fixture() (domain.Sheet, domain.Recipe, map[string]domain.Source, domain.Calibration) {
	sources := map[string]domain.Source{
		"master": {ID: "master", Digest: "a", Media: domain.Media{DurationMS: 20000, Video: true, Audio: true, Width: 160, Height: 90, SampleRate: 48000, Channels: 2}},
		"camera": {ID: "camera", Digest: "b", Media: domain.Media{DurationMS: 20000, Video: true, Audio: true, Width: 160, Height: 90, SampleRate: 48000, Channels: 2}},
	}
	cal := domain.Calibration{ID: "cal", Revision: 1, Name: "pair", MasterSourceID: "master",
		CameraSourceID: "camera", ToleranceMS: 20,
		Anchors: []domain.SyncAnchor{{MasterMS: 0, CameraMS: 0}, {MasterMS: 10000, CameraMS: 10040}}}
	sheet := domain.Sheet{Name: "cut", SyncID: "cal",
		Shots: []domain.Shot{{CameraID: "master", ProgramInMS: 2000, ProgramOutMS: 4000},
			{CameraID: "camera", ProgramInMS: 4000, ProgramOutMS: 8000}},
		Sound: []domain.SoundSpan{{CameraID: "master", ProgramInMS: 2000, ProgramOutMS: 8000}}}
	recipe := domain.Recipe{Name: "r", Format: "mp4", Width: 160, Height: 90, FPS: 24, SampleRate: 48000}
	return sheet, recipe, sources, cal
}

func TestBuildSyncedUsesSharedCorrespondence(t *testing.T) {
	sheet, recipe, sources, cal := fixture()
	plan, err := Build(sheet, recipe, sources, &cal)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Sync == nil || !plan.Sync.NaturalSpeed || plan.Sync.TempoChange {
		t.Fatal("policy facts missing")
	}
	if len(plan.Sync.Pieces) == 0 {
		t.Fatal("no shared correspondence pieces")
	}
	// Sound is arranged independently of picture; changing shots must not move it.
	other := sheet
	other.Shots = []domain.Shot{{CameraID: "camera", ProgramInMS: 2000, ProgramOutMS: 8000}}
	second, err := Build(other, recipe, sources, &cal)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Sound) != len(plan.Sound) ||
		second.Sound[0].SourceInMS != plan.Sound[0].SourceInMS ||
		second.Sound[0].OffsetMS != plan.Sound[0].OffsetMS {
		t.Fatal("switching picture cameras changed the sound arrangement")
	}
	for _, v := range plan.Video {
		if v.SourceOutMS-v.SourceInMS != v.ProgramOutMS-v.ProgramInMS {
			t.Fatalf("picture piece is not natural speed: %+v", v)
		}
	}
}

func TestBuildSyncedRejectsProgramOutsideAnchors(t *testing.T) {
	sheet, recipe, sources, cal := fixture()
	sheet.Shots = []domain.Shot{{CameraID: "master", ProgramInMS: 9500, ProgramOutMS: 10500}}
	sheet.Sound = []domain.SoundSpan{{CameraID: "master", ProgramInMS: 9500, ProgramOutMS: 10500}}
	if _, err := Build(sheet, recipe, sources, &cal); err == nil {
		t.Fatal("program outside confirmed anchors accepted")
	}
}

func TestBuildSyncedRequiresExplicitCalibration(t *testing.T) {
	sheet, recipe, sources, _ := fixture()
	if _, err := Build(sheet, recipe, sources, nil); err == nil {
		t.Fatal("synced sheet built without a calibration")
	}
}

func TestBuildSyncedRejectsGappySound(t *testing.T) {
	sheet, recipe, sources, cal := fixture()
	sheet.Sound = []domain.SoundSpan{
		{CameraID: "master", ProgramInMS: 2000, ProgramOutMS: 5000},
		{CameraID: "master", ProgramInMS: 5500, ProgramOutMS: 8000},
	}
	if _, err := Build(sheet, recipe, sources, &cal); err == nil {
		t.Fatal("gapped sound arrangement accepted")
	}
}

func TestNaiveOffsetWouldBreakFarAnchor(t *testing.T) {
	// The same fixture, but with the camera shot placed on the far side of a
	// drift gap. A single start-offset translation would misplace the far end;
	// the built plan reports a splice and a bounded residual instead.
	sheet, recipe, sources, cal := fixture()
	sheet.Shots = []domain.Shot{{CameraID: "camera", ProgramInMS: 0, ProgramOutMS: 10000}}
	sheet.Sound = []domain.SoundSpan{{CameraID: "master", ProgramInMS: 0, ProgramOutMS: 10000}}
	plan, err := Build(sheet, recipe, sources, &cal)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Sync.MaxResidualMS > 20 {
		t.Fatalf("residual %d exceeds half of the 40ms drift", plan.Sync.MaxResidualMS)
	}
	last := plan.Sync.Anchors[len(plan.Sync.Anchors)-1]
	if !last.Exact {
		t.Fatal("far anchor is not exact")
	}
}
