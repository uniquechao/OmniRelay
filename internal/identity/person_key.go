package identity

import (
	"crypto/sha256"
	"encoding/hex"
)

// FeishuPersonKey matches the SQL presentation key used in account reads.
func FeishuPersonKey(openID string) string {
	digest := sha256.Sum256([]byte(openID))
	return hex.EncodeToString(digest[:])
}

// FeishuPersonKeySQL derives the same presentation key from an unambiguous
// open_id column in directory and account reads for the configured app.
func FeishuPersonKeySQL(openIDColumn string) string {
	return `encode(sha256(convert_to(` + openIDColumn + `, 'UTF8')), 'hex')`
}
