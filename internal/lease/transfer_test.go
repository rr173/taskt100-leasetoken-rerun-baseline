package lease

import (
	"errors"
	"path/filepath"
	"testing"

	"task100-leasetoken/internal/clock"
	"task100-leasetoken/internal/model"
	"task100-leasetoken/internal/store"
)

// newRecoverManager builds a manager over a fresh DB in dir, for the
// transfer/ttl/lock/force-release tests that want a clean slate.
func newRecoverManager(t *testing.T, name string) (*Manager, *clock.FakeClock) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	clk := clock.NewFakeClock(5_000_000)
	return New(st, clk), clk
}

func TestTransferPreservesTTLWindow(t *testing.T) {
	m, clk := newRecoverManager(t, "transfer.db")
	a, err := m.Acquire("r", "alice", 100)
	if err != nil {
		t.Fatal(err)
	}
	// Advance 40s; remaining = 60.
	clk.Advance(40)
	tr, err := m.Transfer(a.LeaseID, a.FencingToken, "bob")
	if err != nil {
		t.Fatal(err)
	}
	// The new lease must keep the original expiry (no free TTL from transfer).
	newLease, err := m.GetLease(tr.NewLeaseID)
	if err != nil {
		t.Fatal(err)
	}
	if newLease.ExpiresAt != a.ExpiresAt {
		t.Fatalf("transfer changed expiry: got %d want %d", newLease.ExpiresAt, a.ExpiresAt)
	}
	if newLease.Holder != "bob" {
		t.Fatalf("holder = %q, want bob", newLease.Holder)
	}
}

func TestTransferWrongTokenRejected(t *testing.T) {
	m, _ := newRecoverManager(t, "transfer-wrong.db")
	a, err := m.Acquire("r", "alice", 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Transfer(a.LeaseID, a.FencingToken+5, "bob"); !errors.Is(err, ErrFencingMismatch) {
		t.Fatalf("expected ErrFencingMismatch, got %v", err)
	}
}

func TestTransferExpiredRejected(t *testing.T) {
	m, clk := newRecoverManager(t, "transfer-expired.db")
	a, err := m.Acquire("r", "alice", 50)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(60)
	if _, err := m.Transfer(a.LeaseID, a.FencingToken, "bob"); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("expected ErrLeaseExpired, got %v", err)
	}
}

func TestTTLUpdateShortenThenExpire(t *testing.T) {
	m, clk := newRecoverManager(t, "ttl.db")
	a, err := m.Acquire("r", "h1", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateTTL(a.LeaseID, a.FencingToken, 10); err != nil {
		t.Fatal(err)
	}
	// Now within 10s the lease is expired; renew should refuse.
	clk.Advance(11)
	if _, err := m.Renew(a.LeaseID, a.FencingToken, 100); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("expected ErrLeaseExpired after shorten, got %v", err)
	}
}

func TestTTLUpdateWrongTokenRejected(t *testing.T) {
	m, _ := newRecoverManager(t, "ttl-wrong.db")
	a, err := m.Acquire("r", "h1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateTTL(a.LeaseID, a.FencingToken+1, 50); !errors.Is(err, ErrFencingMismatch) {
		t.Fatalf("expected ErrFencingMismatch, got %v", err)
	}
}

func TestForceReleaseOnTerminalIsNoOp(t *testing.T) {
	m, _ := newRecoverManager(t, "force-terminal.db")
	a, err := m.Acquire("r", "h1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ForceRelease(a.LeaseID, "admin"); err != nil {
		t.Fatal(err)
	}
	// Second force-release on the already-released lease succeeds idempotently.
	if err := m.ForceRelease(a.LeaseID, "admin"); err != nil {
		t.Fatalf("idempotent force-release failed: %v", err)
	}
}

func TestLockUnlockIdempotent(t *testing.T) {
	m, _ := newRecoverManager(t, "lock.db")
	if err := m.LockResource("r", "admin"); err != nil {
		t.Fatal(err)
	}
	// Locking again is a no-op success.
	if err := m.LockResource("r", "admin"); err != nil {
		t.Fatalf("double lock failed: %v", err)
	}
	// Unlocking twice.
	if err := m.UnlockResource("r", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := m.UnlockResource("r", "admin"); err != nil {
		t.Fatalf("double unlock failed: %v", err)
	}
}

func TestListLeasesAndResources(t *testing.T) {
	m, _ := newRecoverManager(t, "list.db")
	_, _ = m.Acquire("r1", "h1", 100)
	_, _ = m.Acquire("r2", "h2", 100)
	leases, err := m.ListLeases(model.ListLeasesFilter{Limit: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 2 {
		t.Fatalf("expected 2 leases, got %d", len(leases))
	}
	resources, err := m.ListResources()
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(resources))
	}
}

func TestLeasesByHolder(t *testing.T) {
	m, _ := newRecoverManager(t, "holder.db")
	_, _ = m.Acquire("r1", "alice", 100)
	_, _ = m.Acquire("r2", "alice", 100)
	_, _ = m.Acquire("r3", "bob", 100)
	out, err := m.LeasesByHolder("alice", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 leases for alice, got %d", len(out))
	}
}
