<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-04-09 | Updated: 2026-04-09 -->

# kms

## Purpose

Protobuf definition and generated Go stubs for the KMSService gRPC API.

## Key Files

| File | Description |
|------|-------------|
| `kms.proto` | Service definition: Heartbeat, Seal, Unseal RPCs with Request/Response messages |
| `kms.pb.go` | Generated protobuf message types |
| `kms_grpc.pb.go` | Generated gRPC client/server interfaces |
| `kms_vtproto.pb.go` | Generated vtprotobuf fast marshal/unmarshal |

## For AI Agents

### Working In This Directory

- Only edit `kms.proto` — all `.go` files are generated
- Run `make generate` after proto changes
- Request message: `node_uuid` (string) + `data` (bytes)
- Response message: `data` (bytes)
- Three RPCs: `Heartbeat`, `Seal`, `Unseal`

<!-- MANUAL: -->
