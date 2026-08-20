package lease

import "testing"

func TestLeaseHealthOnlyAllowsRenewInsideWindow(t *testing.T) {
	m, clk := newManager(t)
	acquired, err := m.Acquire("health-resource", "holder-a", 10)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(2)
	health, err := m.LeaseHealth(acquired.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	if health.CanRenew {
		t.Fatalf("renew window must remain closed with 8 seconds left: %+v", health)
	}
	clk.Advance(4)
	health, err = m.LeaseHealth(acquired.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	if !health.CanRenew {
		t.Fatalf("renew window must be open with 4 seconds left: %+v", health)
	}
}
