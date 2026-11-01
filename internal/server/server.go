package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/fah-dashboard/internal/config"
	"github.com/USA-RedDragon/fah-dashboard/internal/stats"
)

//go:embed web
var webFS embed.FS

const defTimeout = 5 * time.Second

// ViewFunc returns the latest stats. It returns nil before the first load.
type ViewFunc func() *stats.View

type Server struct {
	server *http.Server
	config *config.Config
}

func NewServer(cfg *config.Config, view ViewFunc) (*Server, error) {
	web, err := fs.Sub(webFS, "web")
	if err != nil {
		return nil, fmt.Errorf("open web assets: %w", err)
	}
	return &Server{
		server: &http.Server{
			Addr:              net.JoinHostPort(strings.Trim(cfg.HTTP.Bind, "[]"), strconv.Itoa(cfg.HTTP.Port)),
			ReadHeaderTimeout: defTimeout,
			WriteTimeout:      defTimeout,
			Handler:           newMux(web, view),
		},
		config: cfg,
	}, nil
}

func newMux(web fs.FS, view ViewFunc) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /api/overview", func(w http.ResponseWriter, _ *http.Request) {
		v := view()
		if v == nil {
			http.Error(w, "no stats yet", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, v.Overview)
	})
	mux.HandleFunc("GET /api/donors/{name}", func(w http.ResponseWriter, r *http.Request) {
		v := view()
		if v == nil {
			http.Error(w, "no stats yet", http.StatusServiceUnavailable)
			return
		}
		detail, ok := v.Detail(r.PathValue("name"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, detail)
	})
	mux.HandleFunc("GET /donor/{name}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, web, "donor.html")
	})
	mux.Handle("GET /", http.FileServerFS(web))
	return mux
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Warn("write response failed", "error", err)
	}
}

// Start listens and serves in the background.
func (s *Server) Start() error {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", s.server.Addr)
	if err != nil {
		return err
	}
	go func() {
		if err := s.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server error", "error", err.Error())
		}
	}()
	slog.Info("HTTP server started", "address", s.config.HTTP.Bind, "port", s.config.HTTP.Port)
	return nil
}

// Stop shuts the server down.
func (s *Server) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), defTimeout)
	defer cancel()
	return s.server.Shutdown(ctx)
}
