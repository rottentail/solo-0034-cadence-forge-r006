package domain

import "testing"

func TestRoundHalfAwayKeepsExactIntegers(t *testing.T) {
	cases := map[float64]int64{1000: 1000, 1000.4: 1000, 1000.5: 1001, -1000.5: -1001, 11990.4: 11990}
	for in, want := range cases {
		if got := RoundHalfAway(in); got != want {
			t.Fatalf("RoundHalfAway(%v) = %d, want %d", in, got, want)
		}
	}
}

func TestDriftAtAnchorsIsExact(t *testing.T) {
	anchors := []SyncAnchor{{0, 0}, {6000, 6005}, {12000, 12008}}
	for _, a := range anchors {
		if got := RoundHalfAway(DriftAt(anchors, a.MasterMS)); got != a.CameraMS-a.MasterMS {
			t.Fatalf("drift at %d = %d, want %d", a.MasterMS, got, a.CameraMS-a.MasterMS)
		}
	}
}

func TestValidateCalibrationRejectsNonAdvancingAnchors(t *testing.T) {
	sources := map[string]Source{
		"m": {ID: "m", Media: Media{Audio: true, DurationMS: 20000}},
		"c": {ID: "c", Media: Media{Audio: true, DurationMS: 20000}},
	}
	base := Calibration{Name: "x", MasterSourceID: "m", CameraSourceID: "c", ToleranceMS: 20,
		Anchors: []SyncAnchor{{0, 0}, {1000, 1000}}}
	if err := ValidateCalibration(base, sources); err != nil {
		t.Fatalf("valid calibration rejected: %v", err)
	}
	backwards := base
	backwards.Anchors = []SyncAnchor{{1000, 1000}, {1000, 1010}}
	if err := ValidateCalibration(backwards, sources); err == nil {
		t.Fatal("non-advancing anchors accepted")
	}
	same := base
	same.CameraSourceID = "m"
	if err := ValidateCalibration(same, sources); err == nil {
		t.Fatal("single-source calibration accepted")
	}
	noAudio := map[string]Source{
		"m": {ID: "m", Media: Media{Audio: true, DurationMS: 20000}},
		"c": {ID: "c", Media: Media{Audio: false, DurationMS: 20000}},
	}
	if err := ValidateCalibration(base, noAudio); err == nil {
		t.Fatal("silent camera accepted as a clock reference")
	}
}
