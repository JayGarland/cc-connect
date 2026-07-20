package core

import (
	"crypto/sha256"
	"encoding/hex"
)

// contentDigest is the stable generation identity for an archive letter.
// Filesystem timestamps are only scan hints and must never define callbacks.
func contentDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
