// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package main is a simple reference implementation of the KMS server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/siderolabs/kms-client/api/kms"
	"github.com/siderolabs/kms-client/pkg/server"
)

var kmsFlags struct {
	apiEndpoint            string
	httpEndpoint           string
	keyPath                string
	leaseStorePath         string
	metricsEndpoint        string
	tlsCertPath            string
	tlsKeyPath             string
	heartbeatHMACKey       string
	adminToken             string
	heartbeatInterval      time.Duration
	heartbeatTimeout       time.Duration
	heartbeatCheckInterval time.Duration
	leaseDuration          time.Duration
	heartbeatEnable        bool
	tlsEnable              bool
}

func main() {
	flag.StringVar(&kmsFlags.apiEndpoint, "kms-api-endpoint", ":4050", "gRPC API endpoint for the KMS")
	flag.StringVar(&kmsFlags.httpEndpoint, "http-endpoint", ":4051", "HTTP API endpoint for heartbeat and admin")
	flag.BoolVar(&kmsFlags.heartbeatEnable, "heartbeat-enable", false, "whether to enforce dead-man-switch heartbeat leases")
	flag.StringVar(&kmsFlags.keyPath, "key-path", "", "encryption key path")
	flag.StringVar(&kmsFlags.metricsEndpoint, "metrics-endpoint", ":2112", "HTTP endpoint for Prometheus metrics, empty to disable")
	flag.BoolVar(&kmsFlags.tlsEnable, "tls-enable", false, "whether to enable tls or not")
	flag.StringVar(&kmsFlags.tlsCertPath, "tls-cert-path", "", "TLS certificate path")
	flag.StringVar(&kmsFlags.tlsKeyPath, "tls-key-path", "", "TLS key path")
	flag.StringVar(&kmsFlags.leaseStorePath, "lease-store-path", "", "path to the persistent heartbeat lease store")
	flag.DurationVar(&kmsFlags.heartbeatInterval, "heartbeat-interval", 30*time.Second, "expected node heartbeat interval")
	flag.DurationVar(&kmsFlags.heartbeatTimeout, "heartbeat-timeout", 120*time.Second, "seconds without heartbeat before blocking a node")
	flag.DurationVar(&kmsFlags.heartbeatCheckInterval, "heartbeat-check-interval", 30*time.Second, "how often the server checks for heartbeat timeouts")
	flag.DurationVar(&kmsFlags.leaseDuration, "lease-duration", 0, "node heartbeat lease duration")
	flag.Parse()

	if envKey := os.Getenv("HEARTBEAT_HMAC_KEY"); envKey != "" {
		kmsFlags.heartbeatHMACKey = envKey
	}

	if envToken := os.Getenv("ADMIN_TOKEN"); envToken != "" {
		kmsFlags.adminToken = envToken
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s", err)
	}
}

func run(ctx context.Context) error {
	logger, err := zap.NewProduction()
	if err != nil {
		return fmt.Errorf("failed to create logger: %w", err)
	}

	defer logger.Sync() //nolint:errcheck

	logger.Info("starting KMS server",
		zap.String("apiEndpoint", kmsFlags.apiEndpoint),
		zap.String("httpEndpoint", kmsFlags.httpEndpoint),
		zap.Bool("heartbeatEnable", kmsFlags.heartbeatEnable),
		zap.Duration("heartbeatInterval", kmsFlags.heartbeatInterval),
		zap.Duration("heartbeatTimeout", kmsFlags.heartbeatTimeout),
		zap.Duration("heartbeatCheckInterval", kmsFlags.heartbeatCheckInterval),
		zap.String("keyPath", kmsFlags.keyPath),
		zap.Duration("leaseDuration", kmsFlags.leaseDuration),
		zap.String("leaseStorePath", kmsFlags.leaseStorePath),
		zap.String("metricsEndpoint", kmsFlags.metricsEndpoint),
		zap.Bool("tlsEnable", kmsFlags.tlsEnable),
	)

	if kmsFlags.keyPath == "" {
		return fmt.Errorf("--key-path is not set")
	}

	key, err := os.ReadFile(kmsFlags.keyPath)
	if err != nil {
		return err
	}

	metricsRegistry := prometheus.NewRegistry()
	metricsRegistry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	metrics := server.NewMetrics(metricsRegistry)

	options, err := serverOptions(metrics)
	if err != nil {
		return err
	}

	srv := server.NewServer(logger, func(context.Context, string) ([]byte, error) { return key, nil }, options)

	grpcServer, err := newGRPCServer()
	if err != nil {
		return err
	}

	kms.RegisterKMSServiceServer(grpcServer, srv)

	lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", kmsFlags.apiEndpoint)
	if err != nil {
		return fmt.Errorf("error listening for gRPC API: %w", err)
	}

	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		return grpcServer.Serve(lis)
	})

	eg.Go(func() error {
		<-ctx.Done()

		grpcServer.Stop()

		return nil
	})

	if kmsFlags.metricsEndpoint != "" {
		metricsServer := &http.Server{
			Addr:              kmsFlags.metricsEndpoint,
			Handler:           promhttp.HandlerFor(metricsRegistry, promhttp.HandlerOpts{}),
			ReadHeaderTimeout: 5 * time.Second,
		}

		logger.Info("starting Prometheus metrics server",
			zap.String("metricsEndpoint", kmsFlags.metricsEndpoint),
		)

		metricsListener, metricsErr := (&net.ListenConfig{}).Listen(ctx, "tcp", kmsFlags.metricsEndpoint)
		if metricsErr != nil {
			return fmt.Errorf("error listening for metrics API: %w", metricsErr)
		}

		eg.Go(func() error {
			if serveErr := metricsServer.Serve(metricsListener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				return serveErr
			}

			return nil
		})

		eg.Go(func() error {
			<-ctx.Done()

			shutdownCtx, shutdownCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer shutdownCancel()

			return metricsServer.Shutdown(shutdownCtx)
		})
	}

	if kmsFlags.heartbeatEnable && kmsFlags.heartbeatHMACKey != "" && kmsFlags.adminToken != "" {
		httpHandler := server.NewHTTPHandler(server.HTTPHandlerOptions{
			LeaseStore:        options.LeaseStore,
			HMACAuth:          server.NewHMACAuth(kmsFlags.heartbeatHMACKey),
			AdminToken:        kmsFlags.adminToken,
			Logger:            logger,
			Metrics:           metrics,
			LeaseDuration:     kmsFlags.leaseDuration,
			HeartbeatInterval: kmsFlags.heartbeatInterval,
		})

		mux := http.NewServeMux()
		httpHandler.RegisterRoutes(mux)

		httpServer := &http.Server{
			Addr:              kmsFlags.httpEndpoint,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		}

		httpListener, httpErr := (&net.ListenConfig{}).Listen(ctx, "tcp", kmsFlags.httpEndpoint)
		if httpErr != nil {
			return fmt.Errorf("error listening for HTTP API: %w", httpErr)
		}

		logger.Info("starting HTTP API server",
			zap.String("httpEndpoint", kmsFlags.httpEndpoint),
		)

		eg.Go(func() error {
			if serveErr := httpServer.Serve(httpListener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				return serveErr
			}

			return nil
		})

		eg.Go(func() error {
			<-ctx.Done()

			shutdownCtx, shutdownCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer shutdownCancel()

			return httpServer.Shutdown(shutdownCtx)
		})

		dms := server.NewDeadManSwitch(server.DeadManSwitchOptions{
			LeaseStore:       options.LeaseStore,
			Logger:           logger,
			Metrics:          metrics,
			CheckInterval:    kmsFlags.heartbeatCheckInterval,
			HeartbeatTimeout: kmsFlags.heartbeatTimeout,
		})

		eg.Go(func() error {
			return dms.Run(ctx)
		})
	}

	if err = eg.Wait(); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return err
	}

	return nil
}

func newGRPCServer() (*grpc.Server, error) {
	if !kmsFlags.tlsEnable {
		return grpc.NewServer(), nil
	}

	creds, err := credentials.NewServerTLSFromFile(kmsFlags.tlsCertPath, kmsFlags.tlsKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create TLS credentials: %w", err)
	}

	return grpc.NewServer(grpc.Creds(creds)), nil
}

func serverOptions(metrics *server.Metrics) (server.Options, error) {
	if !kmsFlags.heartbeatEnable {
		if kmsFlags.leaseDuration > 0 || kmsFlags.leaseStorePath != "" {
			return server.Options{}, fmt.Errorf("--lease-duration and --lease-store-path require --heartbeat-enable")
		}

		return server.Options{
			Metrics: metrics,
		}, nil
	}

	if kmsFlags.leaseStorePath == "" {
		return server.Options{}, fmt.Errorf("--lease-store-path is required when heartbeat leases are enabled")
	}

	if kmsFlags.heartbeatInterval <= 0 {
		return server.Options{}, fmt.Errorf("--heartbeat-interval must be greater than zero when heartbeat leases are enabled")
	}

	if kmsFlags.leaseDuration <= 0 {
		return server.Options{}, fmt.Errorf("--lease-duration must be greater than zero when heartbeat leases are enabled")
	}

	if kmsFlags.leaseDuration <= kmsFlags.heartbeatInterval {
		return server.Options{}, fmt.Errorf("--lease-duration must be greater than --heartbeat-interval")
	}

	leaseStore, err := server.NewFileLeaseStore(kmsFlags.leaseStorePath, metrics)
	if err != nil {
		return server.Options{}, err
	}

	return server.Options{
		LeaseStore:    leaseStore,
		Metrics:       metrics,
		LeaseDuration: kmsFlags.leaseDuration,
	}, nil
}
