// Command task100-leasetoken serves the resource-lease HTTP API backed by
// SQLite, and provides a --smoke-test that exercises the full fencing-token
// contract (monotonicity, renew window, restart recovery, idempotent release,
// transfer, TTL update, locking, audit) without real-time sleeps.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"task100-leasetoken/internal/api"
	"task100-leasetoken/internal/clock"
	"task100-leasetoken/internal/lease"
	"task100-leasetoken/internal/store"
)

// DefaultAdminToken protects admin endpoints. Override with LEASE_ADMIN_TOKEN.
const DefaultAdminToken = "admin-secret"

func main() {
	smoke := flag.Bool("smoke-test", false, "run self-check and exit")
	dbPath := flag.String("db", "lease.db", "SQLite database file path")
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	if *smoke {
		if err := runSmokeTest(); err != nil {
			fmt.Println("smoke-test: FAIL:", err)
			osExit(1)
		}
		fmt.Println("smoke-test: ok")
		osExit(0)
	}

	adminToken := os.Getenv("LEASE_ADMIN_TOKEN")
	if adminToken == "" {
		adminToken = DefaultAdminToken
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	mgr := lease.New(st, clock.RealClock{})
	if n, err := mgr.Recover(); err != nil {
		log.Fatalf("recover: %v", err)
	} else if n > 0 {
		log.Printf("recover: expired %d stale leases", n)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.NewMux(mgr, adminToken),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("lease service %s listening on %s (db=%s)", api.Version, *addr, *dbPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// osExit is indirected so tests can substitute it; in production it is os.Exit.
var osExit = os.Exit
