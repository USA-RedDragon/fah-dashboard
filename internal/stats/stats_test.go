package stats_test

import (
	"testing"
	"time"

	"github.com/USA-RedDragon/fah-dashboard/internal/stats"
	"github.com/USA-RedDragon/fah-dashboard/internal/store"
)

func rules() stats.Rules {
	return stats.Rules{
		Team:      1,
		Start:     date(1, 0),
		End:       time.Date(2026, 12, 7, 0, 0, 0, 0, time.UTC),
		MinDays:   30,
		MinPoints: 1000,
		MinWUs:    10,
	}
}

func date(day, hour int) time.Time {
	return time.Date(2026, 11, day, hour, 0, 0, 0, time.UTC)
}

func TestRankingAndNeighbors(t *testing.T) {
	t.Parallel()
	donors := []store.Donor{{ID: 1, Name: "a"}, {ID: 2, Name: "b", BaselineScore: 100}, {ID: 3, Name: "c"}}
	now := date(2, 0)
	view := stats.Build(rules(), donors, []store.Sample{
		{DonorID: 1, At: now, Score: 50, WUs: 1},
		{DonorID: 2, At: now, Score: 400, WUs: 2},
		{DonorID: 3, At: now, Score: 10, WUs: 1},
	}, nil, now)

	got := view.Overview.Standings
	if got[0].Name != "b" || got[0].Points != 300 || got[2].Name != "c" {
		t.Fatalf("standings = %+v", got)
	}
	a, ok := view.Detail("A")
	if !ok || a.Ahead.Name != "b" || a.Behind.Name != "c" || a.Field != 3 {
		t.Errorf("detail = %+v", a)
	}
	if view.Overview.Points != 360 || view.Overview.Active != 3 {
		t.Errorf("overview = %+v", view.Overview)
	}
}

func TestRankChangeOver24h(t *testing.T) {
	t.Parallel()
	donors := []store.Donor{{ID: 1, Name: "a"}, {ID: 2, Name: "b"}}
	now := date(3, 0)
	view := stats.Build(rules(), donors, []store.Sample{
		{DonorID: 1, At: date(2, 0), Score: 100},
		{DonorID: 2, At: date(2, 0), Score: 50},
		{DonorID: 2, At: now, Score: 300},
	}, nil, now)

	b, _ := view.Detail("b")
	if b.Standing.Rank != 1 || b.Standing.RankChange24h != 1 || b.Standing.Points24h != 250 {
		t.Errorf("b = %+v", b.Standing)
	}
	if len(b.History) != 2 || b.History[0].Rank != 2 || b.History[1].Rank != 1 {
		t.Errorf("history = %+v", b.History)
	}
}

func TestForecastEarlyIsFlaggedLowConfidence(t *testing.T) {
	t.Parallel()
	now := date(2, 0)
	view := stats.Build(rules(), []store.Donor{{ID: 1, Name: "a"}},
		[]store.Sample{{DonorID: 1, At: now, Score: 1000, WUs: 5}}, nil, now)

	f := view.Overview.Standings[0].Forecast
	if f.Confident || f.PointsPerDay != 1000 {
		t.Fatalf("forecast = %+v", f)
	}
	if f.Final != 36000 || f.Low != 18500 || f.High != 53500 {
		t.Errorf("forecast = %+v", f)
	}
}

func TestForecastUsesDailySpreadOnceThreeDaysFinish(t *testing.T) {
	t.Parallel()
	samples := []store.Sample{
		{DonorID: 1, At: date(1, 12), Score: 100, WUs: 1},
		{DonorID: 1, At: date(2, 12), Score: 300, WUs: 2},
		{DonorID: 1, At: date(3, 12), Score: 600, WUs: 3},
		{DonorID: 1, At: date(4, 12), Score: 1000, WUs: 4},
		{DonorID: 1, At: date(5, 12), Score: 1400, WUs: 5},
	}
	view := stats.Build(rules(), []store.Donor{{ID: 1, Name: "a"}}, samples, nil, date(5, 12))

	st := view.Overview.Standings[0]
	f := st.Forecast
	if !f.Confident || f.PointsPerDay != 400 {
		t.Fatalf("forecast = %+v", f)
	}
	if f.Low >= f.Final || f.High < f.Final {
		t.Errorf("band = %+v", f)
	}
	if st.Eligibility.ActiveDays != 5 || !st.Eligibility.Reachable {
		t.Errorf("eligibility = %+v", st.Eligibility)
	}
}

func TestInexactBaselineDoesNotInventFirstDayPoints(t *testing.T) {
	t.Parallel()
	view := stats.Build(rules(), []store.Donor{{ID: 1, Name: "a", BaselineScore: 500, BaselineWUs: 5, BaselineError: 300}},
		[]store.Sample{{DonorID: 1, At: date(2, 0), Score: 500, WUs: 5}}, nil, date(2, 0))
	st := view.Overview.Standings[0]
	if st.Points != 0 || st.Forecast.PointsPerDay != 0 || st.Eligibility.ActiveDays != 0 {
		t.Errorf("standing = %+v", st)
	}
}

func TestReachableFailsWhenTooManyDaysMissed(t *testing.T) {
	t.Parallel()
	now := date(9, 12)
	view := stats.Build(rules(), []store.Donor{{ID: 1, Name: "a"}},
		[]store.Sample{{DonorID: 1, At: now, Score: 1}}, nil, now)
	e := view.Overview.Standings[0].Eligibility
	if e.Reachable || e.RemainingDays != 28 {
		t.Errorf("eligibility = %+v", e)
	}
}

func TestTeamsAddAcrossMonths(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)
	view := stats.Build(rules(), []store.Donor{{ID: 1, Name: "a"}},
		[]store.Sample{{DonorID: 1, At: now, Score: 1}},
		[]store.TeamSample{
			{Team: 1, At: now.Add(-48 * time.Hour), Month: 202611, Name: "x", Credit: 90},
			{Team: 1, At: now.Add(-48 * time.Hour), Month: 202612, Name: "x", Credit: 0},
			{Team: 1, At: now, Month: 202611, Name: "x", Credit: 100},
			{Team: 1, At: now, Month: 202612, Name: "x", Credit: 7},
			{Team: 2, At: now, Month: 202611, Name: "y", Credit: 200},
		}, now)

	teams := view.Overview.Teams
	if len(teams) != 2 || teams[0].Team != 2 || teams[1].Credit != 107 || teams[1].Credit24h != 17 {
		t.Errorf("teams = %+v", teams)
	}
}

func TestSamplesAfterEndStayInLastDay(t *testing.T) {
	t.Parallel()
	r := rules()
	view := stats.Build(r, []store.Donor{{ID: 1, Name: "a"}}, []store.Sample{
		{DonorID: 1, At: r.End.Add(-time.Hour), Score: 10, WUs: 1},
		{DonorID: 1, At: r.End.Add(10 * time.Minute), Score: 20, WUs: 2},
	}, nil, r.End.Add(time.Hour))
	a, _ := view.Detail("a")
	if a.Daily[len(a.Daily)-1].Day != "2026-12-06" {
		t.Errorf("daily = %+v", a.Daily)
	}
}

func TestIdleDaysLowerTheForecastSpread(t *testing.T) {
	t.Parallel()
	samples := make([]store.Sample, 0, 4)
	score := int64(0)
	for _, d := range []int{2, 4, 6, 8} {
		score += 1000
		samples = append(samples, store.Sample{DonorID: 1, At: date(d, 12), Score: score, WUs: score})
	}
	view := stats.Build(rules(), []store.Donor{{ID: 1, Name: "a"}}, samples, nil, date(8, 12))
	a, _ := view.Detail("a")
	if len(a.Daily) != 8 || a.Daily[0].Points != 666 || a.Daily[2].Points != 0 {
		t.Fatalf("daily = %+v", a.Daily)
	}
	if a.Standing.Forecast.Low >= a.Standing.Forecast.High {
		t.Errorf("forecast = %+v", a.Standing.Forecast)
	}
}

func TestLateFirstSampleLeavesDaysReachable(t *testing.T) {
	t.Parallel()
	now := date(20, 12)
	view := stats.Build(rules(), []store.Donor{{ID: 1, Name: "a"}},
		[]store.Sample{{DonorID: 1, At: now, Score: 5000, WUs: 40}}, nil, now)
	e := view.Overview.Standings[0].Eligibility
	if e.ActiveDays != 1 || e.HiddenDays != 19 || !e.Reachable {
		t.Errorf("eligibility = %+v", e)
	}
}

func TestFirstEventDayCountsEverything(t *testing.T) {
	t.Parallel()
	now := date(1, 22)
	view := stats.Build(rules(), []store.Donor{{ID: 1, Name: "a"}},
		[]store.Sample{{DonorID: 1, At: now, Score: 100}},
		[]store.TeamSample{{Team: 1, At: now, Month: 202611, Name: "x", Credit: 40}}, now)
	if a := view.Overview.Standings[0]; view.Overview.Partial24h || a.Points24h != 100 {
		t.Errorf("standing = %+v", a)
	}
	if got := view.Overview.Teams[0].Credit24h; got != 40 {
		t.Errorf("team 24h = %d", got)
	}
}

func TestWindowBeforeTrackingIsEstimated(t *testing.T) {
	t.Parallel()
	view := stats.Build(rules(), []store.Donor{{ID: 1, Name: "a"}}, []store.Sample{
		{DonorID: 1, At: date(2, 0), Score: 100}, {DonorID: 1, At: date(2, 6), Score: 150},
	}, []store.TeamSample{{Team: 1, At: date(2, 0), Month: 202611, Name: "x", Credit: 100}}, date(2, 6))
	if a := view.Overview.Standings[0]; !view.Overview.Partial24h || a.Points24h != 125 {
		t.Errorf("standing = %+v", a)
	}
}

func TestMoveComparesWithFirstRankings(t *testing.T) {
	t.Parallel()
	view := stats.Build(rules(), []store.Donor{{ID: 1, Name: "a"}, {ID: 2, Name: "b"}}, []store.Sample{
		{DonorID: 1, At: date(1, 22), Score: 100},
		{DonorID: 2, At: date(1, 22), Score: 50},
		{DonorID: 2, At: date(1, 23), Score: 300},
	}, nil, date(1, 23))
	if b, _ := view.Detail("b"); b.Standing.RankChange24h != 1 {
		t.Errorf("b = %+v", b.Standing)
	}
}

func TestLateExactHistorySpreadsEarlierCredit(t *testing.T) {
	t.Parallel()
	now := date(2, 12)
	view := stats.Build(rules(), []store.Donor{{ID: 1, Name: "a"}}, []store.Sample{
		{DonorID: 1, At: date(2, 0), Score: 960, WUs: 96},
		{DonorID: 1, At: now, Score: 1000, WUs: 100},
	}, nil, now)
	a, _ := view.Detail("a")
	if len(a.Daily) != 2 || a.Daily[0].Points != 960 || a.Daily[0].WUs != 96 || a.Daily[1].Points != 40 || a.Daily[1].WUs != 4 {
		t.Errorf("daily = %+v", a.Daily)
	}
	if e := a.Standing.Eligibility; e.ActiveDays != 1 || e.HiddenDays != 1 {
		t.Errorf("eligibility = %+v", e)
	}
}
