// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package server_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/siderolabs/kms-client/pkg/server"
)

func TestDeadManSwitchBlocksNodeAfterTimeout(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	storePath := filepath.Join(t.TempDir(), "leases.json")

	store, err := server.NewFileLeaseStore(storePath, nil)
	require.NoError(t, err)

	_, err = store.Bootstrap("node-1", "10.0.0.1", now, 5*time.Minute)
	require.NoError(t, err)

	_, err = store.Bootstrap("node-2", "10.0.0.2", now, 5*time.Minute)
	require.NoError(t, err)

	// Advance time past the heartbeat timeout for node-1 only.
	now = now.Add(60 * time.Second)

	// Send heartbeat for node-2 to keep it alive.
	_, err = store.HeartbeatByUUID("node-2", "10.0.0.2", "", now, 5*time.Minute)
	require.NoError(t, err)

	// Advance time to exceed timeout (120s total from bootstrap).
	now = now.Add(61 * time.Second)

	dms := server.NewDeadManSwitch(server.DeadManSwitchOptions{
		LeaseStore:       store,
		Logger:           zaptest.NewLogger(t),
		CheckInterval:    time.Second, // not used directly in check()
		HeartbeatTimeout: 120 * time.Second,
		Now:              func() time.Time { return now },
	})

	// Run a single check cycle.
	dms.Check()

	// node-1 should be blocked.
	record1, ok, err := store.Get("node-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, server.LeaseStatusBlocked, record1.Status)
	require.Equal(t, "heartbeat_timeout", record1.BlockReason)
	require.False(t, record1.BlockedAt.IsZero())

	// node-2 should still be active (heartbeat was refreshed).
	record2, ok, err := store.Get("node-2")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, server.LeaseStatusActive, record2.Status)
}

func TestDeadManSwitchDoesNotBlockBeforeTimeout(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	storePath := filepath.Join(t.TempDir(), "leases.json")

	store, err := server.NewFileLeaseStore(storePath, nil)
	require.NoError(t, err)

	_, err = store.Bootstrap("node-1", "10.0.0.1", now, 5*time.Minute)
	require.NoError(t, err)

	// Advance time but stay within the timeout window.
	now = now.Add(119 * time.Second)

	dms := server.NewDeadManSwitch(server.DeadManSwitchOptions{
		LeaseStore:       store,
		Logger:           zaptest.NewLogger(t),
		CheckInterval:    time.Second,
		HeartbeatTimeout: 120 * time.Second,
		Now:              func() time.Time { return now },
	})

	dms.Check()

	record, ok, err := store.Get("node-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, server.LeaseStatusActive, record.Status)
}
