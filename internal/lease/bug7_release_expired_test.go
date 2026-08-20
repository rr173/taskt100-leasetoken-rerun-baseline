package lease

import (
	"errors"
	"testing"
)

func TestReleaseRejectsExpiredLease(t *testing.T) {
	m, clk := newManager(t)
	acquired, err := m.Acquire("release-expired", "holder-a", 30)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(31)
	if err := m.Release(acquired.LeaseID, acquired.FencingToken); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("release error = %v, want %v", err, ErrLeaseExpired)
	}
	lease, err := m.GetLease(acquired.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Status != "active" {
		t.Fatalf("expired lease status changed to %q before sweep", lease.Status)
	}
}
