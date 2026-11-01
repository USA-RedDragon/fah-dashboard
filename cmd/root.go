package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/USA-RedDragon/configulator/v2"
	"github.com/USA-RedDragon/fah-dashboard/internal/collector"
	"github.com/USA-RedDragon/fah-dashboard/internal/config"
	"github.com/USA-RedDragon/fah-dashboard/internal/fah"
	"github.com/USA-RedDragon/fah-dashboard/internal/roster"
	"github.com/USA-RedDragon/fah-dashboard/internal/server"
	"github.com/USA-RedDragon/fah-dashboard/internal/stats"
	"github.com/USA-RedDragon/fah-dashboard/internal/store"
	"github.com/lmittmann/tint"
	"github.com/spf13/cobra"
	"github.com/ztrue/shutdown"
)

func NewCommand(version, commit string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "fah-dashboard",
		Version: fmt.Sprintf("%s - %s", version, commit),
		Annotations: map[string]string{
			"version": version,
			"commit":  commit,
		},
		RunE:              runRoot,
		SilenceErrors:     true,
		DisableAutoGenTag: true,
	}
	return cmd
}

func runRoot(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	c, err := configulator.FromContext[config.Config](ctx)
	if err != nil {
		return fmt.Errorf("failed to get config from context")
	}

	cfg, err := c.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	var logger *slog.Logger
	switch cfg.LogLevel {
	case config.LogLevelDebug:
		logger = slog.New(tint.NewTextHandler(os.Stdout, &tint.Options{Level: slog.LevelDebug}))
	case config.LogLevelInfo:
		logger = slog.New(tint.NewTextHandler(os.Stdout, &tint.Options{Level: slog.LevelInfo}))
	case config.LogLevelWarn:
		logger = slog.New(tint.NewTextHandler(os.Stderr, &tint.Options{Level: slog.LevelWarn}))
	case config.LogLevelError:
		logger = slog.New(tint.NewTextHandler(os.Stderr, &tint.Options{Level: slog.LevelError}))
	}
	slog.SetDefault(logger)

	slog.Info("fah-dashboard", "version", cmd.Annotations["version"], "commit", cmd.Annotations["commit"])

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	st, err := store.Open(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	coll, err := newCollector(cfg, st, cmd.Annotations["version"])
	if err != nil {
		return err
	}
	if err := coll.Load(ctx); err != nil {
		return fmt.Errorf("failed to load history: %w", err)
	}

	collectDone := make(chan struct{})
	go func() {
		defer close(collectDone)
		coll.Run(ctx)
	}()

	server, err := server.NewServer(cfg, coll.View)
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}

	if err := server.Start(); err != nil {
		return fmt.Errorf("failed to start server: %w", err)
	}
	slog.Info("Server started successfully")

	stop := func(sig os.Signal) {
		// Remove control codes from the current line in the terminal
		fmt.Println("")

		slog.Info("Received signal", "signal", sig)

		err := server.Stop()
		if err != nil {
			slog.Error("Failed to stop server", "error", err)
		} else {
			slog.Info("Server stopped gracefully")
		}

		cancel()
		<-collectDone
		if err := st.Close(); err != nil {
			slog.Error("Failed to close database", "error", err)
		}
	}
	shutdown.AddWithParam(stop)
	shutdown.Listen(syscall.SIGINT, syscall.SIGKILL, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGHUP)

	return nil
}

func newCollector(cfg *config.Config, st *store.Store, version string) (*collector.Collector, error) {
	start, end, err := cfg.Event.Window()
	if err != nil {
		return nil, fmt.Errorf("failed to read event window: %w", err)
	}
	api := fah.New(
		fah.WithBaseURL(cfg.Collect.APIURL),
		fah.WithRateLimit(cfg.Collect.RequestsPerSec),
		fah.WithUserAgent("fah-dashboard/"+version),
	)
	return collector.New(api, st, collector.Options{
		Rules: stats.Rules{
			Team:      cfg.Event.Team,
			Start:     start,
			End:       end,
			MinDays:   cfg.Event.MinDays,
			MinPoints: cfg.Event.MinPoints,
			MinWUs:    cfg.Event.MinWUs,
		},
		Poll:          time.Duration(cfg.Collect.PollSeconds) * time.Second,
		MinSweep:      time.Duration(cfg.Collect.MinSweepSecs) * time.Second,
		RosterRefresh: time.Duration(cfg.Collect.RosterSeconds) * time.Second,
		Roster:        collector.HTTPRoster(roster.Fetch, cfg.Collect.RosterURL),
	}), nil
}
