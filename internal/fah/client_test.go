package fah_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/USA-RedDragon/fah-dashboard/internal/fah"
)

func serve(t *testing.T, handler http.HandlerFunc) *fah.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return fah.New(fah.WithBaseURL(srv.URL), fah.WithHTTPClient(srv.Client()))
}

func TestUserEscapesNameAndSplitsTeamScore(t *testing.T) {
	t.Parallel()
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/user/Scorching%20Arrow" {
			t.Errorf("path = %q", r.URL.EscapedPath())
		}
		_, _ = w.Write([]byte(`{"name":"Scorching Arrow","id":7,"score":300,"wus":30,"last":"2026-11-01 21:32:26",
			"teams":[{"team":223518,"score":200,"wus":20},{"team":0,"score":100,"wus":10}]}`))
	})

	u, err := c.User(context.Background(), "Scorching Arrow")
	if err != nil {
		t.Fatal(err)
	}
	if score, wus := u.TeamScore(223518); score != 200 || wus != 20 {
		t.Errorf("TeamScore = %d, %d", score, wus)
	}
	if score, _ := u.TeamScore(999); score != 0 {
		t.Errorf("TeamScore for other team = %d", score)
	}
	last, err := u.LastSeen()
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 1, 21, 32, 26, 0, time.UTC); !last.Equal(want) {
		t.Errorf("LastSeen = %v", last)
	}
}

func TestNotFound(t *testing.T) {
	t.Parallel()
	c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "HTTP_NOT_FOUND", http.StatusNotFound)
	})
	if _, err := c.Team(context.Background(), 1); !errors.Is(err, fah.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestServerError(t *testing.T) {
	t.Parallel()
	c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	_, err := c.Team(context.Background(), 1)
	if err == nil || errors.Is(err, fah.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestRetriesRateLimit(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	t.Cleanup(srv.Close)
	c := fah.New(fah.WithBaseURL(srv.URL), fah.WithHTTPClient(srv.Client()), fah.WithBackoff(time.Millisecond))

	if _, err := c.Team(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d", calls.Load())
	}
}

func TestGivesUpOnRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	c := fah.New(fah.WithBaseURL(srv.URL), fah.WithHTTPClient(srv.Client()), fah.WithBackoff(time.Millisecond))

	if _, err := c.Team(context.Background(), 1); !errors.Is(err, fah.ErrRateLimited) {
		t.Errorf("err = %v", err)
	}
}

func TestTeamMembersUsesHeaderOrder(t *testing.T) {
	t.Parallel()
	c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[["wus","name","id","rank","score"],[5,"a",1,2,3]]`))
	})
	members, err := c.TeamMembers(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	want := fah.Member{Name: "a", ID: 1, Rank: 2, Score: 3, WUs: 5}
	if len(members) != 1 || members[0] != want {
		t.Errorf("members = %+v", members)
	}
}

func TestTeamMembersRejectsBadShape(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"missing column": `[["name","id","rank","score"],["a",1,2,3]]`,
		"short row":      `[["name","id","rank","score","wus"],["a",1]]`,
	} {
		c := serve(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		})
		if _, err := c.TeamMembers(context.Background(), 1); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestMonthlySendsYearAndMonth(t *testing.T) {
	t.Parallel()
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("year") != "2026" || q.Get("month") != "11" {
			t.Errorf("query = %v", q)
		}
		if r.URL.Path == "/team/monthly" {
			_, _ = w.Write([]byte(`[{"name":"t","team":1,"credit":2,"wus":3}]`))
			return
		}
		_, _ = w.Write([]byte(`[{"name":"u","id":1,"credit":2,"wus":3}]`))
	})

	users, err := c.MonthlyUsers(context.Background(), 2026, time.November)
	if err != nil || len(users) != 1 || users[0].Credit != 2 {
		t.Errorf("users = %+v, err = %v", users, err)
	}
	teams, err := c.MonthlyTeams(context.Background(), 2026, time.November)
	if err != nil || len(teams) != 1 || teams[0].Team != 1 {
		t.Errorf("teams = %+v, err = %v", teams, err)
	}
}
