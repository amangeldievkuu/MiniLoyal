package clock

import (
	"testing"
	"time"
)

func TestFixed(t *testing.T) {
	start := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	c := At(start)
	if !c.Now().Equal(start) {
		t.Fatal("At did not stop the clock")
	}
	c.Advance(90 * time.Second)
	if got := c.Now().Sub(start); got != 90*time.Second {
		t.Errorf("Advance moved by %s", got)
	}
	c.Set(start)
	if !c.Now().Equal(start) {
		t.Error("Set did not move the clock")
	}
}

func TestRealIsClose(t *testing.T) {
	if d := time.Since(Real{}.Now()); d < 0 || d > time.Second {
		t.Errorf("Real.Now is off by %s", d)
	}
}
