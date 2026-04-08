// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// HTTPHandler provides HTTP endpoints for heartbeat and admin operations.
type HTTPHandler struct {
	leaseStore    *LeaseStore
	hmacAuth      *HMACAuth
	adminToken    string
	logger        *zap.Logger
	metrics       *Metrics
	now           func() time.Time
	leaseDuration time.Duration
	heartbeatInterval time.Duration
}

// HTTPHandlerOptions configures the HTTP handler.
type HTTPHandlerOptions struct {
	LeaseStore        *LeaseStore
	HMACAuth          *HMACAuth
	AdminToken        string
	Logger            *zap.Logger
	Metrics           *Metrics
	Now               func() time.Time
	LeaseDuration     time.Duration
	HeartbeatInterval time.Duration
}

// NewHTTPHandler creates a new HTTP handler for heartbeat and admin endpoints.
func NewHTTPHandler(opts HTTPHandlerOptions) *HTTPHandler {
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &HTTPHandler{
		leaseStore:        opts.LeaseStore,
		hmacAuth:          opts.HMACAuth,
		adminToken:        opts.AdminToken,
		logger:            opts.Logger,
		metrics:           opts.Metrics,
		now:               now,
		leaseDuration:     opts.LeaseDuration,
		heartbeatInterval: opts.HeartbeatInterval,
	}
}

// RegisterRoutes registers HTTP routes on the provided mux.
func (h *HTTPHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /heartbeat", h.handleHeartbeat)
	mux.HandleFunc("GET /admin/nodes", h.handleListNodes)
	mux.HandleFunc("POST /admin/nodes/{uuid}/unblock", h.handleUnblockNode)
}

type heartbeatRequest struct {
	NodeUUID  string `json:"node_uuid"`
	NodeIP    string `json:"node_ip"`
	Timestamp int64  `json:"timestamp"`
}

type heartbeatResponse struct {
	Status          string `json:"status"`
	NextHeartbeatIn int    `json:"next_heartbeat_in"`
}

type errorResponse struct {
	Error  string `json:"error"`
	Reason string `json:"reason,omitempty"`
}

type nodeResponse struct {
	UUID          string     `json:"uuid"`
	IP            string     `json:"ip"`
	Status        string     `json:"status"`
	FirstSeen     *time.Time `json:"first_seen,omitempty"`
	LastHeartbeat *time.Time `json:"last_heartbeat,omitempty"`
	BlockedAt     *time.Time `json:"blocked_at,omitempty"`
	BlockReason   string     `json:"block_reason,omitempty"`
}

func (h *HTTPHandler) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req heartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid_request"})
		return
	}

	if req.NodeUUID == "" || req.NodeIP == "" || req.Timestamp == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "missing_fields"})
		return
	}

	if !ValidateTimestamp(req.Timestamp, h.now()) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "timestamp_out_of_range"})
		return
	}

	signature := r.Header.Get("X-HMAC-Signature")
	if signature == "" || !h.hmacAuth.Verify(req.NodeUUID, req.NodeIP, req.Timestamp, signature) {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "invalid_signature"})
		return
	}

	record, err := h.leaseStore.HeartbeatByUUID(req.NodeUUID, req.NodeIP, h.now(), h.leaseDuration)
	if err != nil {
		if err == ErrLeaseBlocked {
			h.logger.Warn("rejected HTTP heartbeat for blocked node",
				zap.String("node_uuid", req.NodeUUID),
				zap.String("node_ip", req.NodeIP),
				zap.String("block_reason", record.BlockReason),
			)

			writeJSON(w, http.StatusForbidden, errorResponse{
				Error:  "node_blocked",
				Reason: record.BlockReason,
			})

			return
		}

		h.logger.Error("failed to process HTTP heartbeat",
			zap.String("node_uuid", req.NodeUUID),
			zap.Error(err),
		)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal_error"})

		return
	}

	h.logger.Debug("HTTP heartbeat accepted",
		zap.String("node_uuid", req.NodeUUID),
		zap.String("node_ip", req.NodeIP),
		zap.Time("lease_until", record.LeaseUntil),
	)

	writeJSON(w, http.StatusOK, heartbeatResponse{
		Status:          "ok",
		NextHeartbeatIn: int(h.heartbeatInterval.Seconds()),
	})
}

func (h *HTTPHandler) handleListNodes(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateAdmin(r) {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "invalid_admin_token"})
		return
	}

	nodes := h.leaseStore.ListAll()
	result := make([]nodeResponse, 0, len(nodes))

	for uuid, record := range nodes {
		nr := nodeResponse{
			UUID:   uuid,
			IP:     record.NodeIP,
			Status: string(record.Status),
		}

		if !record.FirstSeen.IsZero() {
			t := record.FirstSeen
			nr.FirstSeen = &t
		}

		if !record.LastHeartbeatAt.IsZero() {
			t := record.LastHeartbeatAt
			nr.LastHeartbeat = &t
		}

		if !record.BlockedAt.IsZero() {
			t := record.BlockedAt
			nr.BlockedAt = &t
		}

		nr.BlockReason = record.BlockReason
		result = append(result, nr)
	}

	writeJSON(w, http.StatusOK, result)
}

func (h *HTTPHandler) handleUnblockNode(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateAdmin(r) {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "invalid_admin_token"})
		return
	}

	uuid := r.PathValue("uuid")
	if uuid == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "missing_uuid"})
		return
	}

	record, err := h.leaseStore.Unblock(uuid, h.now(), h.leaseDuration)
	if err != nil {
		if err == ErrLeaseNotFound {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "node_not_found"})
			return
		}

		h.logger.Error("failed to unblock node",
			zap.String("node_uuid", uuid),
			zap.Error(err),
		)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal_error"})

		return
	}

	h.logger.Info("node unblocked by admin",
		zap.String("node_uuid", uuid),
		zap.Time("lease_until", record.LeaseUntil),
	)

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "unblocked",
		"uuid":   uuid,
	})
}

func (h *HTTPHandler) authenticateAdmin(r *http.Request) bool {
	token := r.Header.Get("Authorization")
	token = strings.TrimPrefix(token, "Bearer ")

	return token != "" && token == h.adminToken
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
