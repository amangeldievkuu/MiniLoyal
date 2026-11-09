package auth

import (
	"testing"
	"time"

	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
)

func TestLimiterAllowsBurstThenDeniesThenRecovers(t *testing.T) {
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	l := NewLimiter(clk, 6, 3) // one token every 10 s, burst 3

	for i := 0; i < 3; i++ {
		if wait, ok := l.Allow("ip"); !ok {
			t.Fatalf("call %d denied, wait %s", i, wait)
		}
	}
	wait, ok := l.Allow("ip")
	if ok {
		t.Fatal("fourth call should be denied")
	}
	if wait <= 0 || wait > 10*time.Second {
		t.Errorf("retry after = %s, want within 10s", wait)
	}
	// Another key is independent.
	if _, ok := l.Allow("other"); !ok {
		t.Error("other key denied")
	}
	clk.Advance(10 * time.Second)
	if _, ok := l.Allow("ip"); !ok {
		t.Error("should recover one token after 10s")
	}
}

func TestLimiterPrunesIdleKeys(t *testing.T) {
	clk := clock.At(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	l := NewLimiter(clk, 60, 10)
	l.Allow("a")
	clk.Advance(11 * time.Minute)
	l.Allow("b")
	if l.Len() != 1 {
		t.Errorf("tracked keys = %d, want 1 after pruning", l.Len())
	}
}
