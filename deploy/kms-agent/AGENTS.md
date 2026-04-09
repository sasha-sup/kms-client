<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-04-09 | Updated: 2026-04-09 -->

# kms-agent

## Purpose

Kubernetes manifests for deploying the KMS heartbeat agent as a DaemonSet
across all cluster nodes (including control-plane).

## Key Files

| File | Description |
|------|-------------|
| `daemonset.yaml` | DaemonSet with tolerations for all nodes, env from fieldRef/ConfigMap/Secret |
| `configmap.yaml` | Agent config: KMS server URL, heartbeat interval |
| `secret.yaml` | Template for HMAC pre-shared key (placeholder value) |
| `serviceaccount.yaml` | ServiceAccount in kms-system namespace |
| `rbac.yaml` | Empty ClusterRole + binding (agent needs no K8s API access) |
| `README.md` | Deployment instructions in Russian |

## For AI Agents

### Working In This Directory

- All resources in `kms-system` namespace
- `NODE_UUID` sourced from `spec.nodeName` (matches Talos machine UUID)
- `NODE_IP` sourced from `status.podIP`
- Secret contains placeholder — never commit real HMAC keys
- Agent resource limits: 10m-50m CPU, 16Mi-32Mi memory

### Deployment Order

1. Create namespace `kms-system`
2. Apply serviceaccount.yaml, rbac.yaml
3. Apply secret.yaml (with real HMAC key)
4. Apply configmap.yaml (with correct KMS server URL)
5. Apply daemonset.yaml

<!-- MANUAL: -->
