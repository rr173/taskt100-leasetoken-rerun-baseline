package lease

import (
	"errors"
	"testing"
)

func TestAcquireAdviceReportsActiveLeaseConflict(t *testing.T) {
	m, _ := newManager(t)
	if _, err := m.Acquire("advice-resource", "holder-a", 30); err != nil {
		t.Fatal(err)
	}
	advice, err := m.AcquireAdvice("advice-resource")
	if err != nil {
		t.Fatal(err)
	}
	if advice.Allowed {
		t.Fatalf("advice must reject an actively held resource: %+v", advice)
	}
	if advice.Reason != ErrConflict.Error() {
		t.Fatalf("reason = %q, want %q", advice.Reason, ErrConflict.Error())
	}
}

// An unknown resource is acquirable: advice says it will be created.
func TestAcquireAdviceAbsentResourceAllowed(t *testing.T) {
	m, _ := newManager(t)
	advice, err := m.AcquireAdvice("advice-absent")
	if err != nil {
		t.Fatal(err)
	}
	if !advice.Allowed {
		t.Fatalf("absent resource must be allowed: %+v", advice)
	}
	if advice.Reason != "resource will be created" {
		t.Fatalf("reason = %q, want %q", advice.Reason, "resource will be created")
	}
}

// A locked resource is reported as locked, never allowed, regardless of whether
// it also carries an active lease.
func TestAcquireAdviceLockedResourceRejected(t *testing.T) {
	m, _ := newManager(t)
	if err := m.LockResource("advice-locked", "admin"); err != nil {
		t.Fatal(err)
	}
	advice, err := m.AcquireAdvice("advice-locked")
	if err != nil {
		t.Fatal(err)
	}
	if advice.Allowed {
		t.Fatalf("locked resource must not be allowed: %+v", advice)
	}
	if !advice.Locked {
		t.Fatalf("locked flag must be set: %+v", advice)
	}
	if advice.Reason != ErrResourceLocked.Error() {
		t.Fatalf("reason = %q, want %q", advice.Reason, ErrResourceLocked.Error())
	}
}

// Once the active lease is logically expired (sweep lagged), advice must revert
// to allowed — Acquire would retire it in place. This is the boundary that must
// stay correct even after the conflict fix.
func TestAcquireAdviceExpiredLeaseAllowedAgain(t *testing.T) {
	m, clk := newManager(t)
	if _, err := m.Acquire("advice-expiring", "holder-a", 30); err != nil {
		t.Fatal(err)
	}
	// Conflicts while live.
	advice, err := m.AcquireAdvice("advice-expiring")
	if err != nil {
		t.Fatal(err)
	}
	if advice.Allowed {
		t.Fatalf("expected conflict while active: %+v", advice)
	}
	// Past expiry, no sweep: AcquireAdvice must not block (mirrors Acquire's
	// stale-evict path).
	clk.Advance(31)
	advice, err = m.AcquireAdvice("advice-expiring")
	if err != nil {
		t.Fatal(err)
	}
	if !advice.Allowed {
		t.Fatalf("expired lease must be acquirable again: %+v", advice)
	}
	if advice.Reason != "resource is available for an acquisition attempt" {
		t.Fatalf("reason = %q", advice.Reason)
	}
	// And Acquire actually succeeds after expiry (stale-evict), not conflict.
	if _, err := m.Acquire("advice-expiring", "holder-b", 30); errors.Is(err, ErrConflict) {
		t.Fatalf("acquire after expiry returned conflict: %v", err)
	}
}
