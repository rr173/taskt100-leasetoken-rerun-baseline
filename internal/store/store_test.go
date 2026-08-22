package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"task100-leasetoken/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestOpenAppliesSchemaIdempotently(t *testing.T) {
	st := newTestStore(t)
	// Re-applying should be a no-op (IF NOT EXISTS).
	if _, err := st.db.Exec(schema); err != nil {
		t.Fatalf("re-apply schema: %v", err)
	}
	// The three tables exist.
	for _, tbl := range []string{"resources", "leases", "audit"} {
		var name string
		err := st.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&name)
		if err != nil {
			t.Fatalf("table %s missing: %v", tbl, err)
		}
	}
}

func TestInsertAndGetLease(t *testing.T) {
	st := newTestStore(t)
	tx, err := st.BeginTx()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	l := model.Lease{
		LeaseID: "l1", Resource: "r1", Holder: "h1", FencingToken: 7,
		AcquiredAt: 10, TTLSeconds: 100, ExpiresAt: 110, LastHeartbeat: 10, Status: model.StatusActive,
	}
	if err := st.InsertLease(tx, l); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, found, err := st.GetLease(tx, "l1")
	if err != nil || !found {
		t.Fatalf("get: %v found=%v", err, found)
	}
	if got.FencingToken != 7 || got.Status != model.StatusActive {
		t.Fatalf("wrong lease: %+v", got)
	}
}

func TestSetLeaseStatusGuard(t *testing.T) {
	st := newTestStore(t)
	tx, err := st.BeginTx()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	l := model.Lease{LeaseID: "l2", Resource: "r2", Holder: "h2", FencingToken: 1,
		AcquiredAt: 1, TTLSeconds: 1, ExpiresAt: 2, LastHeartbeat: 1, Status: model.StatusActive}
	st.InsertLease(tx, l)

	n, err := st.SetLeaseStatus(tx, "l2", model.StatusActive, model.StatusReleased)
	if err != nil || n != 1 {
		t.Fatalf("first transition: n=%d err=%v", n, err)
	}
	// Second transition with the old active guard must affect 0 rows.
	n, err = st.SetLeaseStatus(tx, "l2", model.StatusActive, model.StatusReleased)
	if err != nil {
		t.Fatalf("second transition err: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 rows on second transition, got %d", n)
	}
}

func TestListLeasesFilter(t *testing.T) {
	st := newTestStore(t)
	tx, err := st.BeginTx()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	seed := []model.Lease{
		{LeaseID: "a", Resource: "r", Holder: "h1", FencingToken: 1, AcquiredAt: 1, TTLSeconds: 10, ExpiresAt: 11, LastHeartbeat: 1, Status: model.StatusActive},
		{LeaseID: "b", Resource: "r", Holder: "h2", FencingToken: 2, AcquiredAt: 2, TTLSeconds: 10, ExpiresAt: 12, LastHeartbeat: 2, Status: model.StatusReleased},
		{LeaseID: "c", Resource: "r2", Holder: "h1", FencingToken: 1, AcquiredAt: 3, TTLSeconds: 10, ExpiresAt: 13, LastHeartbeat: 3, Status: model.StatusActive},
	}
	for _, l := range seed {
		st.InsertLease(tx, l)
	}
	out, err := st.ListLeases(tx, model.ListLeasesFilter{Resource: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 leases for resource r, got %d", len(out))
	}
	out, err = st.ListLeases(tx, model.ListLeasesFilter{Status: model.StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 active, got %d", len(out))
	}
	out, err = st.ListLeases(tx, model.ListLeasesFilter{Holder: "h1", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("limit not applied: got %d", len(out))
	}
}

func TestAuditRoundTrip(t *testing.T) {
	st := newTestStore(t)
	tx, err := st.BeginTx()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	e := model.AuditEntry{
		LeaseID: "l1", Resource: "r1", Action: model.AuditAcquire,
		Actor: "h1", FencingToken: 1, At: 5, Detail: "",
	}
	if err := st.InsertAudit(tx, e); err != nil {
		t.Fatalf("insert audit: %v", err)
	}
	out, err := st.ListAudit(tx, model.ListAuditFilter{LeaseID: "l1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Action != model.AuditAcquire {
		t.Fatalf("audit read-back wrong: %+v", out)
	}
}

func TestResourceLockPersistence(t *testing.T) {
	st := newTestStore(t)
	tx, err := st.BeginTx()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	if err := st.SetResourceLocked(tx, "rlock", true); err != nil {
		t.Fatalf("set locked: %v", err)
	}
	r, found, err := st.GetResource(tx, "rlock")
	if err != nil || !found {
		t.Fatalf("get: %v %v", err, found)
	}
	if !r.Locked {
		t.Fatalf("expected locked=true")
	}
}

func TestCountLeasesByStatus(t *testing.T) {
	st := newTestStore(t)
	tx, err := st.BeginTx()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	for i, s := range []string{model.StatusActive, model.StatusReleased, model.StatusExpired, model.StatusActive} {
		st.InsertLease(tx, model.Lease{
			LeaseID: lid(i), Resource: "r", Holder: "h", FencingToken: int64(i + 1),
			AcquiredAt: int64(i), TTLSeconds: 10, ExpiresAt: int64(i + 10), LastHeartbeat: int64(i), Status: s,
		})
	}
	a, rel, exp, err := st.CountLeasesByStatus(tx)
	if err != nil {
		t.Fatal(err)
	}
	if a != 2 || rel != 1 || exp != 1 {
		t.Fatalf("counts wrong: active=%d released=%d expired=%d", a, rel, exp)
	}
}

func lid(i int) string { return "lease-" + string(rune('0'+i)) }

func TestCountHolders(t *testing.T) {
	st := newTestStore(t)
	tx, err := st.BeginTx()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	now := int64(100)
	seed := []model.Lease{
		// Two distinct live holders.
		{LeaseID: "live1", Resource: "r1", Holder: "h1", FencingToken: 1, AcquiredAt: 1, TTLSeconds: 90, ExpiresAt: now + 10, LastHeartbeat: 1, Status: model.StatusActive},
		{LeaseID: "live2", Resource: "r2", Holder: "h2", FencingToken: 2, AcquiredAt: 2, TTLSeconds: 90, ExpiresAt: now + 5, LastHeartbeat: 2, Status: model.StatusActive},
		// Same holder, still live: must not double-count.
		{LeaseID: "live3", Resource: "r3", Holder: "h1", FencingToken: 3, AcquiredAt: 3, TTLSeconds: 90, ExpiresAt: now + 20, LastHeartbeat: 3, Status: model.StatusActive},
		// Logically expired but status still active (not swept yet).
		{LeaseID: "stale1", Resource: "r4", Holder: "h3", FencingToken: 4, AcquiredAt: 4, TTLSeconds: 10, ExpiresAt: now - 1, LastHeartbeat: 4, Status: model.StatusActive},
		// Boundary: expires exactly at now -> expired per sweep semantics.
		{LeaseID: "stale2", Resource: "r5", Holder: "h4", FencingToken: 5, AcquiredAt: 5, TTLSeconds: 10, ExpiresAt: now, LastHeartbeat: 5, Status: model.StatusActive},
		// Released/expired rows must never count regardless of holder.
		{LeaseID: "rel1", Resource: "r6", Holder: "h1", FencingToken: 6, AcquiredAt: 6, TTLSeconds: 90, ExpiresAt: now + 90, LastHeartbeat: 6, Status: model.StatusReleased},
	}
	for _, l := range seed {
		if err := st.InsertLease(tx, l); err != nil {
			t.Fatalf("insert %s: %v", l.LeaseID, err)
		}
	}

	got, err := st.CountHolders(tx, now)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	// Only h1 and h2 hold not-yet-expired active leases; h3/h4 are logically
	// expired (stale1/stale2) and the released lease contributes nothing.
	if got != 2 {
		t.Fatalf("expected 2 holders, got %d", got)
	}
}

func TestMaxOpenConnsIsOne(t *testing.T) {
	st := newTestStore(t)
	if got := st.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("expected MaxOpenConnections=1, got %d", got)
	}
}

var _ = sql.ErrNoRows // keep database/sql referenced for clarity
