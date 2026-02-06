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
	"io"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/siderolabs/kms-client/api/kms"
	"github.com/siderolabs/kms-client/pkg/constants"
)

// Server implents gRPC API.
type Server struct {
	kms.UnimplementedKMSServiceServer

	logger *zap.Logger
	getKey func(context.Context, string) ([]byte, error)
}

// NewServer initializes new server.
func NewServer(logger *zap.Logger, keyHandler func(ctx context.Context, nodeUUID string) ([]byte, error)) *Server {
	return &Server{
		logger: logger,
		getKey: keyHandler,
	}
}

// Seal encrypts the incoming data.
func (srv *Server) Seal(ctx context.Context, req *kms.Request) (*kms.Response, error) {
	time.Sleep(time.Second)

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
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
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

// Unseal decrypts the incoming data.
func (srv *Server) Unseal(ctx context.Context, req *kms.Request) (*kms.Response, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Second):
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

		if _, err := io.ReadFull(rand.Reader, resp.Data); err != nil {
			return nil, err
		}

		return resp, nil
	}

	resp.Data = decrypted

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
