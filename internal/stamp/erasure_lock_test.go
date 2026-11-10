package stamp

import (
	"context"
	"testing"
	"time"

	"github.com/amangeldievkuu/miniloyal/internal/coupon"
)

// Phase 4 re-review R5: an erased member's coupons cannot be voided, so no
// new void reason (personal text) is stored after the erasure blanked the
// old ones. A replay of a void made before the erasure is refused too,
// because its stored outcome holds the reason.
func TestVoidRefusesErasedMembers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tmpl := e.template(t, coupon.TemplateInput{})
	m := e.member(t)
	before, err := e.coupons.Issue(ctx, e.owner, m, tmpl.ID, idem())
	if err != nil {
		t.Fatal(err)
	}
	open, err := e.coupons.Issue(ctx, e.owner, m, tmpl.ID, idem())
	if err != nil {
		t.Fatal(err)
	}
	voidKey := idem()
	if _, err := e.coupons.Void(ctx, e.owner, before.ID, voidKey, "Ms Lin asked"); err != nil {
		t.Fatal(err)
	}
	e.d.Exec(t, "UPDATE members SET anonymized_at = now() WHERE id = $1", m)

	if _, err := e.coupons.Void(ctx, e.owner, open.ID, idem(), "after erasure, Ms Lin"); code(err) != "not_found" {
		t.Errorf("void of an erased member's coupon: %v, want not_found", err)
	}
	if _, err := e.coupons.Void(ctx, e.owner, before.ID, voidKey, "Ms Lin asked"); code(err) != "not_found" {
		t.Errorf("replay after erasure: %v, want not_found", err)
	}
	if n := e.count(t, "SELECT count(*) FROM coupon_instances WHERE member_id = $1 AND void_reason LIKE '%after erasure%'", m); n != 0 {
		t.Errorf("%d void reasons written after the erasure", n)
	}
}

// Phase 4 re-review R5: stamp adjustments and coupon voids read the member
// FOR SHARE, so they wait for an erasure in progress (which holds the
// member row) and then see the member is gone, instead of writing a note
// the erasure has already passed over.
func TestNoteWritesWaitForAnErasureInProgress(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tmpl := e.template(t, coupon.TemplateInput{})
	c := e.card(t, 5, 3, nil)

	for _, name := range []string{"stamp adjust", "coupon void"} {
		t.Run(name, func(t *testing.T) {
			m := e.member(t)
			inst, err := e.coupons.Issue(ctx, e.owner, m, tmpl.ID, idem())
			if err != nil {
				t.Fatal(err)
			}
			write := func() error {
				if name == "stamp adjust" {
					_, err := e.svc.Adjust(ctx, e.owner, m, idem(), c.ID, 1, "phoned Ms Lin 0912")
					return err
				}
				_, err := e.coupons.Void(ctx, e.owner, inst.ID, idem(), "Ms Lin 0912 complained")
				return err
			}

			// The erasure's first step: anonymize the member, holding the row.
			erasure, err := e.d.App.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = erasure.Rollback(ctx) }()
			if _, err := erasure.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", e.tenant.String()); err != nil {
				t.Fatal(err)
			}
			if _, err := erasure.Exec(ctx, "UPDATE members SET anonymized_at = now() WHERE id = $1", m); err != nil {
				t.Fatal(err)
			}

			done := make(chan error, 1)
			go func() { done <- write() }()
			select {
			case err := <-done:
				t.Fatalf("%s did not wait for the erasure: %v", name, err)
			case <-time.After(300 * time.Millisecond):
			}
			if err := erasure.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if code(err) != "not_found" {
					t.Errorf("%s after the erasure committed: %v, want not_found", name, err)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("%s still blocked after the erasure committed", name)
			}
			if n := e.count(t, "SELECT count(*) FROM stamp_events WHERE member_id = $1", m); n != 0 {
				t.Errorf("%d stamp events written for the erased member", n)
			}
			if n := e.count(t, "SELECT count(*) FROM coupon_instances WHERE member_id = $1 AND status = 'voided'", m); n != 0 {
				t.Errorf("%d coupons voided for the erased member", n)
			}
		})
	}
}
