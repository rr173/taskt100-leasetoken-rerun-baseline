package api

import (
	"net/http"

	"task100-leasetoken/internal/lease"
)

func registerReportRoutes(mux *http.ServeMux, mgr *lease.Manager) {
	mux.HandleFunc("GET /reports", handleReports(mgr))
	mux.HandleFunc("GET /lease/{id}/health", handleLeaseHealth(mgr))
	mux.HandleFunc("GET /holder/{holder}/report", handleHolderReport(mgr))
	mux.HandleFunc("GET /resource/{name}/advice", handleAcquireAdvice(mgr))
	mux.HandleFunc("GET /lease/{id}/renew-advice", handleRenewAdvice(mgr))
}

func handleReports(mgr *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bundle, err := mgr.ReportBundle(r.URL.Query().Get("resource"))
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, bundle)
	}
}

func handleLeaseHealth(mgr *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		health, err := mgr.LeaseHealth(r.PathValue("id"))
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, health)
	}
}

func handleHolderReport(mgr *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		report, err := mgr.HolderReport(r.PathValue("holder"))
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, report)
	}
}

func handleAcquireAdvice(mgr *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		advice, err := mgr.AcquireAdvice(r.PathValue("name"))
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, advice)
	}
}

func handleRenewAdvice(mgr *lease.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		advice, err := mgr.RenewAdvice(r.PathValue("id"))
		if err != nil {
			writeManagerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, advice)
	}
}
