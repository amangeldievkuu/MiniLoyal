package migrate

import "testing"

func TestQuoteLiteral(t *testing.T) {
	cases := map[string]string{
		`plain`:      `'plain'`,
		`it's`:       `'it''s'`,
		`back\slash`: `E'back\\slash'`,
		`both'\`:     `E'both''\\'`,
	}
	for in, want := range cases {
		if got := quoteLiteral(in); got != want {
			t.Errorf("quoteLiteral(%q) = %s, want %s", in, got, want)
		}
	}
}
