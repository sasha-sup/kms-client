<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-04-09 | Updated: 2026-04-09 -->

# kms-server

## Purpose

Main entry point for the KMS server. Wires together gRPC server, HTTP API,
Prometheus metrics, and the dead-man's switch background monitor.

## Key Files

| File | Description |
|------|-------------|
| `main.go` | CLI flags, server initialization, errgroup lifecycle management |

## For AI Agents

### Working In This Directory

- CLI flags define all server configuration (endpoints, TLS, heartbeat params)
- Secrets (`HEARTBEAT_HMAC_KEY`, `ADMIN_TOKEN`) are read from env vars
- Three servers run concurrently via errgroup: gRPC (:4050), HTTP (:4051), metrics (:2112)
- HTTP API and DeadManSwitch only start when `--heartbeat-enable` is set AND HMAC key + admin token are provided
- Graceful shutdown via context cancellation on SIGINT

### Key Configuration Flags

- `--kms-api-endpoint` (:4050) — gRPC listen address
- `--http-endpoint` (:4051) — HTTP API for heartbeat/admin
- `--heartbeat-enable` (false) — enable lease enforcement
- `--heartbeat-interval` (30s), `--heartbeat-timeout` (120s), `--heartbeat-check-interval` (30s)
- `--lease-duration` (0) — node heartbeat lease window
- `--lease-store-path` — file path for JSON lease persistence
- `--key-path` (required) — AES encryption key file
- `--tls-enable`, `--tls-cert-path`, `--tls-key-path` — optional TLS

<!-- MANUAL: -->
