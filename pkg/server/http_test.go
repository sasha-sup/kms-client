// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/siderolabs/kms-client/api/kms"
	"github.com/siderolabs/kms-client/pkg/server"
)

const testHMACKey = "test-secret-key"
const testAdminToken = "test-admin-token"

func newTestHTTPHandler(t *testing.T, now func() time.Time) (*http.ServeMux, *server.LeaseStore) {
	t.Helper()

	storePath := filepath.Join(t.TempDir(), "leases.json")

	store, err := server.NewFileLeaseStore(storePath, nil)
	require.NoError(t, err)

	if now == nil {
		now = time.Now
	}

	handler := server.NewHTTPHandler(server.HTTPHandlerOptions{
		LeaseStore:        store,
		HMACAuth:          server.NewHMACAuth(testHMACKey),
		AdminToken:        testAdminToken,
		Logger:            zaptest.NewLogger(t),
		LeaseDuration:     5 * time.Minute,
		HeartbeatInterval: 30 * time.Second,
		Now:               now,
	})

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	return mux, store
}

func TestHTTPHeartbeatActiveNode(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	mux, _ := newTestHTTPHandler(t, func() time.Time { return now })

	body := makeHeartbeatBody(t, "node-1", "10.0.0.1", now.Unix())
	hmacAuth := server.NewHMACAuth(testHMACKey)
	sig := hmacAuth.Sign("node-1", "10.0.0.1", now.Unix())

	req := httptest.NewRequest(http.MethodPost, "/heartbeat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-HMAC-Signature", sig)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "ok", resp["status"])
}

func TestHTTPHeartbeatBlockedNodeReturns403(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	mux, store := newTestHTTPHandler(t, func() time.Time { return now })

	// Bootstrap and then block the node via timeout.
	_, err := store.Bootstrap("node-1", "10.0.0.1", now, 5*time.Minute)
	require.NoError(t, err)

	// Advance time past timeout and block.
	now = now.Add(130 * time.Second)
	blocked := store.BlockExpiredNodes(now, 120*time.Second)
	require.Contains(t, blocked, "node-1")

	body := makeHeartbeatBody(t, "node-1", "10.0.0.1", now.Unix())
	hmacAuth := server.NewHMACAuth(testHMACKey)
	sig := hmacAuth.Sign("node-1", "10.0.0.1", now.Unix())

	req := httptest.NewRequest(http.MethodPost, "/heartbeat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-HMAC-Signature", sig)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "node_blocked", resp["error"])
}

func TestHTTPHeartbeatInvalidSignatureReturns401(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	mux, _ := newTestHTTPHandler(t, func() time.Time { return now })

	body := makeHeartbeatBody(t, "node-1", "10.0.0.1", now.Unix())

	req := httptest.NewRequest(http.MethodPost, "/heartbeat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-HMAC-Signature", "invalid-signature")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAdminUnblockWithoutTokenReturns401(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	mux, store := newTestHTTPHandler(t, func() time.Time { return now })

	_, err := store.Bootstrap("node-1", "10.0.0.1", now, 5*time.Minute)
	require.NoError(t, err)

	now = now.Add(130 * time.Second)
	store.BlockExpiredNodes(now, 120*time.Second)

	req := httptest.NewRequest(http.MethodPost, "/admin/nodes/node-1/unblock", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAdminUnblockWithTokenSucceeds(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	mux, store := newTestHTTPHandler(t, func() time.Time { return now })

	_, err := store.Bootstrap("node-1", "10.0.0.1", now, 5*time.Minute)
	require.NoError(t, err)

	now = now.Add(130 * time.Second)
	blocked := store.BlockExpiredNodes(now, 120*time.Second)
	require.Contains(t, blocked, "node-1")

	req := httptest.NewRequest(http.MethodPost, "/admin/nodes/node-1/unblock", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	record, ok, err := store.Get("node-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, server.LeaseStatusActive, record.Status)
	require.Empty(t, record.BlockReason)
}

func TestAdminListNodes(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	mux, store := newTestHTTPHandler(t, func() time.Time { return now })

	_, err := store.Bootstrap("node-1", "10.0.0.1", now, 5*time.Minute)
	require.NoError(t, err)

	_, err = store.Bootstrap("node-2", "10.0.0.2", now, 5*time.Minute)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/admin/nodes", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var nodes []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &nodes))
	require.Len(t, nodes, 2)
}

func TestBlockedNodeCannotUnseal(t *testing.T) {
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

	// Block the node.
	now = now.Add(130 * time.Second)
	blocked := store.BlockExpiredNodes(now, 120*time.Second)
	require.Contains(t, blocked, testNodeUUID)

	// Unseal should fail with PermissionDenied.
	_, err = srv.Unseal(contextWithPeerIP(t.Context(), "10.0.0.10"), &kms.Request{
		NodeUuid: testNodeUUID,
		Data:     encrypted.Data,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "blocked")
}

func TestIdentifyReturnsNodeUUID(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	mux, store := newTestHTTPHandler(t, func() time.Time { return now })

	// Bootstrap a node so it can be found by IP.
	_, err := store.Bootstrap("node-1", "10.0.0.1", now, 5*time.Minute)
	require.NoError(t, err)

	hmacAuth := server.NewHMACAuth(testHMACKey)
	body, err := json.Marshal(map[string]any{
		"node_ip":   "10.0.0.1",
		"timestamp": now.Unix(),
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/node/identify", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-HMAC-Signature", hmacAuth.SignIdentify("10.0.0.1", now.Unix()))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "node-1", resp["node_uuid"])
	require.Equal(t, "active", resp["status"])
}

func TestIdentifyReturns404ForUnknownIP(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	mux, _ := newTestHTTPHandler(t, func() time.Time { return now })

	hmacAuth := server.NewHMACAuth(testHMACKey)
	body, err := json.Marshal(map[string]any{
		"node_ip":   "10.0.0.99",
		"timestamp": now.Unix(),
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/node/identify", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-HMAC-Signature", hmacAuth.SignIdentify("10.0.0.99", now.Unix()))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestIdentifyRejects401WithoutSignature(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	mux, _ := newTestHTTPHandler(t, func() time.Time { return now })

	body, err := json.Marshal(map[string]any{
		"node_ip":   "10.0.0.1",
		"timestamp": now.Unix(),
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/node/identify", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func makeHeartbeatBody(t *testing.T, nodeUUID, nodeIP string, timestamp int64) []byte {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"node_uuid": nodeUUID,
		"node_ip":   nodeIP,
		"timestamp": timestamp,
	})
	require.NoError(t, err)

	return body
}
