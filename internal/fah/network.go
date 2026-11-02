package fah

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"
)

const (
	offlineVersion = "0.0"
	secondsPerHour = 3600
)

// ProjectType is the work available for one core type across the network.
type ProjectType struct {
	Name        string  `json:"name"`
	Public      int64   `json:"public"`
	Beta        int64   `json:"beta"`
	Total       int64   `json:"total"`
	RatePerHour float64 `json:"ratePerHour"`
}

// Network is a snapshot of the F@H work servers. Its numbers are estimates
// published by the assignment servers.
type Network struct {
	Types     []ProjectType `json:"types"`
	Servers   int           `json:"servers"`
	UpdatedAt time.Time     `json:"updatedAt"`
}

type typeStats struct {
	Total  int64   `json:"total"`
	Beta   int64   `json:"beta"`
	Public int64   `json:"public"`
	Rate   float64 `json:"rate"`
}

type workServer struct {
	Address string               `json:"address"`
	Version string               `json:"version"`
	Types   map[string]typeStats `json:"types"`
}

type wsSummary struct {
	WS []workServer `json:"ws"`
}

// AssignServers returns the hostnames of the assignment servers.
func (c *Client) AssignServers(ctx context.Context) ([]string, error) {
	var hosts []string
	err := c.get(ctx, "/as", nil, &hosts)
	return hosts, err
}

// Network reads every assignment server and merges their work servers. Each
// assignment server lists the same work servers, so job counts come from the
// first listing while rates add up across all of them.
func (c *Client) Network(ctx context.Context) (Network, error) {
	hosts, err := c.AssignServers(ctx)
	if err != nil {
		return Network{}, fmt.Errorf("read assignment servers: %w", err)
	}

	merged := map[string]*workServer{}
	for _, host := range hosts {
		var summary wsSummary
		if err := c.getFrom(ctx, c.assignURL(host), "/api/ws/summary", nil, &summary); err != nil {
			return Network{}, fmt.Errorf("read %s: %w", host, err)
		}
		for _, ws := range summary.WS {
			mergeServer(merged, ws)
		}
	}

	byType := map[string]*ProjectType{}
	for _, ws := range merged {
		for name, stats := range ws.Types {
			t, ok := byType[name]
			if !ok {
				t = &ProjectType{Name: name}
				byType[name] = t
			}
			t.Public += stats.Public
			t.Beta += stats.Beta
			t.Total += stats.Total
			t.RatePerHour += stats.Rate * secondsPerHour
		}
	}

	out := Network{Servers: len(merged), Types: make([]ProjectType, 0, len(byType))}
	for _, t := range byType {
		out.Types = append(out.Types, *t)
	}
	slices.SortFunc(out.Types, func(a, b ProjectType) int {
		return cmp.Or(cmp.Compare(b.RatePerHour, a.RatePerHour), cmp.Compare(a.Name, b.Name))
	})
	return out, nil
}

func mergeServer(merged map[string]*workServer, ws workServer) {
	seen, ok := merged[ws.Address]
	if !ok {
		if ws.Version != offlineVersion {
			copied := ws
			merged[ws.Address] = &copied
		}
		return
	}
	for name, stats := range ws.Types {
		existing, ok := seen.Types[name]
		if !ok {
			seen.Types[name] = stats
			continue
		}
		existing.Rate += stats.Rate
		seen.Types[name] = existing
	}
}

func (c *Client) assignURL(host string) string {
	if c.assignBase != "" {
		return c.assignBase
	}
	return "https://" + host
}
