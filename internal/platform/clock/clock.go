// Package clock gives services an injectable source of time so tests can be
// deterministic.
package clock

import "time"

// Clock returns the current time.
type Clock interface {
	Now() time.Time
}

// Real reads the system clock.
type Real struct{}

// Now implements Clock.
func (Real) Now() time.Time { return time.Now() }

// Fixed always returns the same instant until Set or Advance is called. It is
// not safe for concurrent use; tests drive it from one goroutine.
type Fixed struct{ t time.Time }

// At returns a Fixed clock stopped at t.
func At(t time.Time) *Fixed { return &Fixed{t: t} }

// Now implements Clock.
func (f *Fixed) Now() time.Time { return f.t }

// Set moves the clock to t.
func (f *Fixed) Set(t time.Time) { f.t = t }

// Advance moves the clock forward by d.
func (f *Fixed) Advance(d time.Duration) { f.t = f.t.Add(d) }
