package lease

import (
	"errors"
	"path/filepath"
	"testing"

	"task100-leasetoken/internal/clock"
	"task100-leasetoken/internal/model"
	"task100-leasetoken/internal/store"
)

func newManager(t *testing.T) (*Manager, *clock.FakeClock) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	clk := clock.NewFakeClock(1_000_000)
	return New(st, clk), clk
}

func TestAcquireFencingMonotonic(t *testing.T) {
	m, _ := newManager(t)
	a1, err := m.Acquire("r", "h1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if a1.FencingToken != 1 {
		t.Fatalf("first token = %d, want 1", a1.FencingToken)
	}
	if err := m.Release(a1.LeaseID, a1.FencingToken); err != nil {
		t.Fatal(err)
	}
	a2, err := m.Acquire("r", "h1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if a2.FencingToken <= a1.FencingToken {
		t.Fatalf("token not monotonic: %d -> %d", a1.FencingToken, a2.FencingToken)
	}
}

func TestAcquireConflict(t *testing.T) {
	m, _ := newManager(t)
	if _, err := m.Acquire("r", "h1", 100); err != nil {
		t.Fatal(err)
	}
	_, err := m.Acquire("r", "h2", 100)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestFencingMismatchRejected(t *testing.T) {
	m, _ := newManager(t)
	a, err := m.Acquire("r", "h1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Release(a.LeaseID, a.FencingToken+1); !errors.Is(err, ErrFencingMismatch) {
		t.Fatalf("expected ErrFencingMismatch, got %v", err)
	}
	if _, err := m.Renew(a.LeaseID, a.FencingToken+1, 100); !errors.Is(err, ErrFencingMismatch) {
		t.Fatalf("expected ErrFencingMismatch on renew, got %v", err)
	}
}

func TestRenewWindow(t *testing.T) {
	m, clk := newManager(t)
	a, err := m.Acquire("r", "h1", 100)
	if err != nil {
		t.Fatal(err)
	}
	// remaining=100 > 50: too early.
	if _, err := m.Renew(a.LeaseID, a.FencingToken, 100); !errors.Is(err, ErrRenewTooEarly) {
		t.Fatalf("expected ErrRenewTooEarly, got %v", err)
	}
	// remaining=50 (exactly half): allowed (<=).
	clk.Advance(50)
	if _, err := m.Renew(a.LeaseID, a.FencingToken, 100); err != nil {
		t.Fatalf("renew at half failed: %v", err)
	}
}

func TestRenewExpired(t *testing.T) {
	m, clk := newManager(t)
	a, err := m.Acquire("r", "h1", 100)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(101)
	if _, err := m.Renew(a.LeaseID, a.FencingToken, 100); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("expected ErrLeaseExpired, got %v", err)
	}
}

func TestIdempotentRelease(t *testing.T) {
	m, _ := newManager(t)
	a, err := m.Acquire("r", "h1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Release(a.LeaseID, a.FencingToken); err != nil {
		t.Fatal(err)
	}
	// Second release with matching token is a no-op success.
	if err := m.Release(a.LeaseID, a.FencingToken); err != nil {
		t.Fatalf("idempotent release failed: %v", err)
	}
	// Mismatched token on terminal lease is rejected.
	if err := m.Release(a.LeaseID, a.FencingToken+1); !errors.Is(err, ErrFencingMismatch) {
		t.Fatalf("expected ErrFencingMismatch, got %v", err)
	}
}

func TestSweepRetiresAndBumpsToken(t *testing.T) {
	m, clk := newManager(t)
	a, err := m.Acquire("r", "h1", 50)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(60)
	sw, err := m.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if sw.Expired != 1 {
		t.Fatalf("expired = %d, want 1", sw.Expired)
	}
	l, _ := m.GetLease(a.LeaseID)
	if l.Status != model.StatusExpired {
		t.Fatalf("status = %s, want expired", l.Status)
	}
	a2, err := m.Acquire("r", "h2", 50)
	if err != nil {
		t.Fatal(err)
	}
	if a2.FencingToken <= a.FencingToken {
		t.Fatalf("post-sweep token not higher: %d -> %d", a.FencingToken, a2.FencingToken)
	}
}

func TestTransferInvalidatesOldHolder(t *testing.T) {
	m, _ := newManager(t)
	a, err := m.Acquire("r", "alice", 100)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := m.Transfer(a.LeaseID, a.FencingToken, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if tr.FencingToken <= a.FencingToken {
		t.Fatalf("transfer token not bumped")
	}
	// Alice's old token can't renew the old lease.
	if _, err := m.Renew(a.LeaseID, a.FencingToken, 100); !errors.Is(err, ErrTerminal) {
		t.Fatalf("expected terminal on old lease, got %v", err)
	}
	// Bob can act on the new lease.
	if _, err := m.Heartbeat(tr.NewLeaseID, tr.FencingToken); err != nil {
		t.Fatalf("bob heartbeat failed: %v", err)
	}
}

func TestTTLUpdateRecomputesExpiry(t *testing.T) {
	m, clk := newManager(t)
	a, err := m.Acquire("r", "h1", 100)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := m.UpdateTTL(a.LeaseID, a.FencingToken, 10)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ExpiresAt != clk.Now()+10 {
		t.Fatalf("expiry = %d, want %d", resp.ExpiresAt, clk.Now()+10)
	}
	l, _ := m.GetLease(a.LeaseID)
	if l.TTLSeconds != 10 {
		t.Fatalf("ttl_seconds = %d, want 10", l.TTLSeconds)
	}
}

func TestResourceLockBlocksAcquire(t *testing.T) {
	m, _ := newManager(t)
	if err := m.LockResource("r", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Acquire("r", "h1", 100); !errors.Is(err, ErrResourceLocked) {
		t.Fatalf("expected ErrResourceLocked, got %v", err)
	}
	if err := m.UnlockResource("r", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Acquire("r", "h1", 100); err != nil {
		t.Fatalf("acquire after unlock failed: %v", err)
	}
}

func TestForceRelease(t *testing.T) {
	m, _ := newManager(t)
	a, err := m.Acquire("r", "h1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ForceRelease(a.LeaseID, "admin"); err != nil {
		t.Fatal(err)
	}
	l, _ := m.GetLease(a.LeaseID)
	if l.Status != model.StatusReleased {
		t.Fatalf("status = %s, want released", l.Status)
	}
	// The stale holder's token no longer matches anything actionable.
	if err := m.Release(a.LeaseID, a.FencingToken); err != nil {
		t.Fatalf("idempotent release after force-release failed: %v", err)
	}
}

func TestRecoverRetiresStaleActive(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "recover.db")

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFakeClock(1_000_000)
	mgr := New(st, clk)
	a, err := mgr.Acquire("r", "h1", 100)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(101)
	st.Close()

	st2, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	clk2 := clock.NewFakeClock(1_000_200)
	mgr2 := New(st2, clk2)
	n, err := mgr2.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("recover expired = %d, want 1", n)
	}
	tok, err := mgr2.GetResourceFencing("r")
	if err != nil {
		t.Fatal(err)
	}
	if tok <= a.FencingToken {
		t.Fatalf("recover did not bump token: %d -> %d", a.FencingToken, tok)
	}
}

func TestAuditTrail(t *testing.T) {
	m, _ := newManager(t)
	a, err := m.Acquire("r", "h1", 100)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = m.Heartbeat(a.LeaseID, a.FencingToken)
	_ = m.Release(a.LeaseID, a.FencingToken)
	entries, err := m.ListAudit(model.ListAuditFilter{LeaseID: a.LeaseID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 3 {
		t.Fatalf("expected >=3 audit entries, got %d", len(entries))
	}
}

func TestStats(t *testing.T) {
	m, _ := newManager(t)
	_, _ = m.Acquire("r1", "h1", 100)
	_, _ = m.Acquire("r2", "h2", 100)
	s, err := m.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if s.Resources < 2 || s.ActiveLeases < 2 || s.Holders < 2 {
		t.Fatalf("stats too low: %+v", s)
	}
}

func TestStaleAcquireEvictsExpired(t *testing.T) {
	m, clk := newManager(t)
	a, err := m.Acquire("r", "h1", 50)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(60)
	// No sweep; acquire should evict the stale active lease in place.
	a2, err := m.Acquire("r", "h2", 50)
	if err != nil {
		t.Fatal(err)
	}
	if a2.FencingToken <= a.FencingToken {
		t.Fatalf("stale-evict token not higher: %d -> %d", a.FencingToken, a2.FencingToken)
	}
	l, _ := m.GetLease(a.LeaseID)
	if l.Status != model.StatusExpired {
		t.Fatalf("old lease status = %s, want expired", l.Status)
	}
}
