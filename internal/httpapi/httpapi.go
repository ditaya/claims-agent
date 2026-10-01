// Package httpapi exposes the triage agent over HTTP: POST /triage.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"claims-agent/internal/agent"
)

// Triager is the one method httpapi needs from agent.Agent - narrowed to an
// interface so handler tests can use a fake instead of a real agent (and,
// transitively, a real Claude client).
type Triager interface {
	Triage(ctx context.Context, claimText string) (agent.Decision, error)
}

// NewMux builds the HTTP routes for the claims agent, backed by triager.
func NewMux(triager Triager) *http.ServeMux {
	mux := http.NewServeMux()
	h := &handler{triager: triager}
	mux.HandleFunc("POST /triage", h.triage)
	return mux
}

type handler struct {
	triager Triager
}

type triageRequest struct {
	Claim string `json:"claim"`
}

func (h *handler) triage(w http.ResponseWriter, r *http.Request) {
	var req triageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Claim) == "" {
		writeError(w, http.StatusBadRequest, `"claim" is required`)
		return
	}

	decision, err := h.triager.Triage(r.Context(), req.Claim)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			writeError(w, http.StatusGatewayTimeout, "triage timed out")
			return
		}
		log.Printf("httpapi: triage failed: %v", err)
		writeError(w, http.StatusInternalServerError, "triage failed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(decision); err != nil {
		log.Printf("httpapi: failed to encode response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
