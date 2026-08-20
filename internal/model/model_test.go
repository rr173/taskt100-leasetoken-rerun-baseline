package model

import "testing"

func TestIsTerminal(t *testing.T) {
	cases := map[string]bool{
		StatusActive:   false,
		StatusReleased: true,
		StatusExpired:  true,
		"":             false,
		"bogus":        false,
	}
	for status, want := range cases {
		if got := IsTerminal(status); got != want {
			t.Errorf("IsTerminal(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestLeaseRemaining(t *testing.T) {
	l := Lease{ExpiresAt: 200}
	if got := l.Remaining(150); got != 50 {
		t.Errorf("remaining = %d, want 50", got)
	}
	// Past expiry returns a negative value, not zero.
	if got := l.Remaining(250); got != -50 {
		t.Errorf("past-expiry remaining = %d, want -50", got)
	}
}

func TestStatusConstants(t *testing.T) {
	if StatusActive != "active" || StatusReleased != "released" || StatusExpired != "expired" {
		t.Fatal("status string constants changed; downstream SQL depends on them")
	}
}

func TestAuditConstants(t *testing.T) {
	// Each action string must be non-empty and unique; the audit table's
	// action index relies on stable values.
	seen := map[string]bool{}
	for _, a := range []string{
		AuditAcquire, AuditRenew, AuditRelease, AuditHeartbeat, AuditSweep,
		AuditTransfer, AuditForceRelease, AuditTTLUpdate, AuditLock, AuditUnlock, AuditRecover,
	} {
		if a == "" {
			t.Fatal("empty audit action constant")
		}
		if seen[a] {
			t.Fatalf("duplicate audit action %q", a)
		}
		seen[a] = true
	}
}
