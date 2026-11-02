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
	batchGap      = 10 * time.Minute
	minBatchGap   = 8 * time.Minute
	maxBatchGap   = 12 * time.Minute
	missingRetry  = time.Hour
	networkPoll   = 5 * time.Minute
	monthKeyScale = 100
)

// RosterFunc returns the names to track.
type RosterFunc func(ctx context.Context) ([]string, error)

// Status is what the collector is doing right now.
type Status struct {
	Sweeping  bool      `json:"sweeping"`
	Read      int64     `json:"read"`
	Total     int       `json:"total"`
	UpdatedAt time.Time `json:"updatedAt"`
	LastBatch time.Time `json:"lastBatch"`
	NextBatch time.Time `json:"nextBatch"`
}

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

	resolved    map[string]string
	lastMissing time.Time
	lastNetwork time.Time
	network     atomic.Pointer[fah.Network]

	statusMu  sync.Mutex
	status    Status
	read      atomic.Int64
	seenScore int64
	gap       time.Duration

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
		resolved:    map[string]string{},
		gap:         batchGap,
	}
}

// View returns the latest published view. It is nil before Load.
func (c *Collector) View() *stats.View {
	return c.view.Load()
}

// Status returns the collector's current activity.
func (c *Collector) Status() Status {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	st := c.status
	st.Read = c.read.Load()
	return st
}

func (c *Collector) noteBatch(score int64, now time.Time) {
	if score == c.seenScore {
		return
	}
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	if !c.status.LastBatch.IsZero() {
		if gap := now.Sub(c.status.LastBatch); gap >= minBatchGap && gap <= maxBatchGap {
			c.gap = gap
		}
	}
	c.seenScore = score
	c.status.LastBatch = now
	c.status.NextBatch = now.Add(c.gap)
}

func (c *Collector) setSweeping(sweeping bool, total int) {
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	c.status.Sweeping = sweeping
	c.status.Total = total
	c.read.Store(0)
}

// Network returns the latest F@H network snapshot, or nil before the first.
func (c *Collector) Network() *fah.Network {
	return c.network.Load()
}

func (c *Collector) refreshNetwork(ctx context.Context, now time.Time) {
	if now.Sub(c.lastNetwork) < networkPoll {
		return
	}
	c.lastNetwork = now
	n, err := c.api.Network(ctx)
	if err != nil {
		slog.Warn("read network stats", "error", err)
		return
	}
	n.UpdatedAt = now
	c.network.Store(&n)
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
	c.noteBatch(team.Score, now)
	c.refreshNetwork(ctx, now)
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
	return nil
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

func (c *Collector) publishUpdate(ctx context.Context) error {
	if err := c.publish(ctx); err != nil {
		return err
	}
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	c.status.UpdatedAt = c.opts.Now().UTC()
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

// sweep updates donors in two passes. The team list and monthly board cover
// the biggest folders in two requests, so they publish within seconds of a
// batch. Everyone else takes one request each and publishes when that ends.
func (c *Collector) sweep(ctx context.Context, now time.Time) error {
	before, err := c.eventCredits(ctx, now)
	if err != nil {
		return err
	}
	if err := c.recordTeams(ctx, now); err != nil {
		return err
	}
	members, err := c.api.TeamMembers(ctx, c.opts.Rules.Team)
	if err != nil {
		return fmt.Errorf("read team members: %w", err)
	}

	fast, slow := c.partition(members, before, now)
	if len(fast) > 0 {
		if err := c.record(ctx, fast, before, before, now); err != nil {
			return err
		}
		if err := c.publishUpdate(ctx); err != nil {
			return err
		}
	}

	checkMissing := now.Sub(c.lastMissing) >= missingRetry
	c.setSweeping(true, len(slow))
	defer c.setSweeping(false, 0)
	users, missing, err := c.readUsers(ctx, c.spellings(members, before, slow))
	if err != nil {
		return err
	}
	after, err := c.eventCredits(ctx, now)
	if err != nil {
		return err
	}
	if err := c.record(ctx, users, before, after, now); err != nil {
		return err
	}

	if checkMissing {
		c.lastMissing = now
		c.unresolved = missing
		if err := c.store.AddMissing(ctx, missing); err != nil {
			return err
		}
		for _, name := range missing {
			c.everMissing[normalize(name)] = true
		}
	}
	return c.publishUpdate(ctx)
}

// partition splits the roster into donors the bulk lists answer and names that
// need their own read. Names that have never resolved are skipped between
// hourly retries.
func (c *Collector) partition(members []fah.Member, credits eventCredit, now time.Time) ([]readResult, []string) {
	byName := make(map[string]fah.Member, len(members))
	for _, m := range members {
		byName[m.Name] = m
	}
	donors := make(map[string]store.Donor, len(c.donors))
	for _, d := range c.donors {
		donors[d.Name] = d
	}
	waiting := map[string]bool{}
	if now.Sub(c.lastMissing) < missingRetry {
		for _, name := range c.unresolved {
			waiting[name] = true
		}
	}

	var fast []readResult
	var slow []string
	for _, name := range c.names {
		if waiting[name] {
			continue
		}
		donor, ok := c.resolved[name]
		if !ok {
			slow = append(slow, name)
			continue
		}
		if m, ok := byName[donor]; ok {
			fast = append(fast, readResult{user: c.teamUser(m.Name, m.ID, m.Score, m.WUs), wanted: name})
			continue
		}
		if d, ok := donors[donor]; ok && d.BaselineError == 0 {
			if cr := credits.forUser(d.ID); cr.bound == 0 {
				user := c.teamUser(d.Name, d.ID, d.BaselineScore+cr.points, d.BaselineWUs+cr.wus)
				fast = append(fast, readResult{user: user, wanted: name})
				continue
			}
		}
		slow = append(slow, name)
	}
	return fast, slow
}

func (c *Collector) teamUser(name string, id, score, wus int64) fah.User {
	return fah.User{Name: name, ID: id, Teams: []fah.UserTeam{{Team: c.opts.Rules.Team, Score: score, WUs: wus}}}
}

func (c *Collector) record(ctx context.Context, users []readResult, before, after eventCredit, now time.Time) error {
	samples := make([]store.Sample, 0, len(users))
	for _, r := range users {
		u := r.user
		c.resolved[r.wanted] = u.Name
		score, wus := u.TeamScore(c.opts.Rules.Team)
		cr := before.forUser(u.ID)
		cr.bound += max(after.forUser(u.ID).points-cr.points, 0)
		_, aliased := c.opts.Aliases[r.wanted]
		newName := c.everMissing[normalize(r.wanted)] && !aliased && score <= cr.bound
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
func (c *Collector) spellings(members []fah.Member, credits eventCredit, names []string) map[string]string {
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
	out := make(map[string]string, len(names))
	for _, name := range names {
		if donor, ok := c.resolved[name]; ok {
			out[name] = donor
		} else if donor, ok := c.opts.Aliases[name]; ok {
			out[name] = donor
		} else if donor, ok := known[normalize(name)]; ok {
			out[name] = donor
		} else {
			out[name] = strings.ReplaceAll(name, " ", "_")
		}
	}
	return out
}

func (c *Collector) readUser(ctx context.Context, wanted, alternate string) (fah.User, error) {
	if _, ok := c.resolved[wanted]; ok {
		return c.api.User(ctx, alternate)
	}
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
			c.read.Add(1)
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
