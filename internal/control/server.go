package control

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

// Server exposes one process's shares on a Unix socket.
type Server struct {
	provider Provider
	logger   *slog.Logger
	listener net.Listener
	http     *http.Server
	path     string
}

// NewServer returns a control server for provider.
func NewServer(provider Provider, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Server{provider: provider, logger: logger}
}

// Start creates the socket and begins serving. A failure here is not fatal to
// sharing itself — the share still works, only `vrok list` cannot see it — so
// callers are expected to warn rather than abort.
func (s *Server) Start() error {
	dir, err := sessionDir()
	if err != nil {
		return err
	}
	// 0700: the socket can revoke shares, so only its owner may reach it.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("control: create %s: %w", dir, err)
	}

	socket, err := socketPath(os.Getpid())
	if err != nil {
		return err
	}
	// A socket left behind by a process that was killed would block the bind.
	os.Remove(socket)

	listener, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("control: listen on %s: %w", socket, err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		listener.Close()
		return fmt.Errorf("control: secure %s: %w", socket, err)
	}

	s.listener, s.path = listener, socket
	s.http = &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if err := s.http.Serve(listener); err != nil && err != http.ErrServerClosed {
			s.logger.Debug("control server stopped", slog.String("error", err.Error()))
		}
	}()
	return nil
}

// Close stops serving and removes the socket.
func (s *Server) Close() error {
	if s.http != nil {
		s.http.Close()
	}
	if s.path != "" {
		os.Remove(s.path)
	}
	return nil
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET "+pathShares, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, s.provider.Shares())
	})

	mux.HandleFunc("POST "+pathRevoke+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(path.Base(r.PathValue("id")))
		writeJSON(w, map[string]bool{"revoked": s.provider.Revoke(id)})
	})

	mux.HandleFunc("POST "+pathStop, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]bool{"stopping": true})
		// Stopping is deferred so this response reaches the caller before the
		// process starts tearing down its listeners.
		go s.provider.Stop()
	})

	return mux
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		http.Error(w, "encode failed", http.StatusInternalServerError)
	}
}
