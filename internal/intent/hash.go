package intent

import (
	"crypto/sha256"
	"encoding/hex"
)

// IntentSHA256 returns the lowercase SHA-256 digest of the exact Intent bytes.
func IntentSHA256(intent []byte) string {
	digest := sha256.Sum256(intent)
	return hex.EncodeToString(digest[:])
}

// ApprovalSHA256 returns SHA-256(Intent || NUL || acceptance), using both
// inputs exactly as supplied without text decoding or line-ending changes.
func ApprovalSHA256(intent, acceptance []byte) string {
	hash := sha256.New()
	_, _ = hash.Write(intent)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(acceptance)
	return hex.EncodeToString(hash.Sum(nil))
}
