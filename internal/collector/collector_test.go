package collector_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/USA-RedDragon/fah-dashboard/internal/collector"
	"github.com/USA-RedDragon/fah-dashboard/internal/fah"
	"github.com/USA-RedDragon/fah-dashboard/internal/stats"
	"github.com/USA-RedDragon/fah-dashboard/internal/store"
)

const team = 223518

type fakeAPI struct {
	teamScore atomic.Int64
	bigScore  atomic.Int64
	bigCredit atomic.Int64
	userReads atomic.Int64
	smallDown atomic.Bool
	newcomer  atomic.Bool

	smallCredit atomic.Int64
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == fmt.Sprintf("/team/%d", team):
		fmt.Fprintf(w, `{"id":%d,"score":%d}`, team, f.teamScore.Load())
	case r.URL.Path == "/user/monthly":
		fmt.Fprintf(w, `[{"name":"big","id":1,"credit":%d,"wus":100}`, f.bigCredit.Load())
		if credit := f.smallCredit.Load(); credit > 0 {
			fmt.Fprintf(w, `,{"name":"small","id":2,"credit":%d,"wus":1}`, credit)
		}
		for i := range 999 {
			fmt.Fprintf(w, `,{"name":"x%d","id":%d,"credit":3000,"wus":1}`, i, 1000+i)
		}
		fmt.Fprint(w, "]")
	case r.URL.Path == "/team/monthly":
		fmt.Fprintf(w, `[{"name":"Default (No team specified)","team":0,"credit":9,"wus":9},{"name":"LTT","team":%d,"credit":8,"wus":8}]`, team)
	case r.URL.Path == "/user/big":
		f.userReads.Add(1)
		fmt.Fprintf(w, `{"name":"big","id":1,"teams":[{"team":%d,"score":%d,"wus":600}]}`, team, f.bigScore.Load())
	case r.URL.Path == fmt.Sprintf("/team/%d/members", team):
		fmt.Fprint(w, `[["name","id","rank","score","wus"],["space_name",3,1,700,7],["ExactName",6,2,800,8]]`)
	case r.URL.Path == "/user/lifer":
		fmt.Fprintf(w, `{"name":"lifer","id":7,"teams":[{"team":%d,"score":90000,"wus":90}]}`, team)
	case r.URL.Path == "/user/Exact_Name":
		fmt.Fprintf(w, `{"name":"Exact_Name","id":5,"teams":[{"team":%d,"score":1,"wus":1}]}`, team)
	case r.URL.Path == "/user/space_name":
		fmt.Fprintf(w, `{"name":"space_name","id":3,"teams":[{"team":%d,"score":700,"wus":7}]}`, team)
	case r.URL.Path == "/user/late" && f.newcomer.Load():
		fmt.Fprintf(w, `{"name":"late","id":4,"teams":[{"team":%d,"score":900,"wus":2}]}`, team)
	case r.URL.Path == "/user/small" && f.smallDown.Load():
		w.WriteHeader(http.StatusInternalServerError)
	case r.URL.Path == "/user/small":
		fmt.Fprintf(w, `{"name":"small","id":2,"teams":[{"team":%d,"score":500,"wus":20}]}`, team)
	default:
		http.NotFound(w, r)
	}
}

func setup(t *testing.T) (*fakeAPI, *collector.Collector, *atomic.Pointer[time.Time]) {
	t.Helper()
	api := &fakeAPI{}
	api.teamScore.Store(1)
	api.bigScore.Store(10000)
	api.bigCredit.Store(4000)
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := &atomic.Pointer[time.Time]{}
	start := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	now.Store(&start)
	c := collector.New(fah.New(fah.WithBaseURL(srv.URL), fah.WithHTTPClient(srv.Client())), st, collector.Options{
		Rules: stats.Rules{
			Team:  team,
			Start: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 12, 7, 0, 0, 0, 0, time.UTC),
		},
		MinSweep:      time.Minute,
		RosterRefresh: time.Hour,
		Aliases:       collector.ParseAliases("Old Name = lifer , broken"),
		Roster: func(context.Context) ([]string, error) {
			return []string{"big", "small", "missing", "Space Name", "late", "Exact_Name", "Old Name"}, nil
		},
		Now: func() time.Time { return *now.Load() },
	})
	if err := c.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return api, c, now
}

func advance(now *atomic.Pointer[time.Time], d time.Duration) {
	next := now.Load().Add(d)
	now.Store(&next)
}

func TestBaselineFromMonthlyCreditAndBound(t *testing.T) {
	t.Parallel()
	_, c, _ := setup(t)
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}

	big, ok := c.View().Detail("big")
	if !ok {
		t.Fatal("big missing")
	}
	if big.Standing.Points != 4000 || big.Standing.PointsError != 0 || big.Standing.WUs != 100 {
		t.Errorf("big = %+v", big.Standing)
	}
	small, ok := c.View().Detail("SMALL")
	if !ok {
		t.Fatal("small missing")
	}
	if small.Standing.Points != 0 || small.Standing.PointsError != 3000 {
		t.Errorf("small = %+v", small.Standing)
	}
	if _, ok := c.View().Detail("missing"); ok {
		t.Error("unknown donor listed")
	}
}

func TestSweepsOnlyWhenBatchChanges(t *testing.T) {
	t.Parallel()
	api, c, now := setup(t)
	ctx := context.Background()
	if err := c.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	advance(now, time.Hour)
	if err := c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := api.userReads.Load(); got != 1 {
		t.Fatalf("reads after idle tick = %d", got)
	}

	api.teamScore.Store(2)
	advance(now, 10*time.Second)
	api.teamScore.Store(3)
	advance(now, 10*time.Second)
	if err := c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := api.userReads.Load(); got != 2 {
		t.Fatalf("reads after new batch = %d", got)
	}

	api.teamScore.Store(4)
	advance(now, time.Second)
	if err := c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := api.userReads.Load(); got != 2 {
		t.Errorf("sweep ignored the minimum interval, reads = %d", got)
	}
}

func TestPointsGrowFromBaselineAndUnassignedTeamHidden(t *testing.T) {
	t.Parallel()
	api, c, now := setup(t)
	ctx := context.Background()
	if err := c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	api.teamScore.Store(2)
	api.bigScore.Store(12000)
	advance(now, time.Hour)
	if err := c.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	big, _ := c.View().Detail("big")
	if big.Standing.Points != 6000 || big.Standing.Rank != 1 {
		t.Errorf("big = %+v", big.Standing)
	}
	teams := c.View().Overview.Teams
	if len(teams) != 1 || !strings.HasPrefix(teams[0].Name, "LTT") {
		t.Errorf("teams = %+v", teams)
	}
}

func TestRestartKeepsBaseline(t *testing.T) {
	t.Parallel()
	_, c, _ := setup(t)
	ctx := context.Background()
	if err := c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Load(ctx); err != nil {
		t.Fatal(err)
	}
	big, _ := c.View().Detail("big")
	if big.Standing.Points != 4000 {
		t.Errorf("big = %+v", big.Standing)
	}
}

func TestFailedDonorDoesNotLoseTheSweep(t *testing.T) {
	t.Parallel()
	api, c, _ := setup(t)
	api.smallDown.Store(true)
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if big, ok := c.View().Detail("big"); !ok || big.Standing.Points != 4000 {
		t.Errorf("big = %+v", big)
	}
}

func TestForumNameMatchesDonorSpelling(t *testing.T) {
	t.Parallel()
	_, c, _ := setup(t)
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.View().Detail("space_name"); !ok {
		t.Error("space_name not resolved")
	}
	if _, ok := c.View().Detail("Exact_Name"); !ok {
		t.Error("exact name lost to a similar donor")
	}
	if lifer, ok := c.View().Detail("lifer"); !ok || lifer.Standing.Points != 0 {
		t.Errorf("alias = %+v", lifer.Standing)
	}
	if got := c.View().Overview.Unresolved; len(got) != 2 || got[0] != "late" || got[1] != "missing" {
		t.Errorf("unresolved = %v", got)
	}
}

func TestNameThatAppearsLaterStartsFromZero(t *testing.T) {
	t.Parallel()
	api, c, now := setup(t)
	ctx := context.Background()
	if err := c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	api.newcomer.Store(true)
	api.teamScore.Store(2)
	advance(now, time.Hour)
	if err := c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	late, ok := c.View().Detail("late")
	if !ok || late.Standing.Points != 900 || late.Standing.PointsError != 0 {
		t.Errorf("late = %+v", late.Standing)
	}
}

func TestOtherTeamCreditCannotRaisePoints(t *testing.T) {
	t.Parallel()
	api, c, now := setup(t)
	ctx := context.Background()
	if err := c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	small, _ := c.View().Detail("small")
	if small.Standing.PointsError != 3000 {
		t.Fatalf("small = %+v", small.Standing)
	}
	api.smallCredit.Store(5000)
	api.teamScore.Store(2)
	advance(now, time.Hour)
	if err := c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	small, _ = c.View().Detail("small")
	if small.Standing.Points != 0 || small.Standing.PointsError != 3000 {
		t.Errorf("small = %+v", small.Standing)
	}
}

func TestNoSweepAfterLateCreditWindow(t *testing.T) {
	t.Parallel()
	api, c, now := setup(t)
	late := time.Date(2026, 12, 7, 1, 0, 0, 0, time.UTC)
	now.Store(&late)
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.userReads.Load() != 0 {
		t.Error("swept after the event ended")
	}
}

func TestAliasAddedLaterKeepsLifetimeOutOfPoints(t *testing.T) {
	t.Parallel()
	_, c, _ := setup(t)
	ctx := context.Background()
	if err := c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if lifer, ok := c.View().Detail("lifer"); !ok || lifer.Standing.Points != 0 {
		t.Errorf("alias = %+v", lifer.Standing)
	}
}

func TestStatusEstimatesNextBatchFromTheLastGap(t *testing.T) {
	t.Parallel()
	api, c, now := setup(t)
	ctx := context.Background()
	if err := c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	first := c.Status()
	if first.Sweeping || !first.NextBatch.Equal(first.LastBatch.Add(10*time.Minute)) {
		t.Errorf("status = %+v", first)
	}
	api.teamScore.Store(2)
	advance(now, 8*time.Minute)
	if err := c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	st := c.Status()
	if !st.NextBatch.Equal(st.LastBatch.Add(8 * time.Minute)) {
		t.Errorf("status = %+v", st)
	}
}
