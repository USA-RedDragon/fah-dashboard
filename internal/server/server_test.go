package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/configulator/v2"
	"github.com/USA-RedDragon/fah-dashboard/internal/collector"
	"github.com/USA-RedDragon/fah-dashboard/internal/config"
	"github.com/USA-RedDragon/fah-dashboard/internal/fah"
	"github.com/USA-RedDragon/fah-dashboard/internal/server"
	"github.com/USA-RedDragon/fah-dashboard/internal/stats"
	"github.com/USA-RedDragon/fah-dashboard/internal/store"
)

func start(t *testing.T, view server.ViewFunc) string {
	t.Helper()
	return startOn(t, "127.0.0.1", view)
}

func startOn(t *testing.T, bind string, view server.ViewFunc) string {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("not a TCP address")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	srv, err := server.NewServer(&config.Config{HTTP: config.HTTP{Bind: bind, Port: addr.Port}}, view, func() collector.Status {
		return collector.Status{Sweeping: true, Read: 3, Total: 9}
	}, func() *fah.Network { return &fah.Network{Servers: 1} })
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	return fmt.Sprintf("http://127.0.0.1:%d", addr.Port)
}

func get(t *testing.T, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestRoutes(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	view := stats.Build(stats.Rules{Start: now.Add(-time.Hour), End: now.Add(time.Hour)},
		[]store.Donor{{ID: 1, Name: "Scorching Arrow"}},
		[]store.Sample{{DonorID: 1, At: now, Score: 5}}, nil, now)
	base := start(t, func() *stats.View { return view })

	for path, want := range map[string]int{
		"/healthz":                      http.StatusOK,
		"/":                             http.StatusOK,
		"/app.js":                       http.StatusOK,
		"/donor/anyone":                 http.StatusOK,
		"/api/overview":                 http.StatusOK,
		"/api/status":                   http.StatusOK,
		"/api/network":                  http.StatusOK,
		"/api/donors/scorching%20arrow": http.StatusOK,
		"/api/donors/nobody":            http.StatusNotFound,
	} {
		if got := get(t, base+path).StatusCode; got != want {
			t.Errorf("%s = %d, want %d", path, got, want)
		}
	}

	var overview stats.Overview
	if err := json.NewDecoder(get(t, base+"/api/overview").Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	if len(overview.Standings) != 1 || overview.Standings[0].Points != 5 {
		t.Errorf("overview = %+v", overview)
	}
}

func TestNoDataYet(t *testing.T) {
	t.Parallel()
	base := start(t, func() *stats.View { return nil })
	resp := get(t, base+"/api/overview")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d", resp.StatusCode)
	}
	page := get(t, base+"/")
	if !strings.HasPrefix(page.Header.Get("Content-Type"), "text/html") {
		t.Errorf("type = %q", page.Header.Get("Content-Type"))
	}
}

func TestDefaultBindListens(t *testing.T) {
	t.Parallel()
	cfg, err := configulator.New(config.ConfigSchema()).Default()
	if err != nil {
		t.Fatal(err)
	}
	base := startOn(t, cfg.HTTP.Bind, func() *stats.View { return nil })
	if got := get(t, base+"/healthz").StatusCode; got != http.StatusOK {
		t.Errorf("status = %d", got)
	}
}
