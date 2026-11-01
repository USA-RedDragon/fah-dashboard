// Package collector polls the stats API and records event history.
//
// The API publishes in batches, so the collector polls the team total and
// reads every donor only after that total changes.
package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/USA-RedDragon/fah-dashboard/internal/fah"
	"github.com/USA-RedDragon/fah-dashboard/internal/stats"
	"github.com/USA-RedDragon/fah-dashboard/internal/store"
	"golang.org/x/sync/errgroup"
)

const (
	monthlyLimit  = 1000
	teamsKept     = 100
	sweepWorkers  = 2
	lateCredit    = 15 * time.Minute
	monthKeyScale = 100
)

// RosterFunc returns the names to track.
type RosterFunc func(ctx context.Context) ([]string, error)

// Options configures a Collector.
type Options struct {
	Rules         stats.Rules
	Poll          time.Duration
	MinSweep      time.Duration
	RosterRefresh time.Duration
	Roster        RosterFunc
	Aliases       map[string]string
	Now           func() time.Time
}

// Collector records history and publishes the latest stats.View.
type Collector struct {
	opts   Options
	api    *fah.Client
	store  *store.Store
	view   atomic.Pointer[stats.View]
	names  []string
	donors map[int64]store.Donor
	last   map[int64]store.Sample

	everMissing map[string]bool
	unresolved  []string

	lastTeamScore int64
	lastSweep     time.Time
	lastRoster    time.Time
}

// New returns a Collector.
func New(api *fah.Client, st *store.Store, opts Options) *Collector {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Collector{
		opts:   opts,
		api:    api,
		store:  st,
		donors: map[int64]store.Donor{},
		last:   map[int64]store.Sample{},

		everMissing: map[string]bool{},
	}
}

// View returns the latest published view. It is nil before Load.
func (c *Collector) View() *stats.View {
	return c.view.Load()
}

// Load restores state from the database and publishes a first view.
func (c *Collector) Load(ctx context.Context) error {
	donors, err := c.store.Donors(ctx)
	if err != nil {
		return err
	}
	for _, d := range donors {
		c.donors[d.ID] = d
	}
	samples, err := c.store.Samples(ctx)
	if err != nil {
		return err
	}
	for _, s := range samples {
		c.last[s.DonorID] = s
	}
	missing, err := c.store.Missing(ctx)
	if err != nil {
		return err
	}
	for _, name := range missing {
		c.everMissing[normalize(name)] = true
	}
	return c.publish(ctx)
}

// Run polls until ctx ends.
func (c *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.opts.Poll)
	defer ticker.Stop()
	for {
		if err := c.Tick(ctx); err != nil && ctx.Err() == nil {
			slog.Error("collect failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick does one poll and sweeps if a new batch has landed.
func (c *Collector) Tick(ctx context.Context) error {
	now := c.opts.Now().UTC()
	if now.Before(c.opts.Rules.Start) || now.After(c.opts.Rules.End.Add(lateCredit)) {
		return nil
	}

	team, err := c.api.Team(ctx, c.opts.Rules.Team)
	if err != nil {
		return fmt.Errorf("poll team: %w", err)
	}
	if team.Score == c.lastTeamScore || now.Sub(c.lastSweep) < c.opts.MinSweep {
		return nil
	}

	if now.Sub(c.lastRoster) >= c.opts.RosterRefresh || c.names == nil {
		names, err := c.opts.Roster(ctx)
		switch {
		case err == nil:
			c.names, c.lastRoster = names, now
		case c.names == nil:
			return fmt.Errorf("load roster: %w", err)
		default:
			slog.Warn("keeping old roster", "error", err)
		}
	}

	if err := c.sweep(ctx, now); err != nil {
		return err
	}
	slog.Info("swept stats batch", "team_score", team.Score, "delta", team.Score-c.lastTeamScore, "donors", len(c.names))
	c.lastTeamScore, c.lastSweep = team.Score, now
	return c.publish(ctx)
}

func (c *Collector) publish(ctx context.Context) error {
	donors, err := c.store.Donors(ctx)
	if err != nil {
		return err
	}
	samples, err := c.store.Samples(ctx)
	if err != nil {
		return err
	}
	teams, err := c.store.TeamSamples(ctx)
	if err != nil {
		return err
	}
	view := stats.Build(c.opts.Rules, donors, samples, teams, c.opts.Now().UTC())
	view.Overview.Unresolved = c.unresolved
	c.view.Store(view)
	return nil
}

type credit struct {
	points int64
	wus    int64
	bound  int64
}

type readResult struct {
	user   fah.User
	wanted string
}

func (c *Collector) sweep(ctx context.Context, now time.Time) error {
	before, err := c.eventCredits(ctx, now)
	if err != nil {
		return err
	}
	if err := c.recordTeams(ctx, now); err != nil {
		return err
	}
	names, err := c.spellings(ctx, before)
	if err != nil {
		return err
	}

	users, missing, err := c.readUsers(ctx, names)
	if err != nil {
		return err
	}
	after, err := c.eventCredits(ctx, now)
	if err != nil {
		return err
	}

	samples := make([]store.Sample, 0, len(users))
	for _, r := range users {
		u := r.user
		score, wus := u.TeamScore(c.opts.Rules.Team)
		cr := before.forUser(u.ID)
		cr.bound += max(after.forUser(u.ID).points-cr.points, 0)
		newName := c.everMissing[normalize(r.wanted)] && normalize(r.wanted) == normalize(u.Name)
		if err := c.updateBaseline(ctx, u, score, wus, cr, newName, now); err != nil {
			return err
		}
		prev, seen := c.last[u.ID]
		if !seen || prev.Score != score || prev.WUs != wus {
			samples = append(samples, store.Sample{DonorID: u.ID, At: now, Score: score, WUs: wus})
		}
	}
	if err := c.store.AddSamples(ctx, samples); err != nil {
		return err
	}
	for _, s := range samples {
		c.last[s.DonorID] = s
	}

	c.unresolved = missing
	if err := c.store.AddMissing(ctx, missing); err != nil {
		return err
	}
	for _, name := range missing {
		c.everMissing[normalize(name)] = true
	}
	return nil
}

// updateBaseline sets the score a donor had when the event started. Monthly
// credit gives it exactly, for donors on the monthly top 1000. Anyone else
// earned less than the board's lowest entry, so the first score seen is a
// baseline with that lowest entry as its error bound. The two board reads
// around a sweep widen the bound when a batch lands during the sweep. A name
// the API did not know earlier in the event started from zero. Monthly credit
// covers all teams, so a later reading below the known bound is credit for
// another team and leaves the baseline alone.
func (c *Collector) updateBaseline(ctx context.Context, u fah.User, score, wus int64, cr credit, newName bool, now time.Time) error {
	d, known := c.donors[u.ID]
	next := d
	next.ID, next.Name = u.ID, u.Name
	switch {
	case !known && newName:
		next.FirstSeen = now
	case !known:
		next.FirstSeen = now
		next.BaselineScore = max(score-cr.points, 0)
		next.BaselineWUs = max(wus-cr.wus, 0)
		next.BaselineError = cr.bound
	case cr.bound < d.BaselineError && score-cr.points >= d.BaselineScore-d.BaselineError:
		next.BaselineScore = max(score-cr.points, 0)
		next.BaselineWUs = max(wus-cr.wus, 0)
		next.BaselineError = cr.bound
	}
	if known && next == d {
		return nil
	}
	if err := c.store.UpsertDonor(ctx, next); err != nil {
		return err
	}
	c.donors[u.ID] = next
	return nil
}

// spellings maps each roster name to the donor name to try after the exact
// name fails. F@H turns spaces into underscores and forum names can differ in
// punctuation, so a name is matched against the team list and monthly board
// with both reduced to lowercase letters and digits.
func (c *Collector) spellings(ctx context.Context, credits eventCredit) (map[string]string, error) {
	members, err := c.api.TeamMembers(ctx, c.opts.Rules.Team)
	if err != nil {
		return nil, fmt.Errorf("read team members: %w", err)
	}
	known := make(map[string]string, len(members))
	for _, m := range members {
		known[normalize(m.Name)] = m.Name
	}
	for _, month := range credits.months {
		for _, u := range month.users {
			if _, ok := known[normalize(u.Name)]; !ok {
				known[normalize(u.Name)] = u.Name
			}
		}
	}
	out := make(map[string]string, len(c.names))
	for _, name := range c.names {
		if donor, ok := c.opts.Aliases[name]; ok {
			out[name] = donor
		} else if donor, ok := known[normalize(name)]; ok {
			out[name] = donor
		} else {
			out[name] = strings.ReplaceAll(name, " ", "_")
		}
	}
	return out, nil
}

func (c *Collector) readUser(ctx context.Context, wanted, alternate string) (fah.User, error) {
	u, err := c.api.User(ctx, wanted)
	if errors.Is(err, fah.ErrNotFound) && alternate != wanted {
		return c.api.User(ctx, alternate)
	}
	return u, err
}

func normalize(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, name)
}

// readUsers keeps what it can read. A donor that fails keeps its last sample
// until the next sweep, and only a canceled context stops the sweep.
func (c *Collector) readUsers(ctx context.Context, names map[string]string) ([]readResult, []string, error) {
	var mu sync.Mutex
	var results []readResult
	var missing []string
	failed := 0
	group := errgroup.Group{}
	group.SetLimit(sweepWorkers)
	for wanted, donor := range names {
		group.Go(func() error {
			u, err := c.readUser(ctx, wanted, donor)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				results = append(results, readResult{user: u, wanted: wanted})
			case errors.Is(err, fah.ErrNotFound):
				missing = append(missing, wanted)
			case ctx.Err() == nil:
				failed++
				slog.Warn("read donor failed", "name", donor, "error", err)
			}
			return nil
		})
	}
	_ = group.Wait()
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("read donors: %w", err)
	}
	slog.Info("read donors", "names", len(names), "not_found", len(missing), "failed", failed)
	slices.Sort(missing)

	seen := make(map[int64]bool, len(results))
	out := make([]readResult, 0, len(results))
	for _, r := range results {
		if !seen[r.user.ID] {
			seen[r.user.ID] = true
			out = append(out, r)
		}
	}
	return out, missing, nil
}

type eventCredit struct {
	months []monthCredit
}

type monthCredit struct {
	users  map[int64]fah.MonthlyUser
	cutoff int64
}

func (e eventCredit) forUser(id int64) credit {
	var cr credit
	for _, m := range e.months {
		if u, ok := m.users[id]; ok {
			cr.points += u.Credit
			cr.wus += u.WUs
		} else {
			cr.bound += m.cutoff
		}
	}
	return cr
}

func (c *Collector) eventCredits(ctx context.Context, now time.Time) (eventCredit, error) {
	var out eventCredit
	for _, month := range c.eventMonths(now) {
		rows, err := c.api.MonthlyUsers(ctx, month.Year(), month.Month())
		if err != nil {
			return out, fmt.Errorf("read monthly donors: %w", err)
		}
		mc := monthCredit{users: make(map[int64]fah.MonthlyUser, len(rows))}
		for _, r := range rows {
			mc.users[r.ID] = r
		}
		if len(rows) >= monthlyLimit {
			mc.cutoff = rows[len(rows)-1].Credit
		}
		out.months = append(out.months, mc)
	}
	return out, nil
}

// recordTeams keeps any team in the top 100 of any event month, with its row
// from every month, so a team that drops off one month keeps its full total.
func (c *Collector) recordTeams(ctx context.Context, now time.Time) error {
	keep := map[int]bool{c.opts.Rules.Team: true}
	type board struct {
		key  int
		rows []fah.MonthlyTeam
	}
	var boards []board
	for _, month := range c.eventMonths(now) {
		rows, err := c.api.MonthlyTeams(ctx, month.Year(), month.Month())
		if err != nil {
			return fmt.Errorf("read monthly teams: %w", err)
		}
		boards = append(boards, board{month.Year()*monthKeyScale + int(month.Month()), rows})
		for i, r := range rows {
			if i < teamsKept && !isUnassigned(r) {
				keep[r.Team] = true
			}
		}
	}
	var samples []store.TeamSample
	for _, b := range boards {
		for _, r := range b.rows {
			if keep[r.Team] {
				samples = append(samples, store.TeamSample{Team: r.Team, At: now, Month: b.key, Name: r.Name, Credit: r.Credit, WUs: r.WUs})
			}
		}
	}
	return c.store.AddTeamSamples(ctx, samples)
}

func isUnassigned(t fah.MonthlyTeam) bool {
	return t.Team == 0 || strings.HasPrefix(t.Name, "Default")
}

func (c *Collector) eventMonths(now time.Time) []time.Time {
	var out []time.Time
	start := c.opts.Rules.Start
	month := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC)
	for !month.After(now) && month.Before(c.opts.Rules.End) {
		out = append(out, month)
		month = month.AddDate(0, 1, 0)
	}
	return out
}

// ParseAliases reads comma-separated sign-up=donor pairs.
func ParseAliases(raw string) map[string]string {
	out := map[string]string{}
	for pair := range strings.SplitSeq(raw, ",") {
		if name, donor, ok := strings.Cut(pair, "="); ok {
			out[strings.TrimSpace(name)] = strings.TrimSpace(donor)
		}
	}
	return out
}

// HTTPRoster adapts a roster fetcher to a RosterFunc.
func HTTPRoster(fetch func(context.Context, *http.Client, string) ([]string, error), url string) RosterFunc {
	client := &http.Client{Timeout: time.Minute}
	return func(ctx context.Context) ([]string, error) {
		return fetch(ctx, client, url)
	}
}
