package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewID generates a 32-character hex string from 16 random bytes.
// Used for RunID, JobID, EventID, FindingID, ArtifactID.
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}
