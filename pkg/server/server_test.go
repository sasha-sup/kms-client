// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package server_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/siderolabs/kms-client/api/kms"
	"github.com/siderolabs/kms-client/pkg/server"
)

const testNodeUUID = "abcd"

func TestSealUnseal(t *testing.T) {
	t.Parallel()

	key, err := server.GetRandomAESKey()
	require.NoError(t, err)

	passphrase := randomPassphrase(t)

	srv := server.NewServer(zaptest.NewLogger(t), func(_ context.Context, nodeUUID string) ([]byte, error) {
		if nodeUUID != testNodeUUID {
			return nil, fmt.Errorf("unknown node %s", nodeUUID)
		}

		return key, nil
	}, server.Options{})

	ctx := contextWithPeerIP(t.Context(), "10.0.0.10")

	encrypted, err := srv.Seal(ctx, &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     passphrase,
	})
	require.NoError(t, err)
	require.NotEmpty(t, encrypted.Data)

	decrypted, err := srv.Unseal(ctx, &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     encrypted.Data,
	})
	require.NoError(t, err)
	require.Truef(t, bytes.Equal(passphrase, decrypted.Data), "expected %q to be equal to %q", passphrase, decrypted.Data)

	decrypted, err = srv.Unseal(ctx, &kms.Request{
		NodeUuid: "abce",
		Data:     encrypted.Data,
	})
	require.NoError(t, err)
	require.Falsef(t, bytes.Equal(passphrase, decrypted.Data), "expected %q not to be equal to %q", passphrase, decrypted.Data)

	encrypted.Data[0] ^= 0xFF

	decrypted, err = srv.Unseal(ctx, &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     encrypted.Data,
	})
	require.NoError(t, err)
	require.Falsef(t, bytes.Equal(passphrase, decrypted.Data), "expected %q not to be equal to %q", passphrase, decrypted.Data)
}

func TestInvalidInputs(t *testing.T) {
	t.Parallel()

	key, err := server.GetRandomAESKey()
	require.NoError(t, err)

	passphrase := make([]byte, 64)

	_, err = io.ReadFull(rand.Reader, passphrase)
	require.NoError(t, err)

	srv := server.NewServer(zaptest.NewLogger(t), func(_ context.Context, nodeUUID string) ([]byte, error) {
		if nodeUUID != testNodeUUID {
			return nil, fmt.Errorf("unknown node %s", nodeUUID)
		}

		return key, nil
	}, server.Options{})

	ctx := contextWithPeerIP(t.Context(), "10.0.0.10")

	_, err = srv.Seal(ctx, &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     passphrase,
	})
	require.Error(t, err)

	_, err = srv.Unseal(ctx, &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     make([]byte, 0),
	})
	require.Error(t, err)
}

func TestHeartbeatRequiresRegisteredPeerIP(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	srv, _ := newLeaseServer(t, filepath.Join(t.TempDir(), "leases.json"), func() time.Time { return now })

	_, err := srv.Heartbeat(contextWithPeerIP(t.Context(), "10.0.0.10"), &kms.Request{})
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestFirstUnsealAllowedForUnregisteredNode(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	srv, store := newLeaseServer(t, filepath.Join(t.TempDir(), "leases.json"), func() time.Time { return now })
	passphrase := randomPassphrase(t)

	encrypted, err := srv.Seal(contextWithPeerIP(t.Context(), "10.0.0.10"), &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     passphrase,
	})
	require.NoError(t, err)

	// First Unseal for an unregistered node should succeed and bootstrap the lease.
	decrypted, err := srv.Unseal(contextWithPeerIP(t.Context(), "10.0.0.10"), &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     encrypted.Data,
	})
	require.NoError(t, err)
	require.True(t, bytes.Equal(passphrase, decrypted.Data))

	// Verify the node was registered by Bootstrap.
	record, ok, err := store.Get(testNodeUUID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, server.LeaseStatusActive, record.Status)
	require.Equal(t, "10.0.0.10", record.LastUnsealIP)
}

func TestHeartbeatRefreshAllowsUnseal(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	srv, store := newLeaseServer(t, filepath.Join(t.TempDir(), "leases.json"), func() time.Time { return now })
	passphrase := randomPassphrase(t)

	_, err := store.Bootstrap(testNodeUUID, "10.0.0.10", now, 5*time.Second)
	require.NoError(t, err)

	encrypted, err := srv.Seal(contextWithPeerIP(t.Context(), "10.0.0.10"), &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     passphrase,
	})
	require.NoError(t, err)

	_, err = srv.Unseal(contextWithPeerIP(t.Context(), "10.0.0.10"), &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     encrypted.Data,
	})
	require.NoError(t, err)

	now = now.Add(2 * time.Second)

	_, err = srv.Heartbeat(contextWithPeerIP(t.Context(), "10.0.0.10"), &kms.Request{})
	require.NoError(t, err)

	now = now.Add(2 * time.Second)

	decrypted, err := srv.Unseal(contextWithPeerIP(t.Context(), "10.0.0.10"), &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     encrypted.Data,
	})
	require.NoError(t, err)
	require.True(t, bytes.Equal(passphrase, decrypted.Data))
}

func TestHeartbeatRejectsUnknownPeerIP(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	srv, store := newLeaseServer(t, filepath.Join(t.TempDir(), "leases.json"), func() time.Time { return now })
	passphrase := randomPassphrase(t)

	_, err := store.Bootstrap(testNodeUUID, "10.0.0.10", now, 5*time.Second)
	require.NoError(t, err)

	encrypted, err := srv.Seal(contextWithPeerIP(t.Context(), "10.0.0.10"), &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     passphrase,
	})
	require.NoError(t, err)

	_, err = srv.Unseal(contextWithPeerIP(t.Context(), "10.0.0.10"), &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     encrypted.Data,
	})
	require.NoError(t, err)

	_, err = srv.Heartbeat(contextWithPeerIP(t.Context(), "10.0.0.11"), &kms.Request{})
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestExpiredLeaseBlocksHeartbeatAndUnseal(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	srv, store := newLeaseServer(t, filepath.Join(t.TempDir(), "leases.json"), func() time.Time { return now })
	passphrase := randomPassphrase(t)

	_, err := store.Bootstrap(testNodeUUID, "10.0.0.10", now, 5*time.Second)
	require.NoError(t, err)

	encrypted, err := srv.Seal(contextWithPeerIP(t.Context(), "10.0.0.10"), &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     passphrase,
	})
	require.NoError(t, err)

	_, err = srv.Unseal(contextWithPeerIP(t.Context(), "10.0.0.10"), &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     encrypted.Data,
	})
	require.NoError(t, err)

	now = now.Add(6 * time.Second)

	_, err = srv.Heartbeat(contextWithPeerIP(t.Context(), "10.0.0.10"), &kms.Request{})
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	_, err = srv.Unseal(contextWithPeerIP(t.Context(), "10.0.0.10"), &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     encrypted.Data,
	})
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestExpiredLeaseSurvivesServerRestart(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	storePath := filepath.Join(t.TempDir(), "leases.json")
	srv, store := newLeaseServer(t, storePath, func() time.Time { return now })
	passphrase := randomPassphrase(t)

	_, err := store.Bootstrap(testNodeUUID, "10.0.0.10", now, 5*time.Second)
	require.NoError(t, err)

	encrypted, err := srv.Seal(contextWithPeerIP(t.Context(), "10.0.0.10"), &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     passphrase,
	})
	require.NoError(t, err)

	_, err = srv.Unseal(contextWithPeerIP(t.Context(), "10.0.0.10"), &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     encrypted.Data,
	})
	require.NoError(t, err)

	now = now.Add(6 * time.Second)

	restartedServer, _ := newLeaseServer(t, storePath, func() time.Time { return now })

	_, err = restartedServer.Unseal(contextWithPeerIP(t.Context(), "10.0.0.10"), &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     encrypted.Data,
	})
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestUnsealRebindsPeerIPForHeartbeat(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	srv, store := newLeaseServer(t, filepath.Join(t.TempDir(), "leases.json"), func() time.Time { return now })
	passphrase := randomPassphrase(t)

	_, err := store.Bootstrap(testNodeUUID, "10.0.0.10", now, 5*time.Second)
	require.NoError(t, err)

	encrypted, err := srv.Seal(contextWithPeerIP(t.Context(), "10.0.0.10"), &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     passphrase,
	})
	require.NoError(t, err)

	_, err = srv.Unseal(contextWithPeerIP(t.Context(), "10.0.0.10"), &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     encrypted.Data,
	})
	require.NoError(t, err)

	_, err = srv.Heartbeat(contextWithPeerIP(t.Context(), "10.0.0.11"), &kms.Request{})
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	now = now.Add(2 * time.Second)

	_, err = srv.Unseal(contextWithPeerIP(t.Context(), "10.0.0.11"), &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     encrypted.Data,
	})
	require.NoError(t, err)

	_, err = srv.Heartbeat(contextWithPeerIP(t.Context(), "10.0.0.11"), &kms.Request{})
	require.NoError(t, err)
}

func TestFileLeaseStorePersistsHeartbeatState(t *testing.T) {
	t.Parallel()

	storePath := filepath.Join(t.TempDir(), "leases.json")
	store, err := server.NewFileLeaseStore(storePath, nil)
	require.NoError(t, err)

	firstUnseal := time.Unix(1_700_000_000, 0).UTC()
	secondHeartbeat := firstUnseal.Add(2 * time.Second)

	_, err = store.Bootstrap(testNodeUUID, "10.0.0.10", firstUnseal, 5*time.Second)
	require.NoError(t, err)

	_, _, err = store.HeartbeatByIP("10.0.0.10", secondHeartbeat, 5*time.Second)
	require.NoError(t, err)

	reloadedStore, err := server.NewFileLeaseStore(storePath, nil)
	require.NoError(t, err)

	state, ok, err := reloadedStore.Get(testNodeUUID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "10.0.0.10", state.LastUnsealIP)
	require.Equal(t, secondHeartbeat.UTC(), state.LastHeartbeatAt)
	require.Equal(t, secondHeartbeat.UTC().Add(5*time.Second), state.LeaseUntil)
	require.Equal(t, server.LeaseStatusActive, state.Status)
}

func newLeaseServer(t *testing.T, storePath string, now func() time.Time) (*server.Server, *server.LeaseStore) {
	t.Helper()

	key, err := server.GetRandomAESKey()
	require.NoError(t, err)

	store, err := server.NewFileLeaseStore(storePath, nil)
	require.NoError(t, err)

	srv := server.NewServer(zaptest.NewLogger(t), func(_ context.Context, nodeUUID string) ([]byte, error) {
		if nodeUUID != testNodeUUID {
			return nil, fmt.Errorf("unknown node %s", nodeUUID)
		}

		return key, nil
	}, server.Options{
		LeaseStore:    store,
		LeaseDuration: 5 * time.Second,
		Now:           now,
	})

	return srv, store
}

func randomPassphrase(t *testing.T) []byte {
	t.Helper()

	passphrase := make([]byte, 32)

	_, err := io.ReadFull(rand.Reader, passphrase)
	require.NoError(t, err)

	return passphrase
}

func contextWithPeerIP(ctx context.Context, ip string) context.Context {
	return peer.NewContext(ctx, &peer.Peer{
		Addr: &net.TCPAddr{
			IP:   net.ParseIP(ip),
			Port: 4050,
		},
	})
}
