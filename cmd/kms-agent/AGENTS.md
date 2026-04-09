<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-04-09 | Updated: 2026-04-09 -->

# kms-agent

## Purpose

HTTP heartbeat agent designed to run as a Kubernetes DaemonSet. Sends HMAC-SHA256
signed heartbeat requests to the KMS server's HTTP endpoint.

## Key Files

| File | Description |
|------|-------------|
| `main.go` | Config from env vars, heartbeat loop with exponential backoff |

## For AI Agents

### Working In This Directory

- All config via environment variables: `NODE_UUID`, `NODE_IP`, `KMS_SERVER_URL`, `HEARTBEAT_HMAC_KEY`
- Optional: `HEARTBEAT_INTERVAL` (default 30s), `HEARTBEAT_TIMEOUT` (default 5s)
- Uses `pkg/server.HMACAuth` for signing requests
- Exponential backoff on failures (capped at 5 minutes)
- Graceful shutdown on SIGINT/SIGTERM
- Continues retrying even on 403 (blocked node) — admin must unblock

<!-- MANUAL: -->
