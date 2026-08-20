package lease

import "testing"

func TestStatsExcludesExpiredActiveLeaseFromHolders(t *testing.T) {
	m, clk := newManager(t)
	if _, err := m.Acquire("stats-resource", "holder-a", 10); err != nil {
		t.Fatal(err)
	}
	clk.Advance(11)
	stats, err := m.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Holders != 0 {
		t.Fatalf("expired active lease must not count as a holder, got %d", stats.Holders)
	}
}
