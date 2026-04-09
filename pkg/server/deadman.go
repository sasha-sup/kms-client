// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package server

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// DeadManSwitch monitors node heartbeats and blocks nodes that miss their deadline.
type DeadManSwitch struct {
	leaseStore       *LeaseStore
	logger           *zap.Logger
	metrics          *Metrics
	now              func() time.Time
	checkInterval    time.Duration
	heartbeatTimeout time.Duration
}

// DeadManSwitchOptions configures the dead-man's switch.
type DeadManSwitchOptions struct {
	LeaseStore       *LeaseStore
	Logger           *zap.Logger
	Metrics          *Metrics
	Now              func() time.Time
	CheckInterval    time.Duration
	HeartbeatTimeout time.Duration
}

// NewDeadManSwitch creates a new dead-man's switch monitor.
func NewDeadManSwitch(opts DeadManSwitchOptions) *DeadManSwitch {
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &DeadManSwitch{
		leaseStore:       opts.LeaseStore,
		logger:           opts.Logger,
		metrics:          opts.Metrics,
		now:              now,
		checkInterval:    opts.CheckInterval,
		heartbeatTimeout: opts.HeartbeatTimeout,
	}
}

// Run starts the background monitoring loop. It blocks until the context is canceled.
func (d *DeadManSwitch) Run(ctx context.Context) error {
	d.logger.Info("dead-man's switch started",
		zap.Duration("check_interval", d.checkInterval),
		zap.Duration("heartbeat_timeout", d.heartbeatTimeout),
	)

	ticker := time.NewTicker(d.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.logger.Info("dead-man's switch stopped")

			return nil
		case <-ticker.C:
			d.Check()
		}
	}
}

// Check runs a single timeout check cycle. Exported for testing.
func (d *DeadManSwitch) Check() {
	blocked := d.leaseStore.BlockExpiredNodes(d.now(), d.heartbeatTimeout)
	if len(blocked) == 0 {
		return
	}

	d.metrics.incHeartbeatTimeouts(len(blocked))

	nodes := d.leaseStore.ListAll()

	for _, nodeUUID := range blocked {
		record := nodes[nodeUUID]
		d.logger.Warn("[SECURITY] node blocked: no heartbeat",
			zap.String("node_uuid", nodeUUID),
			zap.String("node_ip", record.NodeIP),
			zap.Duration("silence", d.now().Sub(record.LastHeartbeatAt)),
		)
	}
}
