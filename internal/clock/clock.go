// Package clock provides an injectable time source so the lease manager can be
// tested deterministically without real-time sleeps. Production code uses
// RealClock; tests and the smoke test use FakeClock whose Now is advanced
// explicitly.
package clock

import "time"

// Clock abstracts reading the current Unix-second timestamp.
type Clock interface {
	// Now returns the current time as Unix seconds.
	Now() int64
}

// RealClock returns the wall-clock time via time.Now().Unix().
type RealClock struct{}

// Now implements Clock.
func (RealClock) Now() int64 { return time.Now().Unix() }

// FakeClock holds a mutable current time used by tests. Advance updates Now.
type FakeClock struct {
	current int64
}

// NewFakeClock returns a FakeClock anchored at the given Unix second.
func NewFakeClock(at int64) *FakeClock { return &FakeClock{current: at} }

// Now implements Clock.
func (c *FakeClock) Now() int64 { return c.current }

// Advance moves the clock forward by delta seconds and returns the new value.
func (c *FakeClock) Advance(delta int64) int64 {
	c.current += delta
	return c.current
}

// Set replaces the clock's current time. Useful for absolute jumps.
func (c *FakeClock) Set(at int64) { c.current = at }
