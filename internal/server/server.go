package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/USA-RedDragon/http-logger/internal/config"
	"golang.org/x/sync/errgroup"
)

type Server struct {
	server  *http.Server
	stopped bool
	config  *config.Config
}

const defTimeout = 5 * time.Second

func logRequest(w http.ResponseWriter, r *http.Request) {
	// Build log attributes
	attrs := []any{
		"method", r.Method,
		"url", r.URL.String(),
		"remote_addr", r.RemoteAddr,
		"proto", r.Proto,
		"host", r.Host,
	}

	headers := []string{}

	// Add all headers
	for name, values := range r.Header {
		for _, value := range values {
			headers = append(headers, fmt.Sprintf("%s: %s", name, value))
		}
	}

	attrs = append(attrs, "headers", headers)

	// Read and add request body if present
	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			attrs = append(attrs, "body_read_error", err.Error())
		} else if len(body) > 0 {
			attrs = append(attrs, "body", string(body), "body_length", len(body))
		}
	}

	// Log everything in a single line
	slog.Info("HTTP Request", attrs...)

	// Return 200 OK with no body
	w.WriteHeader(http.StatusOK)
}

func NewServer(config *config.Config, _ string, _ string) (*Server, error) {
	return &Server{
		server: &http.Server{
			Addr:              fmt.Sprintf("%s:%d", config.HTTP.Bind, config.HTTP.Port),
			ReadHeaderTimeout: defTimeout,
			WriteTimeout:      defTimeout,
			Handler:           http.HandlerFunc(logRequest),
		},
		config: config,
	}, nil
}

func (s *Server) Start() error {
	waitGrp := sync.WaitGroup{}
	if s.server != nil {
		listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", s.server.Addr)
		if err != nil {
			return err
		}
		waitGrp.Add(1)
		go func() {
			defer waitGrp.Done()
			if err := s.server.Serve(listener); err != nil && !s.stopped {
				slog.Error("HTTP server error", "error", err.Error())
			}
		}()
	}
	slog.Info("HTTP server started", "address", s.config.HTTP.Bind, "port", s.config.HTTP.Port)

	go func() {
		waitGrp.Wait()
	}()
	return nil
}

func (s *Server) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s.stopped = true

	errGrp := errgroup.Group{}
	if s.server != nil {
		errGrp.Go(func() error {
			return s.server.Shutdown(ctx)
		})
	}

	return errGrp.Wait()
}
