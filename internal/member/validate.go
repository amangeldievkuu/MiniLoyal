package member

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/db/sqlc"
)

// Limits on what a member may store.
const (
	MaxDisplayNameLen   = 100
	MaxPhoneLen         = 32
	MaxCustomFieldKeys  = 32
	MaxCustomFieldKey   = 64
	MaxCustomFieldBytes = 8 << 10
	MaxConsentVersion   = 64
)

var genders = map[string]bool{"female": true, "male": true, "other": true}

// validateUpdate turns an Update into query parameters or a validation error
// listing every bad field.
func validateUpdate(in Update, now time.Time) (sqlc.UpdateMemberProfileParams, error) {
	fields := map[string]string{}
	params := sqlc.UpdateMemberProfileParams{SetPhone: in.SetPhone, SetBirthday: in.SetBirthday, SetGender: in.SetGender}

	if in.DisplayName != nil {
		name := strings.TrimSpace(*in.DisplayName)
		if n := len(name); n < 1 || n > MaxDisplayNameLen || !cleanText(name) {
			fields["display_name"] = fmt.Sprintf("must be 1 to %d characters of text", MaxDisplayNameLen)
		} else {
			params.DisplayName = &name
		}
	}
	if in.SetPhone && in.Phone != nil {
		phone := strings.TrimSpace(*in.Phone)
		switch {
		case phone == "":
			params.Phone = nil
		case len(phone) > MaxPhoneLen || !validPhone(phone):
			fields["phone"] = fmt.Sprintf("must be at most %d characters of digits, spaces and a leading plus", MaxPhoneLen)
		default:
			params.Phone = &phone
		}
	}
	if in.SetBirthday && in.Birthday != nil {
		b := in.Birthday.UTC()
		switch {
		case b.After(now):
			fields["birthday"] = "must not be in the future"
		case b.Year() < 1900:
			fields["birthday"] = "must be after 1900"
		default:
			params.Birthday = pgtype.Date{Time: time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
		}
	}
	if in.SetGender && in.Gender != nil {
		g := strings.ToLower(strings.TrimSpace(*in.Gender))
		if !genders[g] {
			fields["gender"] = "must be female, male or other"
		} else {
			params.Gender = &g
		}
	}
	if in.CustomFields != nil {
		raw, err := validateCustomFields(in.CustomFields)
		if err != nil {
			fields["custom_fields"] = err.Error()
		} else {
			params.CustomFields = raw
		}
	}
	if len(fields) > 0 {
		return sqlc.UpdateMemberProfileParams{}, apierr.Validation(fields)
	}
	return params, nil
}

func validPhone(s string) bool {
	for i, r := range s {
		switch {
		case unicode.IsDigit(r), r == ' ':
		case r == '+' && i == 0:
		default:
			return false
		}
	}
	return true
}

func validateCustomFields(m map[string]any) ([]byte, error) {
	if len(m) > MaxCustomFieldKeys {
		return nil, fmt.Errorf("at most %d keys", MaxCustomFieldKeys)
	}
	for k := range m {
		if k == "" || len(k) > MaxCustomFieldKey || !cleanText(k) {
			return nil, fmt.Errorf("keys must be 1 to %d characters of text", MaxCustomFieldKey)
		}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("must be a JSON object")
	}
	// Postgres text and jsonb refuse NUL; it survives marshalling as \u0000.
	if bytes.Contains(raw, []byte(`\u0000`)) {
		return nil, fmt.Errorf("must not contain NUL characters")
	}
	if len(raw) > MaxCustomFieldBytes {
		return nil, fmt.Errorf("at most %d bytes", MaxCustomFieldBytes)
	}
	return raw, nil
}

func validateConsentVersion(v string) error {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > MaxConsentVersion || !cleanText(v) {
		return apierr.Validation(map[string]string{"version": fmt.Sprintf("must be 1 to %d characters", MaxConsentVersion)})
	}
	return nil
}

// cleanText reports whether s is valid UTF-8 without NUL, which is what
// Postgres text columns accept. Anything else is a validation error, not a
// database error.
func cleanText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
