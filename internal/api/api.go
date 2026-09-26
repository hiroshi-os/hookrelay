package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hiroshi-os/hookrelay/internal/store"
)

// Server is the admin HTTP API.
type Server struct {
	Store      *store.Store
	AdminToken string
	Mux        *http.ServeMux
}

func New(st *store.Store, adminToken string) *Server {
	s := &Server{Store: st, AdminToken: adminToken, Mux: http.NewServeMux()}
	s.Mux.HandleFunc("GET /health", s.handleHealth)
	s.Mux.HandleFunc("POST /v1/endpoints", s.auth(s.handleCreateEndpoint))
	s.Mux.HandleFunc("POST /v1/endpoints/{id}/enable", s.auth(s.handleEnableEndpoint))
	s.Mux.HandleFunc("GET /v1/dead_letters", s.auth(s.handleListDeadLetters))
	s.Mux.HandleFunc("POST /v1/dead_letters/{id}/replay", s.auth(s.handleReplayDeadLetter))
	s.Mux.HandleFunc("POST /v1/events", s.auth(s.handleCreateEvent))
	s.Mux.HandleFunc("GET /v1/stats", s.auth(s.handleStats))
	return s
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.AdminToken == "" {
			next(w, r)
			return
		}
		h := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(h, prefix) || strings.TrimPrefix(h, prefix) != s.AdminToken {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type createEndpointReq struct {
	URL                  string `json:"url"`
	Secret               string `json:"secret"`
	MaxConcurrency       int    `json:"max_concurrency"`
	DisableAfterFailures int    `json:"disable_after_failures"`
}

func (s *Server) handleCreateEndpoint(w http.ResponseWriter, r *http.Request) {
	var req createEndpointReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
		return
	}
	if req.URL == "" || req.Secret == "" {
		http.Error(w, `{"error":"url and secret required"}`, http.StatusBadRequest)
		return
	}
	ep, err := s.Store.CreateEndpoint(r.Context(), req.URL, req.Secret, req.MaxConcurrency, req.DisableAfterFailures)
	if err != nil {
		http.Error(w, `{"error":"create failed"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":                     ep.ID.String(),
		"url":                    ep.URL,
		"enabled":                ep.Enabled,
		"max_concurrency":        ep.MaxConcurrency,
		"disable_after_failures": ep.DisableAfterFailures,
	})
}

func (s *Server) handleEnableEndpoint(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, `{"error":"bad id"}`, http.StatusBadRequest)
		return
	}
	ep, err := s.Store.EnableEndpoint(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, `{"error":"enable failed"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      ep.ID.String(),
		"enabled": ep.Enabled,
	})
}

func (s *Server) handleListDeadLetters(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListDeadLetters(r.Context(), 200)
	if err != nil {
		http.Error(w, `{"error":"list failed"}`, http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, dl := range list {
		m := map[string]any{
			"id":          dl.ID,
			"delivery_id": dl.DeliveryID,
			"event_id":    dl.EventID.String(),
			"endpoint_id": dl.EndpointID.String(),
			"attempts":    dl.Attempts,
			"last_error":  dl.LastError,
			"created_at":  dl.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		if dl.ReplayedAt != nil {
			m["replayed_at"] = dl.ReplayedAt.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"dead_letters": out})
}

func (s *Server) handleReplayDeadLetter(w http.ResponseWriter, r *http.Request) {
	var id int64
	if _, err := fmtSscanf(r.PathValue("id"), &id); err != nil {
		http.Error(w, `{"error":"bad id"}`, http.StatusBadRequest)
		return
	}
	if err := s.Store.ReplayDeadLetter(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, `{"error":"replay failed"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type createEventReq struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

func (s *Server) handleCreateEvent(w http.ResponseWriter, r *http.Request) {
	var req createEventReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
		return
	}
	if req.Type == "" {
		req.Type = "event"
	}
	if len(req.Payload) == 0 {
		req.Payload = json.RawMessage(`{}`)
	}
	ev, err := s.Store.InsertEvent(r.Context(), req.Type, req.Payload)
	if err != nil {
		http.Error(w, `{"error":"insert failed"}`, http.StatusInternalServerError)
		return
	}
	_ = s.Store.EnsureDeliveries(r.Context(), ev.ID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         ev.ID.String(),
		"type":       ev.Type,
		"created_at": ev.CreatedAt.UTC().Format(time.RFC3339Nano),
	})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	byStatus, err := s.Store.CountByStatus(r.Context())
	if err != nil {
		http.Error(w, `{"error":"stats failed"}`, http.StatusInternalServerError)
		return
	}
	dlq, _ := s.Store.CountDeadLetters(r.Context())
	events, _ := s.Store.CountEvents(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"events":       events,
		"deliveries":   byStatus,
		"dead_letters": dlq,
	})
}

func fmtSscanf(s string, id *int64) (int, error) {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("bad")
		}
		n = n*10 + int64(c-'0')
	}
	*id = n
	return 1, nil
}
