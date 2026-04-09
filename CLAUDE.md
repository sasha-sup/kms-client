# KMS Client — Project Rules

## Language
Code, identifiers, and file names remain in English.

## Tech Stack
- Go 1.24, stdlib + dependencies from go.mod only
- gRPC (Seal/Unseal) + HTTP API (heartbeat/admin)
- File-based lease store (JSON)
- HMAC-SHA256 authentication for heartbeat

## Build & Test
```bash
go build ./...
go test ./...
golangci-lint run --config .golangci.yml
```

## Code Style
- Idiomatic Go: interfaces, error wrapping, context propagation
- No hardcoded secrets — environment variables only
- No external dependencies without strong justification
- Follow existing patterns in pkg/server/

## Project Layout
- `cmd/kms-server/` — KMS gRPC + HTTP server
- `cmd/kms-agent/` — Heartbeat agent (K8s DaemonSet)
- `pkg/server/` — Core logic (lease store, HTTP handlers, dead-man switch)
- `scripts/` — Admin CLI tools
- `deploy/` — Documentation only; real Helm chart in `devops/helm-infra/kms-agent/`

## Key Constraints
- Do not break existing functionality
- Run `go build ./...` and `go test ./...` before committing
- Closed network — no external CA TLS verification needed
