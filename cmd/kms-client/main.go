// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package main implements a heartbeat client for the KMS server.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/siderolabs/kms-client/api/kms"
)

var clientFlags struct {
	kmsEndpoint       string
	tlsCAPath         string
	heartbeatInterval time.Duration
	heartbeatTimeout  time.Duration
	tlsEnable         bool
}

func main() {
	flag.StringVar(&clientFlags.kmsEndpoint, "kms-endpoint", "127.0.0.1:4050", "gRPC endpoint for the KMS server")
	flag.BoolVar(&clientFlags.tlsEnable, "tls-enable", true, "whether to enable TLS for the KMS connection")
	flag.StringVar(&clientFlags.tlsCAPath, "tls-ca-path", "", "CA bundle path for verifying the KMS server")
	flag.DurationVar(&clientFlags.heartbeatInterval, "heartbeat-interval", 30*time.Second, "interval between heartbeat requests")
	flag.DurationVar(&clientFlags.heartbeatTimeout, "heartbeat-timeout", 5*time.Second, "timeout for a single heartbeat request")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s", err)
	}
}

func run(ctx context.Context) error {
	if err := validateFlags(); err != nil {
		return err
	}

	logger, err := zap.NewProduction()
	if err != nil {
		return fmt.Errorf("failed to create logger: %w", err)
	}

	defer logger.Sync() //nolint:errcheck

	logger.Info("starting KMS heartbeat client",
		zap.String("kmsEndpoint", clientFlags.kmsEndpoint),
		zap.Duration("heartbeatInterval", clientFlags.heartbeatInterval),
		zap.Duration("heartbeatTimeout", clientFlags.heartbeatTimeout),
		zap.Bool("tlsEnable", clientFlags.tlsEnable),
	)

	transportCredentials, err := transportCredentials()
	if err != nil {
		return err
	}

	conn, err := grpc.NewClient(
		clientFlags.kmsEndpoint,
		grpc.WithTransportCredentials(transportCredentials),
	)
	if err != nil {
		return fmt.Errorf("failed to dial KMS: %w", err)
	}

	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			logger.Warn("failed to close KMS connection",
				zap.Error(closeErr),
			)
		}
	}()

	client := kms.NewKMSServiceClient(conn)

	if err = heartbeatLoop(ctx, logger, client); err != nil {
		return err
	}

	return nil
}

func heartbeatLoop(ctx context.Context, logger *zap.Logger, client kms.KMSServiceClient) error {
	if err := sendHeartbeat(ctx, client); err != nil {
		return classifyHeartbeatError(err)
	}

	logger.Info("heartbeat accepted")

	ticker := time.NewTicker(clientFlags.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		err := sendHeartbeat(ctx, client)
		if err == nil {
			logger.Debug("heartbeat accepted")

			continue
		}

		if classifiedErr := classifyHeartbeatError(err); classifiedErr != nil {
			return classifiedErr
		}

		logger.Warn("heartbeat request failed",
			zap.Error(err),
		)
	}
}

func sendHeartbeat(ctx context.Context, client kms.KMSServiceClient) error {
	heartbeatCtx, cancel := context.WithTimeout(ctx, clientFlags.heartbeatTimeout)
	defer cancel()

	_, err := client.Heartbeat(heartbeatCtx, &kms.Request{})

	return err
}

func classifyHeartbeatError(err error) error {
	code := status.Code(err)

	if code == codes.OK {
		return nil
	}

	if code == codes.PermissionDenied || code == codes.Unauthenticated || code == codes.FailedPrecondition || code == codes.InvalidArgument {
		return fmt.Errorf("heartbeat rejected by KMS: %w", err)
	}

	return nil
}

func validateFlags() error {
	if clientFlags.heartbeatInterval <= 0 {
		return fmt.Errorf("--heartbeat-interval must be greater than zero")
	}

	if clientFlags.heartbeatTimeout <= 0 {
		return fmt.Errorf("--heartbeat-timeout must be greater than zero")
	}

	if clientFlags.heartbeatTimeout >= clientFlags.heartbeatInterval {
		return fmt.Errorf("--heartbeat-timeout must be less than --heartbeat-interval")
	}

	if !clientFlags.tlsEnable {
		if clientFlags.tlsCAPath != "" {
			return fmt.Errorf("TLS file flags require --tls-enable")
		}

		return nil
	}

	if clientFlags.tlsCAPath == "" {
		return fmt.Errorf("--tls-ca-path is not set")
	}

	return nil
}

func transportCredentials() (credentials.TransportCredentials, error) {
	if !clientFlags.tlsEnable {
		return insecure.NewCredentials(), nil
	}

	caBundle, err := os.ReadFile(clientFlags.tlsCAPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA bundle: %w", err)
	}

	rootCAs := x509.NewCertPool()
	if !rootCAs.AppendCertsFromPEM(caBundle) {
		return nil, fmt.Errorf("failed to parse CA bundle")
	}

	return credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    rootCAs,
	}), nil
}
