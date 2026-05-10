package idem

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

type Server struct{ Store *Store }

func NewServer(store *Store) *Server {
	if store == nil {
		store = DefaultStore()
	}
	return &Server{Store: store}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/records", s.handleRecords)
	mux.HandleFunc("/records/", s.handleRecordItem)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		sendJSON(w, 404, map[string]any{"error": "not found", "code": "route_not_found"})
	})
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendMethod(w)
		return
	}
	sendJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleRecords(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.Store.ListRecords(r.URL.Query().Get("status"), r.URL.Query().Get("scope"))
		if err != nil {
			s.handleError(w, err)
			return
		}
		sendJSON(w, 200, map[string]any{"records": items})
	case http.MethodPost:
		var in BeginInput
		if !decodeJSON(w, r, &in) {
			return
		}
		rec, outcome, err := s.Store.BeginRecord(in)
		if err != nil {
			s.handleError(w, err)
			return
		}
		status := http.StatusOK
		if outcome == BeginOutcomeStarted {
			status = http.StatusCreated
		}
		sendJSON(w, status, map[string]any{"outcome": outcome, "record": rec})
	default:
		sendMethod(w)
	}
}

func (s *Server) handleRecordItem(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/records/"), "/")
	scope := r.URL.Query().Get("scope")
	if len(parts) == 1 && parts[0] != "" {
		key := parts[0]
		switch r.Method {
		case http.MethodGet:
			rec, err := s.Store.GetRecord(key, scope)
			if err != nil {
				s.handleError(w, err)
				return
			}
			sendJSON(w, 200, map[string]any{"record": rec})
		case http.MethodDelete:
			if err := s.Store.DeleteRecord(key, scope); err != nil {
				s.handleError(w, err)
				return
			}
			w.WriteHeader(204)
		default:
			sendMethod(w)
		}
		return
	}
	if len(parts) == 2 && (parts[1] == "complete" || parts[1] == "fail") {
		if r.Method != http.MethodPost {
			sendMethod(w)
			return
		}
		var in CompleteInput
		if !decodeJSON(w, r, &in) {
			return
		}
		var rec Record
		var err error
		if parts[1] == "complete" {
			rec, err = s.Store.CompleteRecord(parts[0], scope, in)
		} else {
			rec, err = s.Store.FailRecord(parts[0], scope, in)
		}
		if err != nil {
			s.handleError(w, err)
			return
		}
		sendJSON(w, 200, map[string]any{"record": rec})
		return
	}
	sendJSON(w, 404, map[string]any{"error": "not found", "code": "route_not_found"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendMethod(w)
		return
	}
	sendJSON(w, 200, map[string]any{"metrics": s.Store.Metrics()})
}

func (s *Server) handleError(w http.ResponseWriter, err error) {
	var ve ValidationError
	if errors.As(err, &ve) {
		sendJSON(w, 400, ve)
		return
	}
	var ce ConflictError
	if errors.As(err, &ce) {
		sendJSON(w, 409, ce)
		return
	}
	var ge GoneError
	if errors.As(err, &ge) {
		sendJSON(w, 410, ge)
		return
	}
	if errors.Is(err, ErrNotFound) {
		sendJSON(w, 404, map[string]any{"error": "record not found", "code": "record_not_found"})
		return
	}
	sendJSON(w, 500, map[string]any{"error": "internal server error", "code": "internal_error"})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		sendJSON(w, 400, map[string]any{"error": "invalid json", "code": "invalid_json"})
		return false
	}
	return true
}

func sendMethod(w http.ResponseWriter) {
	sendJSON(w, 405, map[string]any{"error": "method not allowed", "code": "method_not_allowed"})
}

func sendJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}
