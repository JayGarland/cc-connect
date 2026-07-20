package core

import (
	"crypto/sha256"
	"encoding/hex"
)

// contentDigest is the stable generation identity for an archive letter.
// Filesystem timestamps are only scan hints and must never define callbacks.
func contentDigest(content []byte) string {
	sum := sha256.Sum256(content)
	// Telegram callback_data is capped at 64 bytes. A 96-bit token leaves
	// room for command and L-ID while keeping collision risk negligible for a
	// local delivery ledger.
	return hex.EncodeToString(sum[:12])
}
