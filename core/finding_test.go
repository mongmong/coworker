package core

import (
	"encoding/hex"
	"testing"
)

func TestComputeFingerprint_Deterministic(t *testing.T) {
	fp1 := ComputeFingerprint("main.go", 42, SeverityImportant, "Missing error check")
	fp2 := ComputeFingerprint("main.go", 42, SeverityImportant, "Missing error check")
	if fp1 != fp2 {
		t.Errorf("fingerprints differ for same input: %q vs %q", fp1, fp2)
	}
}

func TestComputeFingerprint_Length(t *testing.T) {
	fp := ComputeFingerprint("main.go", 42, SeverityImportant, "Missing error check")
	if len(fp) != 32 {
		t.Errorf("fingerprint length = %d, want 32", len(fp))
	}
}

func TestComputeFingerprint_ValidHex(t *testing.T) {
	fp := ComputeFingerprint("main.go", 42, SeverityImportant, "Missing error check")
	_, err := hex.DecodeString(fp)
	if err != nil {
		t.Errorf("fingerprint is not valid hex: %v", err)
	}
}

func TestComputeFingerprint_DiffersByPath(t *testing.T) {
	fp1 := ComputeFingerprint("main.go", 42, SeverityImportant, "Missing error check")
	fp2 := ComputeFingerprint("other.go", 42, SeverityImportant, "Missing error check")
	if fp1 == fp2 {
		t.Error("fingerprints should differ when path differs")
	}
}

func TestComputeFingerprint_DiffersByLine(t *testing.T) {
	fp1 := ComputeFingerprint("main.go", 42, SeverityImportant, "Missing error check")
	fp2 := ComputeFingerprint("main.go", 99, SeverityImportant, "Missing error check")
	if fp1 == fp2 {
		t.Error("fingerprints should differ when line differs")
	}
}

func TestComputeFingerprint_DiffersBySeverity(t *testing.T) {
	fp1 := ComputeFingerprint("main.go", 42, SeverityImportant, "Missing error check")
	fp2 := ComputeFingerprint("main.go", 42, SeverityMinor, "Missing error check")
	if fp1 == fp2 {
		t.Error("fingerprints should differ when severity differs")
	}
}

func TestComputeFingerprint_DiffersByBody(t *testing.T) {
	fp1 := ComputeFingerprint("main.go", 42, SeverityImportant, "Missing error check")
	fp2 := ComputeFingerprint("main.go", 42, SeverityImportant, "Different body text")
	if fp1 == fp2 {
		t.Error("fingerprints should differ when body differs")
	}
}
