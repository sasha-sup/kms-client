# KMS client

KMS client defines API for network based disk encryption for Talos Linux.

This repository contains:

- `kms-server`: production KMS server for Seal/Unseal and heartbeat lease enforcement
- `kms-client`: node-side heartbeat client for periodic liveness updates to KMS after the node has been registered by a successful `Unseal`
