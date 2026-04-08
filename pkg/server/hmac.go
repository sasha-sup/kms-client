// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// HMACAuth validates HMAC-SHA256 signatures for heartbeat requests.
type HMACAuth struct {
	key []byte
}

// NewHMACAuth creates an HMAC authenticator from a pre-shared key.
func NewHMACAuth(key string) *HMACAuth {
	return &HMACAuth{key: []byte(key)}
}

// Sign produces an HMAC-SHA256 hex signature over node_uuid + node_ip + timestamp.
func (a *HMACAuth) Sign(nodeUUID, nodeIP string, timestamp int64) string {
	rounded := roundTimestamp(timestamp)
	msg := fmt.Sprintf("%s%s%d", nodeUUID, nodeIP, rounded)

	mac := hmac.New(sha256.New, a.key)
	mac.Write([]byte(msg))

	return hex.EncodeToString(mac.Sum(nil))
}

// Verify checks the HMAC signature, tolerating a 30-second clock skew window.
func (a *HMACAuth) Verify(nodeUUID, nodeIP string, timestamp int64, signature string) bool {
	sigBytes, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}

	rounded := roundTimestamp(timestamp)

	// Check current window and the adjacent one to tolerate clock skew.
	for _, ts := range []int64{rounded, rounded - 30, rounded + 30} {
		msg := fmt.Sprintf("%s%s%d", nodeUUID, nodeIP, ts)

		mac := hmac.New(sha256.New, a.key)
		mac.Write([]byte(msg))

		if hmac.Equal(mac.Sum(nil), sigBytes) {
			return true
		}
	}

	return false
}

// roundTimestamp rounds a Unix timestamp to a 30-second window.
func roundTimestamp(ts int64) int64 {
	return ts / 30 * 30
}

// ValidateTimestamp checks that the provided timestamp is within an acceptable
// range of the current time (±60 seconds).
func ValidateTimestamp(timestamp int64, now time.Time) bool {
	diff := now.Unix() - timestamp
	if diff < 0 {
		diff = -diff
	}

	return diff <= 60
}
