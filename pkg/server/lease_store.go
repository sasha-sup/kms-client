// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	// ErrLeaseExpired is returned when a persisted node lease is no longer valid.
	ErrLeaseExpired = errors.New("lease expired")
	// ErrLeaseNotFound is returned when no persisted node lease exists.
	ErrLeaseNotFound = errors.New("lease not found")
	// ErrNodeIPNotFound is returned when no node is registered for an observed peer IP.
	ErrNodeIPNotFound = errors.New("node IP not found")
)

// LeaseStatus tracks the persisted dead-man-switch state for a node.
type LeaseStatus string

const (
	LeaseStatusActive  LeaseStatus = "active"
	LeaseStatusExpired LeaseStatus = "expired"
)

// LeaseRecord stores the persisted heartbeat state for a node.
type LeaseRecord struct {
	LastHeartbeatAt time.Time   `json:"last_heartbeat_at"`
	LeaseUntil      time.Time   `json:"lease_until"`
	LastUnsealAt    time.Time   `json:"last_unseal_at"`
	LastUnsealIP    string      `json:"last_unseal_ip"`
	Status          LeaseStatus `json:"status"`
}

type leaseStoreFile struct {
	Nodes map[string]LeaseRecord `json:"nodes"`
}

// LeaseStore persists per-node heartbeat leases.
type LeaseStore struct {
	nodes   map[string]LeaseRecord
	metrics *Metrics
	path    string
	mu      sync.Mutex
}

// NewFileLeaseStore initializes a file-backed lease store.
func NewFileLeaseStore(path string, metrics *Metrics) (*LeaseStore, error) {
	store := &LeaseStore{
		path:    path,
		nodes:   map[string]LeaseRecord{},
		metrics: metrics,
	}

	if path == "" {
		store.updateLeaseMetricsLocked(time.Now().UTC())

		return store, nil
	}

	if err := store.load(); err != nil {
		return nil, err
	}

	return store, nil
}

// Bootstrap creates or resets the lease state for a node after a successful Unseal.
func (store *LeaseStore) Bootstrap(nodeUUID, peerIP string, now time.Time, leaseDuration time.Duration) (LeaseRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	record := activeLease(now.UTC(), leaseDuration)
	record.LastUnsealAt = now.UTC()
	record.LastUnsealIP = peerIP

	for existingNodeUUID, existingRecord := range store.nodes {
		if existingNodeUUID == nodeUUID || existingRecord.LastUnsealIP != peerIP {
			continue
		}

		existingRecord.LastUnsealIP = ""
		store.nodes[existingNodeUUID] = existingRecord
	}

	store.nodes[nodeUUID] = record
	store.updateLeaseMetricsLocked(now.UTC())

	return record, store.persistLocked()
}

// HeartbeatByIP refreshes an already active lease matched by the observed peer IP.
func (store *LeaseStore) HeartbeatByIP(peerIP string, now time.Time, leaseDuration time.Duration) (string, LeaseRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	nodeUUID, record, ok := store.recordByIPLocked(peerIP)
	if !ok {
		return "", LeaseRecord{}, ErrNodeIPNotFound
	}

	now = now.UTC()

	if record.isExpired(now) || record.Status == LeaseStatusExpired {
		record.Status = LeaseStatusExpired
		store.nodes[nodeUUID] = record
		store.updateLeaseMetricsLocked(now)

		if err := store.persistLocked(); err != nil {
			return "", LeaseRecord{}, err
		}

		return nodeUUID, record, ErrLeaseExpired
	}

	record.LastHeartbeatAt = now
	record.LeaseUntil = now.Add(leaseDuration)
	record.Status = LeaseStatusActive
	store.nodes[nodeUUID] = record
	store.updateLeaseMetricsLocked(now)

	return nodeUUID, record, store.persistLocked()
}

// Validate returns the current lease state or an error when the lease is expired.
func (store *LeaseStore) Validate(nodeUUID string, now time.Time) (LeaseRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	record, ok := store.nodes[nodeUUID]
	if !ok {
		return LeaseRecord{}, ErrLeaseNotFound
	}

	now = now.UTC()

	if record.isExpired(now) || record.Status == LeaseStatusExpired {
		record.Status = LeaseStatusExpired
		store.nodes[nodeUUID] = record
		store.updateLeaseMetricsLocked(now)

		if err := store.persistLocked(); err != nil {
			return LeaseRecord{}, err
		}

		return record, ErrLeaseExpired
	}

	return record, nil
}

// Get returns the stored lease record, if it exists.
func (store *LeaseStore) Get(nodeUUID string) (LeaseRecord, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	record, ok := store.nodes[nodeUUID]

	return record, ok, nil
}

func (store *LeaseStore) recordByIPLocked(peerIP string) (string, LeaseRecord, bool) {
	for nodeUUID, record := range store.nodes {
		if record.LastUnsealIP == peerIP {
			return nodeUUID, record, true
		}
	}

	return "", LeaseRecord{}, false
}

func (store *LeaseStore) load() error {
	data, err := os.ReadFile(store.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		store.metrics.incLeaseStoreError("read")

		return fmt.Errorf("failed to read lease state: %w", err)
	}

	if len(data) == 0 {
		return nil
	}

	var payload leaseStoreFile
	if err = json.Unmarshal(data, &payload); err != nil {
		store.metrics.incLeaseStoreError("decode")

		return fmt.Errorf("failed to decode lease state: %w", err)
	}

	if payload.Nodes == nil {
		payload.Nodes = map[string]LeaseRecord{}
	}

	store.nodes = payload.Nodes
	store.updateLeaseMetricsLocked(time.Now().UTC())

	return nil
}

func (store *LeaseStore) persistLocked() error {
	if store.path == "" {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(store.path), 0o755); err != nil {
		store.metrics.incLeaseStoreError("mkdir")

		return fmt.Errorf("failed to create lease state directory: %w", err)
	}

	data, err := json.MarshalIndent(leaseStoreFile{Nodes: store.nodes}, "", "  ")
	if err != nil {
		store.metrics.incLeaseStoreError("encode")

		return fmt.Errorf("failed to encode lease state: %w", err)
	}

	tmpPath := store.path + ".tmp"

	if err = os.WriteFile(tmpPath, data, 0o600); err != nil {
		store.metrics.incLeaseStoreError("write")

		return fmt.Errorf("failed to write lease state: %w", err)
	}

	if err = os.Rename(tmpPath, store.path); err != nil {
		store.metrics.incLeaseStoreError("rename")

		return fmt.Errorf("failed to replace lease state: %w", err)
	}

	return nil
}

func (store *LeaseStore) updateLeaseMetricsLocked(now time.Time) {
	if store.metrics == nil {
		return
	}

	active := 0
	expired := 0

	for _, record := range store.nodes {
		if record.Status == LeaseStatusExpired || record.isExpired(now) {
			expired++
		} else {
			active++
		}
	}

	store.metrics.setLeaseCounts(active, expired)
}

func (record LeaseRecord) isExpired(now time.Time) bool {
	return !record.LeaseUntil.IsZero() && now.After(record.LeaseUntil)
}

func activeLease(now time.Time, leaseDuration time.Duration) LeaseRecord {
	return LeaseRecord{
		LastHeartbeatAt: now,
		LeaseUntil:      now.Add(leaseDuration),
		Status:          LeaseStatusActive,
	}
}
