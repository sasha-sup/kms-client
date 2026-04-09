// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package server implements a test server for the KMS.
package server

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/siderolabs/kms-client/api/kms"
	"github.com/siderolabs/kms-client/pkg/constants"
)

// Server implents gRPC API.
type Server struct {
	kms.UnimplementedKMSServiceServer

	getKey       func(context.Context, string) ([]byte, error)
	logger       *zap.Logger
	leaseStore   *LeaseStore
	metrics      *Metrics
	now          func() time.Time
	leaseWindow  time.Duration
	leaseEnabled bool
}

// Options controls optional lease enforcement.
type Options struct {
	LeaseStore    *LeaseStore
	Metrics       *Metrics
	Now           func() time.Time
	LeaseDuration time.Duration
}

// NewServer initializes new server.
func NewServer(logger *zap.Logger, keyHandler func(ctx context.Context, nodeUUID string) ([]byte, error), options Options) *Server {
	now := options.Now
	if now == nil {
		now = time.Now
	}

	return &Server{
		logger:       logger,
		getKey:       keyHandler,
		leaseStore:   options.LeaseStore,
		metrics:      options.Metrics,
		leaseEnabled: options.LeaseStore != nil && options.LeaseDuration > 0,
		leaseWindow:  options.LeaseDuration,
		now:          now,
	}
}

// Seal encrypts the incoming data.
func (srv *Server) Seal(ctx context.Context, req *kms.Request) (*kms.Response, error) {
	time.Sleep(time.Second)

	if err := validateNodeUUID(req); err != nil {
		return nil, err
	}

	key, err := srv.getKey(ctx, req.NodeUuid)
	if err != nil {
		srv.logger.Error("failed to get key for node, using random key",
			zap.String("node_uuid", req.NodeUuid),
			zap.Error(err),
		)

		key, err = getRandomAESKey()
		if err != nil {
			return nil, err
		}
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, aesgcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	if len(req.Data) != constants.PassphraseSize {
		return nil, status.Error(codes.InvalidArgument, "incorrect data length")
	}

	encrypted := aesgcm.Seal(nil, nonce, req.Data, nil)

	srv.logger.Debug("sealed the data",
		zap.String("node_uuid", req.NodeUuid),
		zap.String("kcv", keyCheckValue(req.Data, req.NodeUuid)),
	)

	return &kms.Response{
		Data: append(nonce, encrypted...), //nolint:makezero
	}, nil
}

// Heartbeat refreshes the node lease before the current lease expires.
func (srv *Server) Heartbeat(ctx context.Context, _ *kms.Request) (_ *kms.Response, err error) {
	startedAt := time.Now()

	defer func() {
		srv.metrics.observeHeartbeat(err, time.Since(startedAt))
	}()

	if !srv.leaseEnabled {
		return nil, status.Error(codes.FailedPrecondition, "heartbeat leases are disabled")
	}

	peerIP, err := peerIPFromContext(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "failed to identify heartbeat peer: %v", err)
	}

	nodeUUID, leaseRecord, err := srv.leaseStore.HeartbeatByIP(peerIP, srv.now(), srv.leaseWindow)
	if err != nil {
		switch {
		case errors.Is(err, ErrLeaseBlocked):
			srv.logger.Warn("rejected heartbeat for blocked node",
				zap.String("peer_ip", peerIP),
				zap.String("node_uuid", nodeUUID),
				zap.String("block_reason", leaseRecord.BlockReason),
			)

			return nil, status.Error(codes.PermissionDenied, "node blocked")
		case errors.Is(err, ErrLeaseExpired):
			srv.logger.Warn("rejected heartbeat for expired lease",
				zap.String("peer_ip", peerIP),
				zap.String("node_uuid", nodeUUID),
				zap.Time("lease_until", leaseRecord.LeaseUntil),
			)

			return nil, status.Error(codes.PermissionDenied, "heartbeat lease expired")
		case errors.Is(err, ErrNodeIPNotFound):
			srv.logger.Warn("rejected heartbeat for unknown peer IP",
				zap.String("peer_ip", peerIP),
			)

			return nil, status.Error(codes.PermissionDenied, "heartbeat peer is not registered")
		default:
			return nil, status.Errorf(codes.Internal, "failed to refresh heartbeat lease: %v", err)
		}
	}

	srv.logger.Info("refreshed heartbeat lease",
		zap.String("peer_ip", peerIP),
		zap.String("node_uuid", nodeUUID),
		zap.Time("last_heartbeat_at", leaseRecord.LastHeartbeatAt),
		zap.Time("lease_until", leaseRecord.LeaseUntil),
	)

	return &kms.Response{}, nil
}

// Unseal decrypts the incoming data.
func (srv *Server) Unseal(ctx context.Context, req *kms.Request) (_ *kms.Response, err error) {
	startedAt := time.Now()

	defer func() {
		srv.metrics.observeUnseal(err, time.Since(startedAt))
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Second):
	}

	validationErr := validateNodeUUID(req)
	if validationErr != nil {
		return nil, validationErr
	}

	leaseErr := srv.validateLease(req.NodeUuid)
	if leaseErr != nil {
		return nil, leaseErr
	}

	key, err := srv.getKey(ctx, req.NodeUuid)
	if err != nil {
		srv.logger.Error("failed to get key for node, using random key",
			zap.String("node_uuid", req.NodeUuid),
			zap.Error(err),
		)

		key, err = getRandomAESKey()
		if err != nil {
			return nil, err
		}
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := aesgcm.NonceSize()

	if len(req.Data) != aes.BlockSize+constants.PassphraseSize+nonceSize {
		return nil, status.Error(codes.InvalidArgument, "incorrect data length")
	}

	resp := &kms.Response{}

	decrypted, err := aesgcm.Open(nil, req.Data[:nonceSize], req.Data[nonceSize:], nil)
	if err != nil {
		srv.logger.Error("failed to authenticate the data, using random data",
			zap.String("node_uuid", req.NodeUuid),
			zap.Error(err),
		)

		resp.Data = make([]byte, constants.PassphraseSize)

		if _, err = io.ReadFull(rand.Reader, resp.Data); err != nil {
			return nil, err
		}

		return resp, nil
	}

	resp.Data = decrypted

	if srv.leaseEnabled {
		peerIP, peerErr := peerIPFromContext(ctx)
		if peerErr != nil {
			return nil, status.Errorf(codes.Unauthenticated, "failed to identify unseal peer: %v", peerErr)
		}

		leaseRecord, bootstrapErr := srv.leaseStore.Bootstrap(req.NodeUuid, peerIP, srv.now(), srv.leaseWindow)
		if bootstrapErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to persist heartbeat lease: %v", bootstrapErr)
		}

		srv.logger.Info("bootstrapped heartbeat lease after successful unseal",
			zap.String("peer_ip", peerIP),
			zap.String("node_uuid", req.NodeUuid),
			zap.Time("last_unseal_at", leaseRecord.LastUnsealAt),
			zap.Time("lease_until", leaseRecord.LeaseUntil),
		)
	}

	srv.logger.Debug("unsealed the data",
		zap.String("node_uuid", req.NodeUuid),
		zap.String("kcv", keyCheckValue(resp.Data, req.NodeUuid)),
	)

	return resp, nil
}

// getRandomAESKey generates random AES256 key.
func getRandomAESKey() ([]byte, error) {
	key := make([]byte, 32)

	_, err := rand.Read(key)
	if err != nil {
		return nil, err
	}

	return key, nil
}

func keyCheckValue(key []byte, data string) string {
	block, err := aes.NewCipher(key)
	if err != nil {
		return ""
	}

	hash := sha256.Sum256([]byte(data))

	encrypted := make([]byte, aes.BlockSize)
	block.Encrypt(encrypted, hash[:aes.BlockSize])

	return hex.EncodeToString(encrypted[:3])
}

func (srv *Server) validateLease(nodeUUID string) error {
	if !srv.leaseEnabled {
		return nil
	}

	leaseRecord, err := srv.leaseStore.Validate(nodeUUID, srv.now())
	if err == nil {
		return nil
	}

	if errors.Is(err, ErrLeaseBlocked) {
		srv.logger.Warn("denied unseal because node is blocked",
			zap.String("node_uuid", nodeUUID),
			zap.String("block_reason", leaseRecord.BlockReason),
			zap.Time("blocked_at", leaseRecord.BlockedAt),
		)

		return status.Error(codes.PermissionDenied, "node blocked")
	}

	if errors.Is(err, ErrLeaseNotFound) {
		srv.logger.Info("allowing first unseal for unregistered node",
			zap.String("node_uuid", nodeUUID),
		)

		return nil
	}

	if errors.Is(err, ErrLeaseExpired) {
		srv.logger.Warn("denied unseal because heartbeat lease expired",
			zap.String("node_uuid", nodeUUID),
			zap.Time("last_heartbeat_at", leaseRecord.LastHeartbeatAt),
			zap.Time("lease_until", leaseRecord.LeaseUntil),
		)

		return status.Error(codes.PermissionDenied, "heartbeat lease expired")
	}

	return status.Errorf(codes.Internal, "failed to validate heartbeat lease: %v", err)
}

func validateNodeUUID(req *kms.Request) error {
	if req.GetNodeUuid() == "" {
		return status.Error(codes.InvalidArgument, "node UUID is required")
	}

	return nil
}

func peerIPFromContext(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return "", fmt.Errorf("missing peer information")
	}

	host, _, err := net.SplitHostPort(p.Addr.String())
	if err == nil {
		return host, nil
	}

	ip := net.ParseIP(p.Addr.String())
	if ip == nil {
		return "", fmt.Errorf("failed to parse peer address %q", p.Addr.String())
	}

	return ip.String(), nil
}
