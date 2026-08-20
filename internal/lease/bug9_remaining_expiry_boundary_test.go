package lease

import "testing"

func TestRemainingMarksDeadlineAsExpired(t *testing.T) {
	m, clk := newManager(t)
	acquired, err := m.Acquire("remaining-expiry", "holder-a", 10)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(10)
	remaining, err := m.Remaining(acquired.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	if !remaining.Expired || remaining.Remaining != 0 {
		t.Fatalf("remaining = %+v, want expired at the deadline", remaining)
	}
}
