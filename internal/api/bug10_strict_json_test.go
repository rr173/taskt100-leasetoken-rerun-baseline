package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"task100-leasetoken/internal/clock"
	"task100-leasetoken/internal/lease"
	"task100-leasetoken/internal/store"
)

func TestAcquireRejectsTrailingJSON(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/lease.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mgr := lease.New(st, clock.NewFakeClock(1000))
	req := httptest.NewRequest(http.MethodPost, "/acquire", strings.NewReader(`{"resource":"r","holder":"h","ttl_seconds":10}{"extra":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	NewMux(mgr, "").ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}
