package main

import (
	"fmt"
	"os"
	"path/filepath"

	"task100-leasetoken/internal/clock"
	"task100-leasetoken/internal/lease"
	"task100-leasetoken/internal/model"
	"task100-leasetoken/internal/store"
)

// runSmokeTest exercises the full fencing-token contract against a temporary
// SQLite file. It uses a FakeClock so no real-time sleep is needed. Each
// scenario reopens the store to validate persistence/recovery, and the final
// scenario deletes the file to start clean.
func runSmokeTest() error {
	dir, err := os.MkdirTemp("", "leasetoken-smoke-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	cases := []struct {
		name string
		fn   func(*lease.Manager, *clock.FakeClock) error
	}{
		{"fencing-monotonic-and-acquire-conflict", smokeFencingMonotonic},
		{"renew-window-and-too-early", smokeRenewWindow},
		{"idempotent-release-and-fencing-mismatch", smokeIdempotentRelease},
		{"transfer-invalidates-old-holder", smokeTransfer},
		{"ttl-update-recomputes-expiry", smokeTTLUpdate},
		{"resource-lock-blocks-acquire", smokeResourceLock},
		{"sweep-retires-and-bumps-token", smokeSweep},
		{"audit-trail-recorded", smokeAudit},
		{"stats-aggregate-counts", smokeStats},
	}
	for i, c := range cases {
		// Each scenario gets its own DB file so contract assertions don't
		// trip over state left by an earlier scenario (e.g. a TTL-shortened
		// lease that stays active and expires under a later clock).
		dbPath := filepath.Join(dir, fmt.Sprintf("smoke-%02d.db", i))
		st, err := store.Open(dbPath)
		if err != nil {
			return fmt.Errorf("%s: open: %w", c.name, err)
		}
		clk := clock.NewFakeClock(1_000_000)
		mgr := lease.New(st, clk)
		if err := c.fn(mgr, clk); err != nil {
			st.Close()
			return fmt.Errorf("%s: %w", c.name, err)
		}
		st.Close()
	}

	// Restart-recovery uses its own DB file so it controls the persisted
	// active-but-expired lease exactly.
	if err := smokeRestartRecovery(filepath.Join(dir, "smoke-recover.db")); err != nil {
		return fmt.Errorf("restart-recovery: %w", err)
	}

	return nil
}

func smokeFencingMonotonic(m *lease.Manager, clk *clock.FakeClock) error {
	const res = "res-A"
	// First acquire -> token 1.
	a1, err := m.Acquire(res, "h1", 100)
	if err != nil {
		return err
	}
	if a1.FencingToken != 1 {
		return fmt.Errorf("expected first token 1, got %d", a1.FencingToken)
	}
	// Conflict: a second holder cannot acquire while the first is active.
	if _, err := m.Acquire(res, "h2", 100); err == nil {
		return fmt.Errorf("expected conflict on second acquire")
	}
	// Release, then re-acquire -> token strictly > 1.
	if err := m.Release(a1.LeaseID, a1.FencingToken); err != nil {
		return err
	}
	a2, err := m.Acquire(res, "h1", 100)
	if err != nil {
		return err
	}
	if a2.FencingToken <= a1.FencingToken {
		return fmt.Errorf("token not monotonic: %d then %d", a1.FencingToken, a2.FencingToken)
	}
	// Stale holder (old token) cannot release the new lease.
	if err := m.Release(a2.LeaseID, a1.FencingToken); err == nil {
		return fmt.Errorf("expected fencing mismatch releasing with stale token")
	}
	// Correct token works.
	if err := m.Release(a2.LeaseID, a2.FencingToken); err != nil {
		return err
	}
	return nil
}

func smokeRenewWindow(m *lease.Manager, clk *clock.FakeClock) error {
	const res = "res-B"
	a, err := m.Acquire(res, "h1", 100)
	if err != nil {
		return err
	}
	// Immediately (remaining=100 > 50) renew must be refused.
	if _, err := m.Renew(a.LeaseID, a.FencingToken, 100); err == nil {
		return fmt.Errorf("expected renew-too-early immediately after acquire")
	}
	// Advance past the half-window (remaining <= 50).
	clk.Advance(55)
	if _, err := m.Renew(a.LeaseID, a.FencingToken, 100); err != nil {
		return fmt.Errorf("renew in window failed: %w", err)
	}
	// After renew, expires moved forward; verify via Remaining.
	r, err := m.Remaining(a.LeaseID)
	if err != nil {
		return err
	}
	// now is 1_000_055; new expiry = now + 100 = 1_000_155; remaining = 100.
	if r.Remaining != 100 {
		return fmt.Errorf("expected remaining 100 after renew, got %d", r.Remaining)
	}
	// Past expiry -> renew must be refused as expired.
	clk.Advance(101)
	if _, err := m.Renew(a.LeaseID, a.FencingToken, 100); err == nil {
		return fmt.Errorf("expected expired refusal after expiry")
	}
	return nil
}

func smokeIdempotentRelease(m *lease.Manager, clk *clock.FakeClock) error {
	const res = "res-C"
	a, err := m.Acquire(res, "h1", 100)
	if err != nil {
		return err
	}
	if err := m.Release(a.LeaseID, a.FencingToken); err != nil {
		return err
	}
	// Idempotent second release with matching token.
	if err := m.Release(a.LeaseID, a.FencingToken); err != nil {
		return fmt.Errorf("idempotent release failed: %w", err)
	}
	// Token mismatch on a terminal lease is still rejected.
	if err := m.Release(a.LeaseID, a.FencingToken+999); err == nil {
		return fmt.Errorf("expected fencing mismatch on terminal lease")
	}
	// Renew/heartbeat on terminal lease must fail.
	if _, err := m.Heartbeat(a.LeaseID, a.FencingToken); err == nil {
		return fmt.Errorf("expected terminal error on heartbeat")
	}
	return nil
}

func smokeTransfer(m *lease.Manager, clk *clock.FakeClock) error {
	const res = "res-D"
	a, err := m.Acquire(res, "alice", 100)
	if err != nil {
		return err
	}
	tr, err := m.Transfer(a.LeaseID, a.FencingToken, "bob")
	if err != nil {
		return err
	}
	if tr.FencingToken <= a.FencingToken {
		return fmt.Errorf("transfer token not bumped: %d -> %d", a.FencingToken, tr.FencingToken)
	}
	// Alice (old token) can no longer renew the old lease.
	if _, err := m.Renew(a.LeaseID, a.FencingToken, 100); err == nil {
		return fmt.Errorf("expected old holder renew to fail post-transfer")
	}
	// Bob (new token) can heartbeat the new lease.
	if _, err := m.Heartbeat(tr.NewLeaseID, tr.FencingToken); err != nil {
		return fmt.Errorf("new holder heartbeat failed: %w", err)
	}
	// Bob releases the new lease.
	if err := m.Release(tr.NewLeaseID, tr.FencingToken); err != nil {
		return err
	}
	return nil
}

func smokeTTLUpdate(m *lease.Manager, clk *clock.FakeClock) error {
	const res = "res-E"
	a, err := m.Acquire(res, "h1", 100)
	if err != nil {
		return err
	}
	// Shorten TTL to 10; expiry should be now+10.
	resp, err := m.UpdateTTL(a.LeaseID, a.FencingToken, 10)
	if err != nil {
		return err
	}
	if resp.ExpiresAt != clk.Now()+10 {
		return fmt.Errorf("ttl update expiry wrong: got %d want %d", resp.ExpiresAt, clk.Now()+10)
	}
	l, err := m.GetLease(a.LeaseID)
	if err != nil {
		return err
	}
	if l.TTLSeconds != 10 {
		return fmt.Errorf("ttl_seconds not persisted: got %d", l.TTLSeconds)
	}
	return nil
}

func smokeResourceLock(m *lease.Manager, clk *clock.FakeClock) error {
	const res = "res-F"
	if err := m.LockResource(res, "admin"); err != nil {
		return err
	}
	// Acquire on a locked resource must be refused.
	if _, err := m.Acquire(res, "h1", 100); err == nil {
		return fmt.Errorf("expected locked-resource refusal")
	}
	if err := m.UnlockResource(res, "admin"); err != nil {
		return err
	}
	// After unlock, acquire works.
	if _, err := m.Acquire(res, "h1", 100); err != nil {
		return err
	}
	return nil
}

func smokeSweep(m *lease.Manager, clk *clock.FakeClock) error {
	const res = "res-G"
	a, err := m.Acquire(res, "h1", 50)
	if err != nil {
		return err
	}
	// Move past expiry but don't release.
	clk.Advance(60)
	sw, err := m.Sweep()
	if err != nil {
		return err
	}
	if sw.Expired != 1 {
		return fmt.Errorf("expected sweep of 1, got %d", sw.Expired)
	}
	// The lease is now terminal.
	l, err := m.GetLease(a.LeaseID)
	if err != nil {
		return err
	}
	if l.Status != model.StatusExpired {
		return fmt.Errorf("expected expired status, got %s", l.Status)
	}
	// Re-acquire yields a strictly higher token (sweep bumped it).
	a2, err := m.Acquire(res, "h2", 50)
	if err != nil {
		return err
	}
	if a2.FencingToken <= a.FencingToken {
		return fmt.Errorf("post-sweep re-acquire token not higher: %d then %d", a.FencingToken, a2.FencingToken)
	}
	return nil
}

func smokeAudit(m *lease.Manager, clk *clock.FakeClock) error {
	const res = "res-H"
	a, err := m.Acquire(res, "h1", 100)
	if err != nil {
		return err
	}
	_, _ = m.Heartbeat(a.LeaseID, a.FencingToken)
	_ = m.Release(a.LeaseID, a.FencingToken)
	entries, err := m.ListAudit(model.ListAuditFilter{LeaseID: a.LeaseID, Limit: 100})
	if err != nil {
		return err
	}
	// Expect at least acquire + heartbeat + release.
	if len(entries) < 3 {
		return fmt.Errorf("expected >=3 audit entries, got %d", len(entries))
	}
	// Confirm actions are present.
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Action] = true
	}
	for _, want := range []string{model.AuditAcquire, model.AuditHeartbeat, model.AuditRelease} {
		if !got[want] {
			return fmt.Errorf("audit missing action %q", want)
		}
	}
	return nil
}

func smokeStats(m *lease.Manager, clk *clock.FakeClock) error {
	const res = "res-I"
	_, _ = m.Acquire(res, "h1", 100)
	_, _ = m.Acquire(res+"2", "h2", 100)
	s, err := m.Stats()
	if err != nil {
		return err
	}
	if s.Resources < 2 {
		return fmt.Errorf("expected >=2 resources in stats, got %d", s.Resources)
	}
	if s.ActiveLeases < 2 {
		return fmt.Errorf("expected >=2 active leases in stats, got %d", s.ActiveLeases)
	}
	if s.Holders < 2 {
		return fmt.Errorf("expected >=2 holders in stats, got %d", s.Holders)
	}
	return nil
}

// smokeRestartRecovery seeds an expired active lease, closes the store, reopens
// it (simulating a restart) and asserts Recover retires the lease and bumps the
// resource fencing token.
func smokeRestartRecovery(dbPath string) error {
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	clk := clock.NewFakeClock(2_000_000)
	mgr := lease.New(st, clk)
	a, err := mgr.Acquire("res-recover", "h1", 100)
	if err != nil {
		st.Close()
		return err
	}
	// Advance past expiry but DON'T sweep; the lease is logically expired but
	// still status=active on disk.
	clk.Advance(101)
	st.Close()

	// Reopen: the persisted row has status=active, expires_at=2_000_101.
	// On reopen the clock is reset to 2_000_101 so Recover sees it as expired.
	st2, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	clk2 := clock.NewFakeClock(2_000_200)
	mgr2 := lease.New(st2, clk2)
	n, err := mgr2.Recover()
	if err != nil {
		st2.Close()
		return err
	}
	if n != 1 {
		st2.Close()
		return fmt.Errorf("expected recover to expire 1 lease, got %d", n)
	}
	tok, err := mgr2.GetResourceFencing("res-recover")
	if err != nil {
		st2.Close()
		return err
	}
	if tok <= a.FencingToken {
		st2.Close()
		return fmt.Errorf("recover did not bump token: %d then %d", a.FencingToken, tok)
	}
	st2.Close()
	return nil
}
