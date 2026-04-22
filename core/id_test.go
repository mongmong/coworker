package core

import (
	"encoding/hex"
	"testing"
)

func TestNewID_Length(t *testing.T) {
	id := NewID()
	if len(id) != 32 {
		t.Errorf("NewID() length = %d, want 32", len(id))
	}
}

func TestNewID_ValidHex(t *testing.T) {
	id := NewID()
	_, err := hex.DecodeString(id)
	if err != nil {
		t.Errorf("NewID() is not valid hex: %v", err)
	}
}

func TestNewID_Unique(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id := NewID()
		if seen[id] {
			t.Fatalf("NewID() produced duplicate at iteration %d: %s", i, id)
		}
		seen[id] = true
	}
}
