package main

import (
	"context"

	"github.com/amangeldievkuu/miniloyal/internal/line"
	"github.com/amangeldievkuu/miniloyal/internal/lineauth"
)

// lineVerifier adapts line.Verifier to lineauth.IDTokenVerifier. The two
// packages declare the same input and claims shapes so neither depends on
// the other; the conversions below are field-for-field.
type lineVerifier struct{ v *line.Verifier }

func (a lineVerifier) Verify(ctx context.Context, token string, in lineauth.VerifyInput) (lineauth.Claims, error) {
	claims, err := a.v.Verify(ctx, token, line.VerifyInput(in))
	if err != nil {
		return lineauth.Claims{}, err
	}
	return lineauth.Claims(claims), nil
}
