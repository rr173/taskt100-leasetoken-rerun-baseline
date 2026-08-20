// Package api wires the lease Manager to HTTP/JSON endpoints. Handlers are
// plain functions over *lease.Manager so the same mux is shared by the real
// server, the smoke test and the handler tests (httptest).
package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"task100-leasetoken/internal/lease"
	"task100-leasetoken/internal/model"
)

// AdminTokenHeader is the header consulted by admin-only endpoints
// (force-release, resource lock/unlock). The token is configured on the mux.
const AdminTokenHeader = "X-Admin-Token"

// NewMux builds the HTTP handler tree over the given manager. adminToken is
// the shared secret required by admin endpoints; empty disables admin
// protection (used by the smoke test, which supplies its own token).
func NewMux(mgr *lease.Manager, adminToken string) http.Handler {
	mux := http.NewServeMux()
	requireAdmin := func(w http.ResponseWriter, r *http.Request) bool {
		if adminToken != "" && r.Header.Get(AdminTokenHeader) != adminToken {
			writeError(w, http.StatusUnauthorized, "invalid admin token")
			return false
		}
		return true
	}

	// --- lease lifecycle (fencing-token authenticated) ---
	mux.HandleFunc("POST /acquire", handleAcquire(mgr))
	mux.HandleFunc("POST /renew", handleRenew(mgr))
	mux.HandleFunc("POST /release", handleRelease(mgr))
	mux.HandleFunc("POST /heartbeat", handleHeartbeat(mgr))
	mux.HandleFunc("POST /transfer", handleTransfer(mgr))
	mux.HandleFunc("PATCH /lease/{id}/ttl", handleTTL(mgr))

	// --- admin-gated lease/resource ops ---
	mux.HandleFunc("DELETE /lease/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		handleForceRelease(mgr)(w, r)
	})
	mux.HandleFunc("POST /resource/{name}/lock", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		handleLock(mgr, true)(w, r)
	})
	mux.HandleFunc("POST /resource/{name}/unlock", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		handleLock(mgr, false)(w, r)
	})
	mux.HandleFunc("POST /admin/recover", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		handleRecover(mgr)(w, r)
	})

	// --- blocking acquire ---
	mux.HandleFunc("POST /acquire/wait", handleAcquireWait(mgr))

	// --- sweep (system, no auth) ---
	mux.HandleFunc("POST /sweep", handleSweep(mgr))

	// --- read endpoints ---
	mux.HandleFunc("GET /lease/{id}", handleGetLease(mgr))
	mux.HandleFunc("GET /lease/{id}/remaining", handleRemaining(mgr))
	mux.HandleFunc("GET /leases", handleListLeases(mgr))
	mux.HandleFunc("GET /resource/{name}", handleGetResource(mgr))
	mux.HandleFunc("GET /resources", handleListResources(mgr))
	mux.HandleFunc("GET /holders/{holder}/leases", handleHolderLeases(mgr))
	mux.HandleFunc("GET /lease/{id}/audit", handleLeaseAudit(mgr))
	mux.HandleFunc("GET /audit", handleListAudit(mgr))
	mux.HandleFunc("GET /stats", handleStats(mgr))
	mux.HandleFunc("GET /metrics", handleMetrics(mgr))
	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /version", handleVersion)
	registerReportRoutes(mux, mgr)

	return mux
}

// --- handlers ---

func handleAcquire(m *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.AcquireRequest
		if err := decode(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		resp, err := m.Acquire(req.Resource, req.Holder, req.TTLSeconds)
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleAcquireWait(m *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.AcquireWaitRequest
		if err := decode(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		resp, err := m.AcquireWaitContext(r.Context(), req)
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleRenew(m *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.RenewRequest
		if err := decode(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		resp, err := m.Renew(req.LeaseID, req.FencingToken, req.TTLSeconds)
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleRelease(m *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.ReleaseRequest
		if err := decode(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := m.Release(req.LeaseID, req.FencingToken); err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleHeartbeat(m *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.HeartbeatRequest
		if err := decode(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		resp, err := m.Heartbeat(req.LeaseID, req.FencingToken)
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleTransfer(m *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.TransferRequest
		if err := decode(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		resp, err := m.Transfer(req.LeaseID, req.FencingToken, req.NewHolder)
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleTTL(m *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.TTLUpdateRequest
		if err := decode(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.LeaseID == "" {
			req.LeaseID = r.PathValue("id")
		}
		resp, err := m.UpdateTTL(req.LeaseID, req.FencingToken, req.NewTTLSeconds)
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleForceRelease(m *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			writeError(w, http.StatusBadRequest, "lease id required")
			return
		}
		if err := m.ForceRelease(id, "admin"); err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleLock(m *lease.Manager, locked bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if name == "" {
			writeError(w, http.StatusBadRequest, "resource name required")
			return
		}
		var err error
		if locked {
			err = m.LockResource(name, "admin")
		} else {
			err = m.UnlockResource(name, "admin")
		}
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "locked": locked})
	}
}

func handleRecover(m *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, err := m.Recover()
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, model.SweepResponse{Expired: n})
	}
}

func handleSweep(m *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp, err := m.Sweep()
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleGetLease(m *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		l, err := m.GetLease(id)
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, l)
	}
}

func handleRemaining(m *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		resp, err := m.Remaining(id)
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleListLeases(m *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f := model.ListLeasesFilter{
			Status:   r.URL.Query().Get("status"),
			Resource: r.URL.Query().Get("resource"),
			Holder:   r.URL.Query().Get("holder"),
			Limit:    queryInt(r, "limit", 0),
		}
		out, err := m.ListLeases(f)
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func handleGetResource(m *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		res, found, err := m.GetResource(name)
		if err != nil {
			writeManagerError(w, err)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "resource not found")
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

func handleListResources(m *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := m.ListResources()
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func handleHolderLeases(m *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		holder := r.PathValue("holder")
		out, err := m.LeasesByHolder(holder, queryInt(r, "limit", 0))
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func handleLeaseAudit(m *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		out, err := m.ListAudit(model.ListAuditFilter{LeaseID: id, Limit: queryInt(r, "limit", 100)})
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func handleListAudit(m *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f := model.ListAuditFilter{
			LeaseID:  r.URL.Query().Get("lease_id"),
			Resource: r.URL.Query().Get("resource"),
			Action:   r.URL.Query().Get("action"),
			Limit:    queryInt(r, "limit", 100),
		}
		out, err := m.ListAudit(f)
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func handleStats(m *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, err := m.Stats()
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s)
	}
}

func handleMetrics(m *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		me, err := m.Metrics()
		if err != nil {
			writeManagerError(w, err)
			return
		}
		// Prometheus-style text exposition for ops convenience.
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		var b strings.Builder
		b.WriteString("# TYPE lease_resources_total gauge\n")
		b.WriteString("lease_resources_total " + strconv.Itoa(me.Resources) + "\n")
		b.WriteString("# TYPE lease_active gauge\n")
		b.WriteString("lease_active " + strconv.Itoa(me.ActiveLeases) + "\n")
		b.WriteString("# TYPE lease_released gauge\n")
		b.WriteString("lease_released " + strconv.Itoa(me.ReleasedLeases) + "\n")
		b.WriteString("# TYPE lease_expired gauge\n")
		b.WriteString("lease_expired " + strconv.Itoa(me.ExpiredLeases) + "\n")
		b.WriteString("# TYPE lease_holders gauge\n")
		b.WriteString("lease_holders " + strconv.Itoa(me.Holders) + "\n")
		b.WriteString("# TYPE lease_locked_resources gauge\n")
		b.WriteString("lease_locked_resources " + strconv.Itoa(me.LockedResources) + "\n")
		b.WriteString("# TYPE lease_max_fencing_token gauge\n")
		b.WriteString("lease_max_fencing_token " + strconv.FormatInt(me.MaxFencingToken, 10) + "\n")
		b.WriteString("# TYPE lease_audit_entries gauge\n")
		b.WriteString("lease_audit_entries " + strconv.Itoa(me.AuditEntries) + "\n")
		_, _ = w.Write([]byte(b.String()))
	}
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Version is the service build identifier exposed by GET /version.
const Version = "task100-leasetoken v1.0.0"

func handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": Version})
}

// --- helpers ---

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	defer r.Body.Close()
	return dec.Decode(v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, model.ErrorBody{Error: msg})
}

// writeManagerError maps sentinel manager errors to HTTP status codes so
// clients can distinguish conflict / not-found / auth / gone from generic
// internal errors.
func writeManagerError(w http.ResponseWriter, err error) {
	switch {
	case isErr(err, lease.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case isErr(err, lease.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case isErr(err, lease.ErrFencingMismatch):
		writeError(w, http.StatusForbidden, err.Error())
	case isErr(err, lease.ErrResourceLocked):
		writeError(w, http.StatusLocked, err.Error())
	case isErr(err, lease.ErrRenewTooEarly):
		writeError(w, http.StatusTooEarly, err.Error())
	case isErr(err, lease.ErrLeaseExpired), isErr(err, lease.ErrTerminal):
		writeError(w, http.StatusGone, err.Error())
	case isErr(err, lease.ErrTimeout):
		writeError(w, http.StatusGatewayTimeout, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

func isErr(err, target error) bool { return err.Error() == target.Error() }

func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}
