// Package model defines the data types shared across the lease service layers
// (store, manager, api). Keeping them in a dedicated package avoids import
// cycles between the persistence and business-logic packages.
package model

// Status of a lease.
const (
	StatusActive   = "active"
	StatusReleased = "released"
	StatusExpired  = "expired"
)

// IsTerminal reports whether a lease status can no longer transition. Terminal
// leases (released/expired) only accept idempotent reads and matching-token
// releases; they reject renew, heartbeat and transfer.
func IsTerminal(status string) bool {
	return status == StatusReleased || status == StatusExpired
}

// Audit actions recorded on every state transition.
const (
	AuditAcquire      = "acquire"
	AuditRenew        = "renew"
	AuditRelease      = "release"
	AuditHeartbeat    = "heartbeat"
	AuditSweep        = "sweep"
	AuditTransfer     = "transfer"
	AuditForceRelease = "force_release"
	AuditTTLUpdate    = "ttl_update"
	AuditLock         = "lock"
	AuditUnlock       = "unlock"
	AuditRecover      = "recover"
)

// Lease is the persistent representation of a single resource lease.
//
// Time fields are Unix seconds, sourced from the injectable Clock so the
// manager is deterministic under test. fencing_token is monotonic per
// resource and authenticates renew/release/heartbeat/transfer calls: a caller
// must present the exact token the lease was acquired with.
type Lease struct {
	LeaseID       string `json:"lease_id"`
	Resource      string `json:"resource"`
	Holder        string `json:"holder"`
	FencingToken  int64  `json:"fencing_token"`
	AcquiredAt    int64  `json:"acquired_at"`
	TTLSeconds    int64  `json:"ttl_seconds"`
	ExpiresAt     int64  `json:"expires_at"`
	LastHeartbeat int64  `json:"last_heartbeat"`
	Status        string `json:"status"`
}

// Remaining returns the remaining seconds of validity at the given now. May
// be negative when the lease is past expiry.
func (l Lease) Remaining(now int64) int64 { return l.ExpiresAt - now }

// Resource holds the per-resource fencing-token counter, the id of the
// currently active lease and a locked flag (locked resources reject acquire).
type Resource struct {
	Name           string `json:"name"`
	FencingToken   int64  `json:"fencing_token"`
	CurrentLeaseID string `json:"current_lease_id"`
	Locked         bool   `json:"locked"`
}

// AuditEntry is an append-only record of a state transition.
type AuditEntry struct {
	ID           int64  `json:"id"`
	LeaseID      string `json:"lease_id"`
	Resource     string `json:"resource"`
	Action       string `json:"action"`
	Actor        string `json:"actor"`
	FencingToken int64  `json:"fencing_token"`
	At           int64  `json:"at"`
	Detail       string `json:"detail"`
}

// --- request / response envelopes ---

// AcquireRequest is the JSON body for POST /acquire.
type AcquireRequest struct {
	Resource   string `json:"resource"`
	Holder     string `json:"holder"`
	TTLSeconds int64  `json:"ttl_seconds"`
}

// AcquireResponse is returned on successful Acquire.
type AcquireResponse struct {
	LeaseID      string `json:"lease_id"`
	FencingToken int64  `json:"fencing_token"`
	ExpiresAt    int64  `json:"expires_at"`
}

// AcquireWaitRequest is the JSON body for POST /acquire/wait, a polling
// variant that retries until the resource is free or the deadline elapses.
type AcquireWaitRequest struct {
	Resource     string `json:"resource"`
	Holder       string `json:"holder"`
	TTLSeconds   int64  `json:"ttl_seconds"`
	TimeoutSecs  int64  `json:"timeout_seconds"`
	PollInterval int64  `json:"poll_interval_seconds"`
}

// RenewRequest is the JSON body for POST /renew.
type RenewRequest struct {
	LeaseID      string `json:"lease_id"`
	FencingToken int64  `json:"fencing_token"`
	TTLSeconds   int64  `json:"ttl_seconds"`
}

// RenewResponse is returned on successful Renew.
type RenewResponse struct {
	FencingToken int64 `json:"fencing_token"`
	ExpiresAt    int64 `json:"expires_at"`
}

// ReleaseRequest is the JSON body for POST /release.
type ReleaseRequest struct {
	LeaseID      string `json:"lease_id"`
	FencingToken int64  `json:"fencing_token"`
}

// HeartbeatRequest is the JSON body for POST /heartbeat.
type HeartbeatRequest struct {
	LeaseID      string `json:"lease_id"`
	FencingToken int64  `json:"fencing_token"`
}

// HeartbeatResponse is returned on successful Heartbeat.
type HeartbeatResponse struct {
	LastHeartbeat int64 `json:"last_heartbeat"`
}

// TransferRequest moves a lease to a new holder within one transaction. The old
// lease is retired (released) and a fresh lease is created carrying a fencing
// token one higher than the resource's current counter.
type TransferRequest struct {
	LeaseID      string `json:"lease_id"`
	FencingToken int64  `json:"fencing_token"`
	NewHolder    string `json:"new_holder"`
}

// TransferResponse is returned on successful Transfer.
type TransferResponse struct {
	OldLeaseID   string `json:"old_lease_id"`
	NewLeaseID   string `json:"new_lease_id"`
	FencingToken int64  `json:"fencing_token"`
	ExpiresAt    int64  `json:"expires_at"`
}

// TTLUpdateRequest changes a lease's ttl_seconds and recomputes expires_at from
// now. The renew-window does not apply; only fencing-token auth and freshness.
type TTLUpdateRequest struct {
	LeaseID       string `json:"lease_id"`
	FencingToken  int64  `json:"fencing_token"`
	NewTTLSeconds int64  `json:"new_ttl_seconds"`
}

// SweepResponse is returned by POST /sweep and POST /admin/recover.
type SweepResponse struct {
	Expired int `json:"expired"`
}

// Stats aggregates counts the service exposes via GET /stats.
type Stats struct {
	Resources       int `json:"resources"`
	ActiveLeases    int `json:"active_leases"`
	ReleasedLeases  int `json:"released_leases"`
	ExpiredLeases   int `json:"expired_leases"`
	LockedResources int `json:"locked_resources"`
	Holders         int `json:"holders"`
}

// RemainingResponse is returned by GET /lease/{id}/remaining.
type RemainingResponse struct {
	LeaseID   string `json:"lease_id"`
	Remaining int64  `json:"remaining"`
	Expired   bool   `json:"expired"`
}

// ErrorBody is the JSON error envelope.
type ErrorBody struct {
	Error string `json:"error"`
}

// ListLeasesFilter carries optional query filters for GET /leases.
type ListLeasesFilter struct {
	Status   string
	Resource string
	Holder   string
	Limit    int
}

// ListAuditFilter carries optional query filters for GET /audit.
type ListAuditFilter struct {
	LeaseID  string
	Resource string
	Action   string
	Limit    int
}
