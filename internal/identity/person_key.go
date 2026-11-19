package identity

// FeishuPersonKeySQL derives the same presentation key from an unambiguous
// open_id column in directory and account reads for the configured app.
func FeishuPersonKeySQL(openIDColumn string) string {
	return `encode(sha256(convert_to(` + openIDColumn + `, 'UTF8')), 'hex')`
}
