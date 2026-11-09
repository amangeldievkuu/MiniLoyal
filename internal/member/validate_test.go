package member

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
)

func str(s string) *string { return &s }

func TestValidateUpdate(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	future := now.Add(48 * time.Hour)
	past := time.Date(1990, 5, 6, 0, 0, 0, 0, time.UTC)
	ancient := time.Date(1850, 1, 1, 0, 0, 0, 0, time.UTC)
	big := map[string]any{}
	for i := 0; i < 33; i++ {
		big[strings.Repeat("k", 1)+string(rune('a'+i%26))+string(rune('a'+i/26))] = i
	}
	huge := map[string]any{"blob": strings.Repeat("x", MaxCustomFieldBytes)}

	cases := []struct {
		name  string
		in    Update
		field string // empty = valid
	}{
		{"empty update is fine", Update{}, ""},
		{"display name", Update{DisplayName: str("  Mei  ")}, ""},
		{"display name too long", Update{DisplayName: str(strings.Repeat("x", 101))}, "display_name"},
		{"display name blank", Update{DisplayName: str("   ")}, "display_name"},
		{"phone ok", Update{Phone: str("+886 912 345 678"), SetPhone: true}, ""},
		{"phone letters", Update{Phone: str("0912-ABC"), SetPhone: true}, "phone"},
		{"phone plus inside", Update{Phone: str("09+12"), SetPhone: true}, "phone"},
		{"phone cleared", Update{Phone: str(""), SetPhone: true}, ""},
		{"birthday ok", Update{Birthday: &past, SetBirthday: true}, ""},
		{"birthday future", Update{Birthday: &future, SetBirthday: true}, "birthday"},
		{"birthday ancient", Update{Birthday: &ancient, SetBirthday: true}, "birthday"},
		{"gender ok", Update{Gender: str("Female"), SetGender: true}, ""},
		{"gender bad", Update{Gender: str("yes"), SetGender: true}, "gender"},
		{"custom fields ok", Update{CustomFields: map[string]any{"vip": true, "tags": []string{"a"}}}, ""},
		{"custom fields too many keys", Update{CustomFields: big}, "custom_fields"},
		{"custom fields too large", Update{CustomFields: huge}, "custom_fields"},
		{"custom fields empty key", Update{CustomFields: map[string]any{"": 1}}, "custom_fields"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params, err := validateUpdate(tc.in, now)
			if tc.field == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			ae, ok := apierr.As(err)
			if !ok || ae.Status != 400 || ae.Details[tc.field] == nil {
				t.Fatalf("got %v %+v, want validation error on %s", err, params, tc.field)
			}
		})
	}

	params, err := validateUpdate(Update{DisplayName: str("  Mei  "), Gender: str("Female"), SetGender: true, Phone: str(""), SetPhone: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if *params.DisplayName != "Mei" || *params.Gender != "female" || params.Phone != nil || !params.SetPhone {
		t.Errorf("normalisation: %+v", params)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 9, 12, 0, 0, 123456000, time.UTC)
	id := uuid.New()
	c, err := decodeCursor(encodeCursor(at, id))
	if err != nil || !c.createdAt.Equal(at) || c.id != id {
		t.Fatalf("round trip: %v %+v", err, c)
	}
	for _, bad := range []string{"!!!", "bm90LWEtY3Vyc29y", encodeCursor(at, id) + "x"} {
		if _, err := decodeCursor(bad); err == nil {
			t.Errorf("accepted cursor %q", bad)
		}
	}
	if c, err := decodeCursor(""); err != nil || c != nil {
		t.Error("empty cursor should be nil")
	}
	if _, _, err := parseListQuery(ListQuery{Limit: 101}); err == nil {
		t.Error("limit over 100 accepted")
	}
	if limit, _, err := parseListQuery(ListQuery{}); err != nil || limit != DefaultLimit {
		t.Errorf("default limit = %d, %v", limit, err)
	}
}
