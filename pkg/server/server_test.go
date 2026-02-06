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
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/siderolabs/kms-client/api/kms"
	"github.com/siderolabs/kms-client/pkg/server"
)

func TestSealUnseal(t *testing.T) {
	t.Parallel()

	key, err := server.GetRandomAESKey()
	require.NoError(t, err)

	passphrase := make([]byte, 32)

	_, err = io.ReadFull(rand.Reader, passphrase)
	require.NoError(t, err)

	srv := server.NewServer(zaptest.NewLogger(t), func(_ context.Context, nodeUUID string) ([]byte, error) {
		if nodeUUID != "abcd" {
			return nil, fmt.Errorf("unknown node %s", nodeUUID)
		}

		return key, nil
	})

	ctx := t.Context()

	encrypted, err := srv.Seal(ctx, &kms.Request{
		NodeUuid: "abcd",
		Data:     passphrase,
	})
	require.NoError(t, err)
	require.NotEmpty(t, encrypted.Data)

	decrypted, err := srv.Unseal(ctx, &kms.Request{
		NodeUuid: "abcd",
		Data:     encrypted.Data,
	})
	require.NoError(t, err)
	require.Truef(t, bytes.Equal(passphrase, decrypted.Data), "expected %q to be equal to %q", passphrase, decrypted.Data)

	// wrong node uuid should not be able to decrypt the data
	decrypted, err = srv.Unseal(ctx, &kms.Request{
		NodeUuid: "abce",
		Data:     encrypted.Data,
	})
	require.NoError(t, err)
	require.Falsef(t, bytes.Equal(passphrase, decrypted.Data), "expected %q not to be equal to %q", passphrase, decrypted.Data)

	// randomly mutate the encrypted data, it should not be able to decrypt
	encrypted.Data[0] ^= 0xFF

	decrypted, err = srv.Unseal(ctx, &kms.Request{
		NodeUuid: "abcd",
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
		if nodeUUID != "abcd" {
			return nil, fmt.Errorf("unknown node %s", nodeUUID)
		}

		return key, nil
	})

	ctx := t.Context()

	_, err = srv.Seal(ctx, &kms.Request{
		NodeUuid: "abcd",
		Data:     passphrase,
	})

	require.Error(t, err)

	_, err = srv.Unseal(ctx, &kms.Request{
		NodeUuid: "abcd",
		Data:     make([]byte, 0),
	})

	require.Error(t, err)
}
