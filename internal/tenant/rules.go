package tenant

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/principal"
)

// Scopes an API key may carry (mirrors ApiKeyScope in the OpenAPI document).
var Scopes = []string{
	"members:read",
	"members:write",
	"points:write",
	"coupons:write",
	"messages:send",
	"webhooks:manage",
	"config:read",
}

// Environments of a LINE MINI App channel.
var Environments = []string{"developing", "review", "published"}

var (
	slugPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,48}[a-z0-9]$`)
	localePattern = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
)

// planAppLimit is how many apps a plan allows; 0 means unlimited.
func planAppLimit(plan string) int {
	switch plan {
	case "free", "starter", "pro":
		return 1
	case "agency":
		return 50
	case "enterprise":
		return 0
	default:
		return 1
	}
}

// canGrant reports whether an actor with role actor may assign target.
func canGrant(actor, target principal.Role) bool {
	if !target.Valid() {
		return false
	}
	switch actor {
	case principal.RoleOwner:
		return true
	case principal.RoleAdmin:
		return target != principal.RoleOwner
	default:
		return false
	}
}

// deriveSlug turns a display name into a URL-safe slug, or a random one when
// the name has no usable ASCII (for example a Chinese brand name).
func deriveSlug(name string) string {
	var b strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || unicode.IsSpace(r):
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > 50 {
		slug = strings.Trim(slug[:50], "-")
	}
	if len(slug) < 2 {
		return "app-" + randomHex(3)
	}
	return slug
}

// withSuffix appends a short random suffix, keeping the slug within limits.
func withSuffix(slug string) string {
	if len(slug) > 43 {
		slug = strings.Trim(slug[:43], "-")
	}
	return slug + "-" + randomHex(3)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("tenant: random: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// inviteAlphabet avoids characters that are easy to confuse when typed.
const inviteAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// InviteCodeLen is the length of a staff invite code: 12 characters from a
// 32-letter alphabet is 60 bits, matched within one app and rate limited.
const InviteCodeLen = 12

func randomInviteCode() string {
	b := make([]byte, InviteCodeLen)
	if _, err := rand.Read(b); err != nil {
		panic("tenant: random: " + err.Error())
	}
	for i := range b {
		b[i] = inviteAlphabet[int(b[i])%len(inviteAlphabet)]
	}
	return string(b)
}

func validateName(field, name string, max int) map[string]string {
	n := strings.TrimSpace(name)
	if n == "" {
		return map[string]string{field: "is required"}
	}
	if len(n) > max {
		return map[string]string{field: "is too long"}
	}
	return nil
}

func validateSlug(slug string) error {
	if !slugPattern.MatchString(slug) {
		return apierr.Validation(map[string]string{"slug": "use 3 to 50 lowercase letters, digits or hyphens"})
	}
	return nil
}

func validateLocale(locale string) error {
	if !localePattern.MatchString(locale) || len(locale) > 16 {
		return apierr.Validation(map[string]string{"locale": "must be a BCP 47 tag such as zh-TW"})
	}
	return nil
}

func validateStatus(status string) error {
	if status != "active" && status != "disabled" {
		return apierr.Validation(map[string]string{"status": "must be active or disabled"})
	}
	return nil
}

func validateStaffRole(role string) error {
	if role != "cashier" && role != "manager" {
		return apierr.Validation(map[string]string{"role": "must be cashier or manager"})
	}
	return nil
}

func validateEnvironment(env string) error {
	if !slices.Contains(Environments, env) {
		return apierr.Validation(map[string]string{"environment": "must be developing, review or published"})
	}
	return nil
}

func validateScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return nil, apierr.Validation(map[string]string{"scopes": "choose at least one scope"})
	}
	out := make([]string, 0, len(scopes))
	for _, sc := range scopes {
		if !slices.Contains(Scopes, sc) {
			return nil, apierr.Validation(map[string]string{"scopes": "unknown scope " + sc})
		}
		if !slices.Contains(out, sc) {
			out = append(out, sc)
		}
	}
	slices.Sort(out)
	return out, nil
}
