// Package server exposes TemporalDB over HTTP: TQL text in, JSON out
// (ADR-001 D8). TQL is the only wire protocol — there is nothing else to
// keep readable, and nothing else for the client, curl, and MCP to
// disagree about.
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/atvirokodosprendimai/temporaldbv1/internal/tql"
)

// Server wires chi routes over a tql.Executor.
type Server struct {
	Executor *tql.Executor
	router   chi.Router
}

// New builds a Server ready to be used as an http.Handler.
func New(executor *tql.Executor) *Server {
	s := &Server{Executor: executor}
	r := chi.NewRouter()
	r.Get("/healthz", s.handleHealthz)
	r.Post("/query", s.handleQuery)
	s.router = r
	return s
}

// ServeHTTP makes *Server an http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// queryResponse is the wire shape for POST /query (ADR-001 D8), mirrored
// by client.QueryResponse.
type queryResponse struct {
	Results []tql.Result `json:"results"`
	Error   string       `json:"error,omitempty"`
}

// handleQuery executes one or more newline-separated TQL statements from
// the request body. Statements run sequentially, each in its own
// transaction — not one shared transaction across the batch (ADR-001 D8,
// corrected during implementation). Execution stops at the first
// statement that errors; the results collected before that point are
// still returned alongside the error, so a caller can tell how far a
// script got.
func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("read body: %v", err))
		return
	}

	stmts, err := tql.ParseBatch(string(body))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(stmts) == 0 {
		writeError(w, http.StatusBadRequest, "empty request body")
		return
	}

	resp := queryResponse{Results: make([]tql.Result, 0, len(stmts))}
	for _, stmt := range stmts {
		res, err := s.Executor.Exec(r.Context(), stmt)
		if err != nil {
			resp.Error = err.Error()
			break
		}
		resp.Results = append(resp.Results, res)
	}

	writeJSON(w, http.StatusOK, resp)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, queryResponse{Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
