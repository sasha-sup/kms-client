<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-04-09 | Updated: 2026-04-09 -->

# server

## Purpose

Core KMS server logic: gRPC service implementation (Seal/Unseal/Heartbeat),
HTTP API handlers, file-backed lease store, dead-man's switch monitor,
HMAC authentication, and Prometheus metrics.

## Key Files

| File | Description |
|------|-------------|
| `server.go` | gRPC `KMSServiceServer` implementation — Seal, Unseal, Heartbeat RPCs |
| `http.go` | HTTP handlers: POST /heartbeat, GET /admin/nodes, POST /admin/nodes/{uuid}/unblock |
| `lease_store.go` | File-backed JSON persistence for node lease records with mutex locking |
| `deadman.go` | Background goroutine that blocks nodes with expired heartbeats |
| `hmac.go` | HMAC-SHA256 signing/verification with 30s timestamp rounding |
| `metrics.go` | Prometheus counters/gauges for unseal, heartbeat, leases, timeouts |
| `export_test.go` | Exports internal functions for testing (`GetRandomAESKey`) |
| `server_test.go` | Tests for gRPC Seal/Unseal/Heartbeat handlers |
| `http_test.go` | Tests for HTTP heartbeat and admin endpoints |
| `deadman_test.go` | Tests for dead-man's switch timeout logic |
| `hmac_test.go` | Tests for HMAC sign/verify |

## For AI Agents

### Working In This Directory

- This is the main business logic package — most changes happen here
- All structs use Options pattern for configuration
- Time is injected via `func() time.Time` — use this in tests, never `time.Now()` directly
- LeaseStore uses mutex + atomic JSON file writes (write tmp → rename)
- LeaseRecord states: `active`, `expired`, `blocked`
- Two heartbeat paths: gRPC (by peer IP) and HTTP (by UUID + HMAC)

### Testing Requirements

- Run `go test ./pkg/server/...`
- Tests mock time via Options.Now
- Use `testify/assert` and `testify/require`
- LeaseStore tests use temp directories for file persistence

### Key Types

- `Server` — gRPC service implementation
- `HTTPHandler` — HTTP endpoint handlers
- `LeaseStore` — File-backed node state persistence
- `LeaseRecord` — Per-node state (heartbeat times, lease window, block status)
- `DeadManSwitch` — Background heartbeat timeout checker
- `HMACAuth` — HMAC-SHA256 authenticator
- `Metrics` — Prometheus metrics collection

### Error Sentinel Values

- `ErrLeaseExpired` — lease window exceeded
- `ErrLeaseNotFound` — node not registered
- `ErrNodeIPNotFound` — no node for peer IP
- `ErrLeaseBlocked` — node blocked by deadman switch

## Dependencies

### Internal

- `api/kms` — gRPC service interfaces and protobuf types
- `pkg/constants` — PassphraseSize constant

### External

- `google.golang.org/grpc` — gRPC status codes, peer extraction
- `go.uber.org/zap` — structured logging
- `github.com/prometheus/client_golang` — metrics

<!-- MANUAL: -->
