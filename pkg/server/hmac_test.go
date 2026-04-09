// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package server_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/siderolabs/kms-client/pkg/server"
)

func TestHMACSignAndVerify(t *testing.T) {
	t.Parallel()

	auth := server.NewHMACAuth("test-key")

	ts := time.Unix(1_700_000_000, 0).Unix()
	sig := auth.Sign("node-1", "10.0.0.1", ts)

	require.True(t, auth.Verify("node-1", "10.0.0.1", ts, sig))
}

func TestHMACRejectsWrongKey(t *testing.T) {
	t.Parallel()

	auth1 := server.NewHMACAuth("key-1")
	auth2 := server.NewHMACAuth("key-2")

	ts := time.Unix(1_700_000_000, 0).Unix()
	sig := auth1.Sign("node-1", "10.0.0.1", ts)

	require.False(t, auth2.Verify("node-1", "10.0.0.1", ts, sig))
}

func TestHMACToleratesClockSkew(t *testing.T) {
	t.Parallel()

	auth := server.NewHMACAuth("test-key")

	ts := int64(1_700_000_000)
	sig := auth.Sign("node-1", "10.0.0.1", ts)

	// Verify with a timestamp 25 seconds off — should still match
	// because both round to the same 30-second window.
	require.True(t, auth.Verify("node-1", "10.0.0.1", ts+25, sig))

	// Verify with a timestamp in the adjacent window — should still pass
	// because we check ±30s windows.
	require.True(t, auth.Verify("node-1", "10.0.0.1", ts+35, sig))
}

func TestHMACRejectsTamperedData(t *testing.T) {
	t.Parallel()

	auth := server.NewHMACAuth("test-key")

	ts := time.Unix(1_700_000_000, 0).Unix()
	sig := auth.Sign("node-1", "10.0.0.1", ts)

	require.False(t, auth.Verify("node-2", "10.0.0.1", ts, sig))
	require.False(t, auth.Verify("node-1", "10.0.0.2", ts, sig))
}

func TestHMACIdentifySignAndVerify(t *testing.T) {
	t.Parallel()

	auth := server.NewHMACAuth("test-key")

	ts := time.Unix(1_700_000_000, 0).Unix()
	sig := auth.SignIdentify("10.0.0.1", ts)

	require.True(t, auth.VerifyIdentify("10.0.0.1", ts, sig))
	require.False(t, auth.VerifyIdentify("10.0.0.2", ts, sig))
}

func TestHMACIdentifyRejectsWrongKey(t *testing.T) {
	t.Parallel()

	auth1 := server.NewHMACAuth("key-1")
	auth2 := server.NewHMACAuth("key-2")

	ts := time.Unix(1_700_000_000, 0).Unix()
	sig := auth1.SignIdentify("10.0.0.1", ts)

	require.False(t, auth2.VerifyIdentify("10.0.0.1", ts, sig))
}

func TestValidateTimestamp(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)

	require.True(t, server.ValidateTimestamp(now.Unix(), now))
	require.True(t, server.ValidateTimestamp(now.Unix()-60, now))
	require.True(t, server.ValidateTimestamp(now.Unix()+60, now))
	require.False(t, server.ValidateTimestamp(now.Unix()-61, now))
	require.False(t, server.ValidateTimestamp(now.Unix()+61, now))
}
