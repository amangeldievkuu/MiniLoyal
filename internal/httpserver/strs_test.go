package httpserver

// deref returns *p, or "" for nil; LINE user IDs are optional in responses
// because the staff role sees a redacted member.
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
