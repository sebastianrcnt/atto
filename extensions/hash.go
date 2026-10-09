package extensions

import (
	"crypto/sha256"
	"encoding/hex"
)

// hash identifies a bundle's code: approving a project extension approves
// this hash, so a change to it or to a file it imports needs approval
// again.
func hash(code string) string {
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}
