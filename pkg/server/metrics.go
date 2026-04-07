// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package server

import (
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	requestResultDenied = "denied"
	requestResultError  = "error"
)

// Metrics provides Prometheus metrics for the KMS server.
type Metrics struct {
	unsealRequests   *prometheus.CounterVec
	heartbeatRequest *prometheus.CounterVec
	requestDuration  *prometheus.HistogramVec
	activeLeases     prometheus.Gauge
	expiredLeases    prometheus.Gauge
	leaseStoreErrors *prometheus.CounterVec
}

// NewMetrics initializes KMS Prometheus metrics.
func NewMetrics(registerer prometheus.Registerer) *Metrics {
	metrics := &Metrics{
		unsealRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kms_unseal_requests_total",
			Help: "Total number of KMS unseal requests grouped by result and reason.",
		}, []string{"result", "reason"}),
		heartbeatRequest: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kms_heartbeat_requests_total",
			Help: "Total number of KMS heartbeat requests grouped by result and reason.",
		}, []string{"result", "reason"}),
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "kms_request_duration_seconds",
			Help:    "KMS request duration in seconds grouped by method.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method"}),
		activeLeases: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kms_active_leases",
			Help: "Current number of active heartbeat leases.",
		}),
		expiredLeases: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kms_expired_leases",
			Help: "Current number of expired heartbeat leases.",
		}),
		leaseStoreErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kms_lease_store_errors_total",
			Help: "Total number of lease store persistence errors grouped by operation.",
		}, []string{"operation"}),
	}

	registerer.MustRegister(
		metrics.unsealRequests,
		metrics.heartbeatRequest,
		metrics.requestDuration,
		metrics.activeLeases,
		metrics.expiredLeases,
		metrics.leaseStoreErrors,
	)

	return metrics
}

func (metrics *Metrics) observeUnseal(err error, duration time.Duration) {
	if metrics == nil {
		return
	}

	result, reason := requestLabels(err)
	metrics.unsealRequests.WithLabelValues(result, reason).Inc()
	metrics.requestDuration.WithLabelValues("unseal").Observe(duration.Seconds())
}

func (metrics *Metrics) observeHeartbeat(err error, duration time.Duration) {
	if metrics == nil {
		return
	}

	result, reason := requestLabels(err)
	metrics.heartbeatRequest.WithLabelValues(result, reason).Inc()
	metrics.requestDuration.WithLabelValues("heartbeat").Observe(duration.Seconds())
}

func (metrics *Metrics) setLeaseCounts(active, expired int) {
	if metrics == nil {
		return
	}

	metrics.activeLeases.Set(float64(active))
	metrics.expiredLeases.Set(float64(expired))
}

func (metrics *Metrics) incLeaseStoreError(operation string) {
	if metrics == nil {
		return
	}

	metrics.leaseStoreErrors.WithLabelValues(operation).Inc()
}

func requestLabels(err error) (string, string) {
	if err == nil {
		return "success", "ok"
	}

	st, ok := status.FromError(err)
	if !ok {
		return requestResultError, "internal"
	}

	if st.Code() == codes.PermissionDenied {
		switch {
		case strings.Contains(st.Message(), "expired"):
			return requestResultDenied, "lease_expired"
		case strings.Contains(st.Message(), "missing"):
			return requestResultDenied, "lease_missing"
		case strings.Contains(st.Message(), "registered"):
			return requestResultDenied, "unknown_peer_ip"
		default:
			return requestResultDenied, "permission_denied"
		}
	}

	if st.Code() == codes.InvalidArgument {
		return requestResultError, "invalid_argument"
	}

	if st.Code() == codes.Unauthenticated {
		return requestResultDenied, "unauthenticated"
	}

	if st.Code() == codes.FailedPrecondition {
		return requestResultDenied, "failed_precondition"
	}

	return requestResultError, strings.ToLower(st.Code().String())
}
