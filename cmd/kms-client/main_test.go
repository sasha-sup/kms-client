// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidateFlagsAllowsInsecureModeWithoutTLSFiles(t *testing.T) {
	t.Cleanup(resetClientFlags)

	clientFlags.heartbeatInterval = 30 * time.Second
	clientFlags.heartbeatTimeout = 5 * time.Second
	clientFlags.tlsEnable = false

	err := validateFlags()
	require.NoError(t, err)
}

func TestValidateFlagsRejectsTLSFilesWhenTLSDisabled(t *testing.T) {
	t.Cleanup(resetClientFlags)

	clientFlags.heartbeatInterval = 30 * time.Second
	clientFlags.heartbeatTimeout = 5 * time.Second
	clientFlags.tlsEnable = false
	clientFlags.tlsCAPath = "/tmp/ca.pem"

	err := validateFlags()
	require.EqualError(t, err, "TLS file flags require --tls-enable")
}

func TestValidateFlagsRequiresTLSMaterial(t *testing.T) {
	t.Cleanup(resetClientFlags)

	clientFlags.heartbeatInterval = 30 * time.Second
	clientFlags.heartbeatTimeout = 5 * time.Second
	clientFlags.tlsEnable = true

	err := validateFlags()
	require.EqualError(t, err, "--tls-ca-path is not set")
}

func TestValidateFlagsRejectsLargeTimeout(t *testing.T) {
	t.Cleanup(resetClientFlags)

	clientFlags.heartbeatInterval = 5 * time.Second
	clientFlags.heartbeatTimeout = 5 * time.Second
	clientFlags.tlsEnable = false

	err := validateFlags()
	require.EqualError(t, err, "--heartbeat-timeout must be less than --heartbeat-interval")
}

func resetClientFlags() {
	clientFlags = struct {
		kmsEndpoint       string
		tlsCAPath         string
		heartbeatInterval time.Duration
		heartbeatTimeout  time.Duration
		tlsEnable         bool
	}{}
}
