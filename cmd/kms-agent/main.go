// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package main implements an HTTP heartbeat agent for the KMS dead-man's switch.
// It runs as a Kubernetes DaemonSet and periodically sends signed heartbeat
// requests to the KMS server.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/signal"
	"time"

	"go.uber.org/zap"

	"github.com/siderolabs/kms-client/pkg/server"
)

type agentConfig struct {
	nodeUUID          string
	nodeIP            string
	kmsServerURL      string
	hmacKey           string
	heartbeatInterval time.Duration
	heartbeatTimeout  time.Duration
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)

	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		cancel()
		os.Exit(1)
	}

	cancel()
}

func run(ctx context.Context) error {
	logger, err := zap.NewProduction()
	if err != nil {
		return fmt.Errorf("failed to create logger: %w", err)
	}

	defer logger.Sync() //nolint:errcheck

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	logger.Info("starting KMS heartbeat agent",
		zap.String("node_uuid", cfg.nodeUUID),
		zap.String("node_ip", cfg.nodeIP),
		zap.String("kms_server_url", cfg.kmsServerURL),
		zap.Duration("heartbeat_interval", cfg.heartbeatInterval),
	)

	hmacAuth := server.NewHMACAuth(cfg.hmacKey)

	client := &http.Client{
		Timeout: cfg.heartbeatTimeout,
	}

	if cfg.nodeUUID == "" {
		logger.Info("NODE_UUID not set, discovering via /node/identify")

		uuid, err := identifyLoop(ctx, logger, client, hmacAuth, cfg)
		if err != nil {
			return err
		}

		cfg.nodeUUID = uuid

		logger.Info("node UUID discovered",
			zap.String("node_uuid", cfg.nodeUUID),
		)
	}

	return heartbeatLoop(ctx, logger, client, hmacAuth, cfg)
}

func loadConfig() (agentConfig, error) {
	cfg := agentConfig{
		nodeUUID:          os.Getenv("NODE_UUID"),
		nodeIP:            os.Getenv("NODE_IP"),
		kmsServerURL:      os.Getenv("KMS_SERVER_URL"),
		hmacKey:           os.Getenv("HEARTBEAT_HMAC_KEY"),
		heartbeatInterval: 30 * time.Second,
		heartbeatTimeout:  5 * time.Second,
	}

	if cfg.nodeIP == "" {
		return agentConfig{}, fmt.Errorf("NODE_IP environment variable is required")
	}

	if cfg.kmsServerURL == "" {
		return agentConfig{}, fmt.Errorf("KMS_SERVER_URL environment variable is required")
	}

	if cfg.hmacKey == "" {
		return agentConfig{}, fmt.Errorf("HEARTBEAT_HMAC_KEY environment variable is required")
	}

	if envInterval := os.Getenv("HEARTBEAT_INTERVAL"); envInterval != "" {
		d, err := time.ParseDuration(envInterval)
		if err != nil {
			return agentConfig{}, fmt.Errorf("invalid HEARTBEAT_INTERVAL: %w", err)
		}

		cfg.heartbeatInterval = d
	}

	if envTimeout := os.Getenv("HEARTBEAT_TIMEOUT"); envTimeout != "" {
		d, err := time.ParseDuration(envTimeout)
		if err != nil {
			return agentConfig{}, fmt.Errorf("invalid HEARTBEAT_TIMEOUT: %w", err)
		}

		cfg.heartbeatTimeout = d
	}

	return cfg, nil
}

func heartbeatLoop(ctx context.Context, logger *zap.Logger, client *http.Client, hmacAuth *server.HMACAuth, cfg agentConfig) error {
	consecutiveFailures := 0

	if err := sendHeartbeat(ctx, client, hmacAuth, cfg); err != nil {
		logger.Warn("initial heartbeat failed, will retry",
			zap.Error(err),
		)

		consecutiveFailures++
	} else {
		logger.Info("initial heartbeat accepted")
	}

	ticker := time.NewTicker(cfg.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("shutting down heartbeat agent")

			return nil
		case <-ticker.C:
		}

		err := sendHeartbeat(ctx, client, hmacAuth, cfg)
		if err == nil {
			if consecutiveFailures > 0 {
				logger.Info("heartbeat recovered after failures",
					zap.Int("previous_failures", consecutiveFailures),
				)
			}

			consecutiveFailures = 0

			logger.Debug("heartbeat accepted")

			continue
		}

		consecutiveFailures++
		backoff := backoffDuration(consecutiveFailures, cfg.heartbeatInterval)

		logger.Warn("heartbeat failed",
			zap.Error(err),
			zap.Int("consecutive_failures", consecutiveFailures),
			zap.Duration("next_retry_in", backoff),
		)

		// If the error is a terminal 403, log and continue — the admin
		// must unblock the node, but the agent keeps trying.
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
	}
}

type heartbeatRequest struct {
	NodeUUID  string `json:"node_uuid"`
	NodeIP    string `json:"node_ip"`
	Timestamp int64  `json:"timestamp"`
}

func sendHeartbeat(ctx context.Context, client *http.Client, hmacAuth *server.HMACAuth, cfg agentConfig) (err error) {
	timestamp := time.Now().Unix()

	reqBody := heartbeatRequest{
		NodeUUID:  cfg.nodeUUID,
		NodeIP:    cfg.nodeIP,
		Timestamp: timestamp,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal heartbeat request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.kmsServerURL+"/heartbeat", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create heartbeat request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hmac-Signature", hmacAuth.Sign(cfg.nodeUUID, cfg.nodeIP, timestamp))

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("heartbeat request failed: %w", err)
	}

	defer func() {
		closeErr := resp.Body.Close()
		if err == nil && closeErr != nil {
			err = fmt.Errorf("failed to close heartbeat response body: %w", closeErr)
		}
	}()

	if resp.StatusCode == http.StatusOK {
		return nil
	}

	return fmt.Errorf("heartbeat rejected with status %d", resp.StatusCode)
}

type identifyResponse struct {
	NodeUUID string `json:"node_uuid"`
	Status   string `json:"status"`
}

func identifyLoop(ctx context.Context, logger *zap.Logger, client *http.Client, hmacAuth *server.HMACAuth, cfg agentConfig) (string, error) {
	consecutiveFailures := 0

	for {
		uuid, err := sendIdentify(ctx, client, hmacAuth, cfg)
		if err == nil {
			return uuid, nil
		}

		consecutiveFailures++
		backoff := backoffDuration(consecutiveFailures, cfg.heartbeatInterval)

		logger.Warn("identify failed, retrying",
			zap.Error(err),
			zap.Int("consecutive_failures", consecutiveFailures),
			zap.Duration("retry_in", backoff),
		)

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(backoff):
		}
	}
}

func sendIdentify(ctx context.Context, client *http.Client, hmacAuth *server.HMACAuth, cfg agentConfig) (nodeUUID string, err error) {
	timestamp := time.Now().Unix()

	reqBody := struct {
		NodeIP    string `json:"node_ip"`
		Timestamp int64  `json:"timestamp"`
	}{
		NodeIP:    cfg.nodeIP,
		Timestamp: timestamp,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal identify request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.kmsServerURL+"/node/identify", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to create identify request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hmac-Signature", hmacAuth.SignIdentify(cfg.nodeIP, timestamp))

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("identify request failed: %w", err)
	}

	defer func() {
		closeErr := resp.Body.Close()
		if err == nil && closeErr != nil {
			err = fmt.Errorf("failed to close identify response body: %w", closeErr)
		}
	}()

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("node not yet registered (waiting for first Unseal)")
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("identify rejected with status %d", resp.StatusCode)
	}

	var identResp identifyResponse
	if err = json.NewDecoder(resp.Body).Decode(&identResp); err != nil {
		return "", fmt.Errorf("failed to decode identify response: %w", err)
	}

	if identResp.NodeUUID == "" {
		return "", fmt.Errorf("server returned empty node_uuid")
	}

	return identResp.NodeUUID, nil
}

func backoffDuration(failures int, baseInterval time.Duration) time.Duration {
	if failures <= 1 {
		return baseInterval
	}

	multiplier := math.Pow(2, float64(failures-1))
	backoff := time.Duration(float64(baseInterval) * multiplier)

	maxBackoff := 5 * time.Minute
	if backoff > maxBackoff {
		backoff = maxBackoff
	}

	return backoff
}
