package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"task100-leasetoken/internal/clock"
	"task100-leasetoken/internal/lease"
	"task100-leasetoken/internal/store"
)

const testAdminToken = "admin-secret"

func newTestMux(t *testing.T) (*lease.Manager, *clock.FakeClock, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	clk := clock.NewFakeClock(1_000_000)
	mgr := lease.New(st, clk)
	return mgr, clk, NewMux(mgr, testAdminToken)
}

func do(t *testing.T, h http.Handler, method, target string, body any, admin bool) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, target, nil)
	} else {
		buf, _ := json.Marshal(body)
		r = httptest.NewRequest(method, target, bytes.NewReader(buf))
		r.Header.Set("Content-Type", "application/json")
	}
	if admin {
		r.Header.Set(AdminTokenHeader, testAdminToken)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAcquireConflictViaHTTP(t *testing.T) {
	_, _, h := newTestMux(t)
	body := map[string]any{"resource": "r", "holder": "h1", "ttl_seconds": 100}
	w := do(t, h, "POST", "/acquire", body, false)
	if w.Code != 200 {
		t.Fatalf("first acquire: %d %s", w.Code, w.Body.String())
	}
	w2 := do(t, h, "POST", "/acquire", body, false)
	if w2.Code != 409 {
		t.Fatalf("conflict: got %d, want 409", w2.Code)
	}
}

func TestRenewWindowViaHTTP(t *testing.T) {
	_, clk, h := newTestMux(t)
	w := do(t, h, "POST", "/acquire", map[string]any{"resource": "r", "holder": "h1", "ttl_seconds": 100}, false)
	var resp struct {
		LeaseID      string `json:"lease_id"`
		FencingToken int64  `json:"fencing_token"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	// too early
	w2 := do(t, h, "POST", "/renew", map[string]any{"lease_id": resp.LeaseID, "fencing_token": resp.FencingToken, "ttl_seconds": 100}, false)
	if w2.Code != 425 {
		t.Fatalf("too-early renew: got %d, want 425", w2.Code)
	}
	clk.Advance(55)
	w3 := do(t, h, "POST", "/renew", map[string]any{"lease_id": resp.LeaseID, "fencing_token": resp.FencingToken, "ttl_seconds": 100}, false)
	if w3.Code != 200 {
		t.Fatalf("in-window renew: got %d, want 200: %s", w3.Code, w3.Body.String())
	}
}

func TestFencingMismatchViaHTTP(t *testing.T) {
	_, _, h := newTestMux(t)
	w := do(t, h, "POST", "/acquire", map[string]any{"resource": "r", "holder": "h1", "ttl_seconds": 100}, false)
	var resp struct {
		LeaseID      string `json:"lease_id"`
		FencingToken int64  `json:"fencing_token"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	w2 := do(t, h, "POST", "/release", map[string]any{"lease_id": resp.LeaseID, "fencing_token": resp.FencingToken + 1}, false)
	if w2.Code != 403 {
		t.Fatalf("mismatch release: got %d, want 403", w2.Code)
	}
}

func TestIdempotentReleaseViaHTTP(t *testing.T) {
	_, _, h := newTestMux(t)
	w := do(t, h, "POST", "/acquire", map[string]any{"resource": "r", "holder": "h1", "ttl_seconds": 100}, false)
	var resp struct {
		LeaseID      string `json:"lease_id"`
		FencingToken int64  `json:"fencing_token"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	do(t, h, "POST", "/release", map[string]any{"lease_id": resp.LeaseID, "fencing_token": resp.FencingToken}, false)
	w3 := do(t, h, "POST", "/release", map[string]any{"lease_id": resp.LeaseID, "fencing_token": resp.FencingToken}, false)
	if w3.Code != 200 {
		t.Fatalf("idempotent release: got %d, want 200", w3.Code)
	}
}

func TestTransferViaHTTP(t *testing.T) {
	_, _, h := newTestMux(t)
	w := do(t, h, "POST", "/acquire", map[string]any{"resource": "r", "holder": "alice", "ttl_seconds": 100}, false)
	var a struct {
		LeaseID      string `json:"lease_id"`
		FencingToken int64  `json:"fencing_token"`
	}
	json.Unmarshal(w.Body.Bytes(), &a)
	w2 := do(t, h, "POST", "/transfer", map[string]any{"lease_id": a.LeaseID, "fencing_token": a.FencingToken, "new_holder": "bob"}, false)
	if w2.Code != 200 {
		t.Fatalf("transfer: %d %s", w2.Code, w2.Body.String())
	}
	var tr struct {
		NewLeaseID string `json:"new_lease_id"`
	}
	json.Unmarshal(w2.Body.Bytes(), &tr)
	// bob heartbeats the new lease with the new token from the response.
}

func TestResourceLockViaHTTP(t *testing.T) {
	_, _, h := newTestMux(t)
	// without admin token -> 401
	w := do(t, h, "POST", "/resource/r/lock", nil, false)
	if w.Code != 401 {
		t.Fatalf("no-admin lock: got %d, want 401", w.Code)
	}
	w = do(t, h, "POST", "/resource/r/lock", nil, true)
	if w.Code != 200 {
		t.Fatalf("lock: %d %s", w.Code, w.Body.String())
	}
	// acquire blocked
	w = do(t, h, "POST", "/acquire", map[string]any{"resource": "r", "holder": "h1", "ttl_seconds": 100}, false)
	if w.Code != 423 {
		t.Fatalf("locked acquire: got %d, want 423", w.Code)
	}
	// unlock
	do(t, h, "POST", "/resource/r/unlock", nil, true)
	w = do(t, h, "POST", "/acquire", map[string]any{"resource": "r", "holder": "h1", "ttl_seconds": 100}, false)
	if w.Code != 200 {
		t.Fatalf("acquire after unlock: %d", w.Code)
	}
}

func TestForceReleaseViaHTTP(t *testing.T) {
	_, _, h := newTestMux(t)
	w := do(t, h, "POST", "/acquire", map[string]any{"resource": "r", "holder": "h1", "ttl_seconds": 100}, false)
	var a struct {
		LeaseID string `json:"lease_id"`
	}
	json.Unmarshal(w.Body.Bytes(), &a)
	// no admin -> 401
	if w2 := do(t, h, "DELETE", "/lease/"+a.LeaseID, nil, false); w2.Code != 401 {
		t.Fatalf("no-admin force-release: %d", w2.Code)
	}
	w2 := do(t, h, "DELETE", "/lease/"+a.LeaseID, nil, true)
	if w2.Code != 200 {
		t.Fatalf("force-release: %d %s", w2.Code, w2.Body.String())
	}
}

func TestSweepAndRemainingViaHTTP(t *testing.T) {
	_, clk, h := newTestMux(t)
	w := do(t, h, "POST", "/acquire", map[string]any{"resource": "r", "holder": "h1", "ttl_seconds": 50}, false)
	var a struct {
		LeaseID string `json:"lease_id"`
	}
	json.Unmarshal(w.Body.Bytes(), &a)
	clk.Advance(60)
	sw := do(t, h, "POST", "/sweep", nil, false)
	if sw.Code != 200 {
		t.Fatalf("sweep: %d", sw.Code)
	}
	rem := do(t, h, "GET", "/lease/"+a.LeaseID+"/remaining", nil, false)
	if rem.Code != 200 {
		t.Fatalf("remaining: %d", rem.Code)
	}
}

func TestStatsAndMetricsViaHTTP(t *testing.T) {
	_, _, h := newTestMux(t)
	do(t, h, "POST", "/acquire", map[string]any{"resource": "r1", "holder": "h1", "ttl_seconds": 100}, false)
	do(t, h, "POST", "/acquire", map[string]any{"resource": "r2", "holder": "h2", "ttl_seconds": 100}, false)
	if w := do(t, h, "GET", "/stats", nil, false); w.Code != 200 {
		t.Fatalf("stats: %d", w.Code)
	}
	w := do(t, h, "GET", "/metrics", nil, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "lease_active") {
		t.Fatalf("metrics: %d %s", w.Code, w.Body.String())
	}
}

func TestListAndAuditViaHTTP(t *testing.T) {
	_, _, h := newTestMux(t)
	w := do(t, h, "POST", "/acquire", map[string]any{"resource": "r", "holder": "h1", "ttl_seconds": 100}, false)
	var a struct {
		LeaseID string `json:"lease_id"`
	}
	json.Unmarshal(w.Body.Bytes(), &a)
	if w := do(t, h, "GET", "/leases?status=active", nil, false); w.Code != 200 {
		t.Fatalf("leases: %d", w.Code)
	}
	if w := do(t, h, "GET", "/resources", nil, false); w.Code != 200 {
		t.Fatalf("resources: %d", w.Code)
	}
	if w := do(t, h, "GET", "/holders/h1/leases", nil, false); w.Code != 200 {
		t.Fatalf("holder leases: %d", w.Code)
	}
	if w := do(t, h, "GET", "/lease/"+a.LeaseID+"/audit", nil, false); w.Code != 200 {
		t.Fatalf("lease audit: %d", w.Code)
	}
	if w := do(t, h, "GET", "/audit?action=acquire", nil, false); w.Code != 200 {
		t.Fatalf("audit: %d", w.Code)
	}
}

func TestHealthAndVersionViaHTTP(t *testing.T) {
	_, _, h := newTestMux(t)
	if w := do(t, h, "GET", "/health", nil, false); w.Code != 200 {
		t.Fatalf("health: %d", w.Code)
	}
	if w := do(t, h, "GET", "/version", nil, false); w.Code != 200 {
		t.Fatalf("version: %d", w.Code)
	}
}

func TestTTLUpdateViaHTTP(t *testing.T) {
	_, clk, h := newTestMux(t)
	w := do(t, h, "POST", "/acquire", map[string]any{"resource": "r", "holder": "h1", "ttl_seconds": 100}, false)
	var a struct {
		LeaseID      string `json:"lease_id"`
		FencingToken int64  `json:"fencing_token"`
	}
	json.Unmarshal(w.Body.Bytes(), &a)
	w2 := do(t, h, "PATCH", "/lease/"+a.LeaseID+"/ttl", map[string]any{"fencing_token": a.FencingToken, "new_ttl_seconds": 10}, false)
	if w2.Code != 200 {
		t.Fatalf("ttl update: %d %s", w2.Code, w2.Body.String())
	}
	_ = clk
}
