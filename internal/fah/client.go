// Package fah is a client for the public Folding@home stats API at
// api.foldingathome.org.
//
// The API has three limits that callers must plan for. TeamMembers returns
// only the top 1000 members by lifetime score and has no paging.
// MonthlyUsers and MonthlyTeams return only the top 1000 for the month.
// User.Score is a lifetime total across all teams, so use User.TeamScore to
// get the credit earned for one team.
package fah

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

const (
	// DefaultBaseURL is the production stats API.
	DefaultBaseURL = "https://api.foldingathome.org"

	defaultTimeout = 30 * time.Second
	maxBodyBytes   = 16 << 20
	maxAttempts    = 4
	firstBackoff   = 15 * time.Second
	lastSeenLayout = "2006-01-02 15:04:05"
)

// ErrNotFound is returned when the API has no such donor or team.
var ErrNotFound = errors.New("not found")

// ErrRateLimited is returned when the API still answers 429 after retries.
var ErrRateLimited = errors.New("rate limited")

// Client calls the stats API. It is safe for concurrent use.
type Client struct {
	baseURL   string
	userAgent string
	http      *http.Client
	limiter   *rate.Limiter
	backoff   time.Duration

	assignBase string
}

// Option changes how a Client is built.
type Option func(*Client)

// WithBaseURL sets the API root.
func WithBaseURL(baseURL string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(baseURL, "/") }
}

// WithHTTPClient sets the HTTP client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// WithRateLimit limits the client to perSecond requests each second.
func WithRateLimit(perSecond float64) Option {
	return func(c *Client) { c.limiter = rate.NewLimiter(rate.Limit(perSecond), 1) }
}

// WithBackoff sets the wait after the first 429 response. Later waits double.
func WithBackoff(d time.Duration) Option {
	return func(c *Client) { c.backoff = d }
}

// WithAssignBase sends assignment server requests to one URL.
func WithAssignBase(url string) Option {
	return func(c *Client) { c.assignBase = strings.TrimRight(url, "/") }
}

// WithUserAgent sets the User-Agent header.
func WithUserAgent(userAgent string) Option {
	return func(c *Client) { c.userAgent = userAgent }
}

// New returns a Client.
func New(opts ...Option) *Client {
	c := &Client{
		baseURL:   DefaultBaseURL,
		userAgent: "fah-dashboard",
		http:      &http.Client{Timeout: defaultTimeout},
		limiter:   rate.NewLimiter(rate.Inf, 1),
		backoff:   firstBackoff,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// UserTeam is the credit a donor earned for one team.
type UserTeam struct {
	Team  int    `json:"team"`
	Name  string `json:"name"`
	Score int64  `json:"score"`
	WUs   int64  `json:"wus"`
}

// User is a donor.
type User struct {
	Name     string     `json:"name"`
	ID       int64      `json:"id"`
	Score    int64      `json:"score"`
	WUs      int64      `json:"wus"`
	Rank     int64      `json:"rank"`
	Active50 int        `json:"active_50"`
	Active7  int        `json:"active_7"`
	Last     string     `json:"last"`
	Users    int64      `json:"users"`
	Teams    []UserTeam `json:"teams"`
}

// TeamScore returns the credit and work units the donor earned for team.
func (u User) TeamScore(team int) (score, wus int64) {
	for _, t := range u.Teams {
		if t.Team == team {
			return t.Score, t.WUs
		}
	}
	return 0, 0
}

// LastSeen returns the time of the donor's last credited work unit.
func (u User) LastSeen() (time.Time, error) {
	t, err := time.ParseInLocation(lastSeenLayout, u.Last, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse last seen: %w", err)
	}
	return t, nil
}

// Team is a folding team.
type Team struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Founder string `json:"founder"`
	Score   int64  `json:"score"`
	WUs     int64  `json:"wus"`
	Rank    int64  `json:"rank"`
}

// Member is one row of a team's member list.
type Member struct {
	Name  string
	ID    int64
	Rank  int64
	Score int64
	WUs   int64
}

// MonthlyUser is a donor's credit for one calendar month.
type MonthlyUser struct {
	Name   string `json:"name"`
	ID     int64  `json:"id"`
	Credit int64  `json:"credit"`
	WUs    int64  `json:"wus"`
}

// MonthlyTeam is a team's credit for one calendar month.
type MonthlyTeam struct {
	Name   string `json:"name"`
	Team   int    `json:"team"`
	Credit int64  `json:"credit"`
	WUs    int64  `json:"wus"`
}

// User returns the donor with this name.
func (c *Client) User(ctx context.Context, name string) (User, error) {
	var u User
	err := c.get(ctx, "/user/"+url.PathEscape(name), nil, &u)
	return u, err
}

// Team returns the team with this ID.
func (c *Client) Team(ctx context.Context, id int) (Team, error) {
	var t Team
	err := c.get(ctx, "/team/"+strconv.Itoa(id), nil, &t)
	return t, err
}

// TeamMembers returns the top 1000 members of a team by lifetime score.
func (c *Client) TeamMembers(ctx context.Context, id int) ([]Member, error) {
	var rows [][]json.RawMessage
	if err := c.get(ctx, "/team/"+strconv.Itoa(id)+"/members", nil, &rows); err != nil {
		return nil, err
	}
	return decodeMembers(rows)
}

// MonthlyUsers returns the top 1000 donors for a calendar month in UTC.
func (c *Client) MonthlyUsers(ctx context.Context, year int, month time.Month) ([]MonthlyUser, error) {
	var out []MonthlyUser
	err := c.get(ctx, "/user/monthly", monthQuery(year, month), &out)
	return out, err
}

// MonthlyTeams returns the top 1000 teams for a calendar month in UTC.
func (c *Client) MonthlyTeams(ctx context.Context, year int, month time.Month) ([]MonthlyTeam, error) {
	var out []MonthlyTeam
	err := c.get(ctx, "/team/monthly", monthQuery(year, month), &out)
	return out, err
}

func monthQuery(year int, month time.Month) url.Values {
	return url.Values{
		"year":  {strconv.Itoa(year)},
		"month": {strconv.Itoa(int(month))},
	}
}

func decodeMembers(rows [][]json.RawMessage) ([]Member, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	columns := map[string]int{}
	for i, raw := range rows[0] {
		var name string
		if err := json.Unmarshal(raw, &name); err != nil {
			return nil, fmt.Errorf("decode member header: %w", err)
		}
		columns[name] = i
	}
	for _, name := range []string{"name", "id", "rank", "score", "wus"} {
		if _, ok := columns[name]; !ok {
			return nil, fmt.Errorf("member header has no %q column", name)
		}
	}

	members := make([]Member, 0, len(rows)-1)
	for _, row := range rows[1:] {
		if len(row) != len(rows[0]) {
			return nil, fmt.Errorf("member row has %d columns, want %d", len(row), len(rows[0]))
		}
		var m Member
		fields := []struct {
			column string
			dest   any
		}{
			{"name", &m.Name},
			{"id", &m.ID},
			{"rank", &m.Rank},
			{"score", &m.Score},
			{"wus", &m.WUs},
		}
		for _, f := range fields {
			if err := json.Unmarshal(row[columns[f.column]], f.dest); err != nil {
				return nil, fmt.Errorf("decode member %s: %w", f.column, err)
			}
		}
		members = append(members, m)
	}
	return members, nil
}

// get retries 429 responses. The API sends no Retry-After header, and its
// limit clears within a minute.
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	return c.getFrom(ctx, c.baseURL, path, query, out)
}

func (c *Client) getFrom(ctx context.Context, base, path string, query url.Values, out any) error {
	wait := c.backoff
	for attempt := 1; ; attempt++ {
		err := c.getOnce(ctx, base, path, query, out)
		if !errors.Is(err, ErrRateLimited) || attempt == maxAttempts {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait *= 2
	}
}

func (c *Client) getOnce(ctx context.Context, base, path string, query url.Values, out any) error {
	if err := c.limiter.Wait(ctx); err != nil {
		return fmt.Errorf("wait for rate limit: %w", err)
	}

	target := base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("get %s: %w", path, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("get %s: %w", path, ErrNotFound)
	case resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("get %s: %w", path, ErrRateLimited)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("get %s: unexpected status %s", path, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}
