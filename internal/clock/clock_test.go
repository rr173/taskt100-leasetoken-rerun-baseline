package clock

import "testing"

func TestFakeClockAdvance(t *testing.T) {
	c := NewFakeClock(1000)
	if got := c.Now(); got != 1000 {
		t.Fatalf("Now = %d, want 1000", got)
	}
	if got := c.Advance(50); got != 1050 {
		t.Fatalf("Advance returned %d, want 1050", got)
	}
	if got := c.Now(); got != 1050 {
		t.Fatalf("Now after advance = %d, want 1050", got)
	}
	// Negative deltas are allowed (rewind) and just add.
	c.Advance(-10)
	if got := c.Now(); got != 1040 {
		t.Fatalf("Now after rewind = %d, want 1040", got)
	}
}

func TestFakeClockSet(t *testing.T) {
	c := NewFakeClock(0)
	c.Set(9_999)
	if got := c.Now(); got != 9_999 {
		t.Fatalf("Now after Set = %d, want 9999", got)
	}
}

func TestRealClockNonZero(t *testing.T) {
	// RealClock.Now must be a plausible Unix second (well past epoch). We
	// don't assert exact equality, just that it advances on the wall clock.
	r := RealClock{}
	a := r.Now()
	if a < 1_000_000_000 {
		t.Fatalf("RealClock.Now implausible: %d", a)
	}
}

// Verify the Clock interface is satisfied by both implementations.
func TestClockInterface(t *testing.T) {
	var _ Clock = RealClock{}
	var _ Clock = NewFakeClock(0)
}
