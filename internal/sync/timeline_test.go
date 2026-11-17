package sync

import (
	"testing"

	"cadenceforge/internal/domain"
)

func testPair(anchors ...domain.SyncAnchor) Pair {
	return NewPair(domain.Calibration{MasterSourceID: "a", CameraSourceID: "b", Anchors: anchors, ToleranceMS: 20})
}

func TestBuildMapsAnchorsExactly(t *testing.T) {
	p := testPair(domain.SyncAnchor{MasterMS: 0, CameraMS: 0}, domain.SyncAnchor{MasterMS: 10000, CameraMS: 10120})
	m, err := Build(p, 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range p.Anchors {
		source, ok := m.SourceAt("b", a.MasterMS)
		if !ok {
			t.Fatalf("no source at %d", a.MasterMS)
		}
		if source != a.CameraMS {
			t.Fatalf("anchor %d maps to %d, want %d", a.MasterMS, source, a.CameraMS)
		}
	}
	if m.MaxResidual("b", 0, 10000) > 60 {
		t.Fatalf("residual %d exceeds half-gap bound", m.MaxResidual("b", 0, 10000))
	}
}

func TestBuildKeepsNaturalSpeed(t *testing.T) {
	p := testPair(
		domain.SyncAnchor{MasterMS: 0, CameraMS: 0},
		domain.SyncAnchor{MasterMS: 5000, CameraMS: 5030},
		domain.SyncAnchor{MasterMS: 10000, CameraMS: 5060},
	)
	m, err := Build(p, 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	for _, piece := range m.Pieces {
		if piece.SourceOut()-piece.SourceIn() != piece.ProgramOut-piece.ProgramIn {
			t.Fatalf("piece changes source speed: %+v", piece)
		}
	}
	if len(m.Corrections()) != 2 {
		t.Fatalf("expected one splice per drift gap, got %d", len(m.Corrections()))
	}
	for _, piece := range m.Corrections() {
		if piece.SkipMS == 0 {
			t.Fatal("splice must remove or repeat material")
		}
	}
}

func TestBuildOutsideAnchorsRejected(t *testing.T) {
	p := testPair(domain.SyncAnchor{MasterMS: 1000, CameraMS: 900}, domain.SyncAnchor{MasterMS: 5000, CameraMS: 4950})
	if _, err := Build(p, 0, 4000); err == nil {
		t.Fatal("expected rejection outside confirmed anchors")
	}
}

func TestBoundariesAlignExactly(t *testing.T) {
	p := testPair(
		domain.SyncAnchor{MasterMS: 1000, CameraMS: 1100},
		domain.SyncAnchor{MasterMS: 9000, CameraMS: 9100},
	)
	m, err := Build(p, 3000, 7000)
	if err != nil {
		t.Fatal(err)
	}
	for _, boundary := range []int64{3000, 7000} {
		got, _ := m.SourceAt("b", boundary)
		if got != boundary+100 {
			t.Fatalf("boundary %d maps to %d, want %d", boundary, got, boundary+100)
		}
	}
}

func TestInvertReportsUsedIntervals(t *testing.T) {
	p := testPair(domain.SyncAnchor{MasterMS: 0, CameraMS: 0}, domain.SyncAnchor{MasterMS: 10000, CameraMS: 10100})
	m, err := Build(p, 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Invert("b", 0, 10000)) != len(m.Pieces) {
		t.Fatalf("inversion does not cover every piece")
	}
}

func TestMasterHasNoDrift(t *testing.T) {
	p := testPair(domain.SyncAnchor{MasterMS: 0, CameraMS: 0}, domain.SyncAnchor{MasterMS: 10000, CameraMS: 10000})
	m, err := Build(p, 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	for program := int64(0); program < 10000; program += 500 {
		got, ok := m.SourceAt("a", program)
		if !ok || got != program {
			t.Fatalf("master timeline is not the identity at %d", program)
		}
		if m.MaxResidual("a", 0, 10000) != 0 {
			t.Fatal("master clock must have zero residual")
		}
	}
}
