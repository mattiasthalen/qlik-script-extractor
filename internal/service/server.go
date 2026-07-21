package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// Config configures the HTTP service.
type Config struct {
	// APIKey, when non-empty, is required on /parse via APIKeyHeader.
	APIKey string
	// APIKeyHeader is the header carrying the API key (default "X-API-Key").
	APIKeyHeader string
	// OutputBucket is the default output prefix (gs:// or local dir) used by the
	// event handler and by /parse when the request omits an output.
	OutputBucket string
	// TmpDir is where remote inputs are streamed before mmap parsing.
	TmpDir string
	// Logger is the structured logger; a default is used when nil.
	Logger *slog.Logger
}

// Server is the HTTP service.
type Server struct {
	cfg        Config
	store      *Storage
	log        *slog.Logger
	documenter Documenter
}

// NewServer builds a Server. documenter may be nil to disable the AI stage.
func NewServer(cfg Config, store *Storage, documenter Documenter) *Server {
	if cfg.APIKeyHeader == "" {
		cfg.APIKeyHeader = "X-API-Key"
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	if cfg.APIKey == "" {
		log.Warn("API key not configured: /parse is unauthenticated")
	}
	return &Server{cfg: cfg, store: store, log: log, documenter: documenter}
}

// Handler returns the HTTP handler with all routes and middleware wired up.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.Handle("POST /parse", s.requireAPIKey(http.HandlerFunc(s.handleParse)))
	// Event mode (Eventarc/CloudEvent GCS object-finalize). Protected by Cloud
	// Run IAM rather than the API key, since Eventarc cannot mint the header.
	mux.HandleFunc("POST /events", s.handleEvent)
	return s.logRequests(mux)
}

// Run starts the server and blocks until ctx is cancelled, then shuts down
// gracefully.
func (s *Server) Run(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("qvf documentation service listening", "addr", addr, "schemaVersion", SchemaVersion)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		s.log.Info("shutting down")
		return srv.Shutdown(shutdownCtx)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":        "ok",
		"service":       "qvf-documentation",
		"schemaVersion": SchemaVersion,
	})
}

func (s *Server) handleParse(w http.ResponseWriter, r *http.Request) {
	var req ParseRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	resp, err := s.process(r.Context(), req, "")
	if err != nil {
		s.log.ErrorContext(r.Context(), "parse failed", "source", req.Source, "error", err)
		s.writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// requireAPIKey enforces the configured API key. When no key is configured the
// middleware is a pass-through (a startup warning is logged).
func (s *Server) requireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APIKey != "" {
			got := r.Header.Get(s.cfg.APIKeyHeader)
			if !constantTimeEqual(got, s.cfg.APIKey) {
				s.writeError(w, http.StatusUnauthorized, "missing or invalid API key")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"durationMs", time.Since(start).Milliseconds(),
		)
	})
}

func (s *Server) writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func marshalIndent(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}
