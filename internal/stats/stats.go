// Package stats turns stored history into standings and forecasts.
package stats

import (
	"cmp"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/USA-RedDragon/fah-dashboard/internal/store"
)

const (
	day            = 24 * time.Hour
	minRateSpan    = 6 * time.Hour
	minForecastLen = 3
	lowQuantile    = 0.2
	highQuantile   = 0.8
	wideBand       = 0.5
	maxChartPoints = 240
)

// Rules are the event settings used to score donors.
type Rules struct {
	Team      int
	Start     time.Time
	End       time.Time
	MinDays   int
	MinPoints int64
	MinWUs    int64
}

// Forecast is the expected final score for a donor.
type Forecast struct {
	PointsPerDay float64 `json:"pointsPerDay"`
	Final        int64   `json:"final"`
	Low          int64   `json:"low"`
	High         int64   `json:"high"`
	FinalRank    int     `json:"finalRank"`
	Confident    bool    `json:"confident"`
}

// Eligibility tracks a donor's progress to the prize minimums.
type Eligibility struct {
	ActiveDays    int  `json:"activeDays"`
	HiddenDays    int  `json:"hiddenDays"`
	RemainingDays int  `json:"remainingDays"`
	Reachable     bool `json:"reachable"`
	Met           bool `json:"met"`
}

// Standing is a donor's place on the leaderboard.
type Standing struct {
	Rank          int         `json:"rank"`
	RankChange24h int         `json:"rankChange24h"`
	ID            int64       `json:"id"`
	Name          string      `json:"name"`
	Points        int64       `json:"points"`
	PointsError   int64       `json:"pointsError"`
	WUs           int64       `json:"wus"`
	Points24h     int64       `json:"points24h"`
	Forecast      Forecast    `json:"forecast"`
	Eligibility   Eligibility `json:"eligibility"`
}

// Point is one position on a donor's history.
type Point struct {
	At     time.Time `json:"at"`
	Points int64     `json:"points"`
	Rank   int       `json:"rank"`
}

// DailyPoints is a donor's output on one UTC day.
type DailyPoints struct {
	Day    string `json:"day"`
	Points int64  `json:"points"`
	WUs    int64  `json:"wus"`
}

// DonorDetail is everything the donor page shows.
type DonorDetail struct {
	Standing Standing      `json:"standing"`
	Field    int           `json:"field"`
	Ahead    *Standing     `json:"ahead"`
	Behind   *Standing     `json:"behind"`
	History  []Point       `json:"history"`
	Daily    []DailyPoints `json:"daily"`
}

// TeamStanding is a team's place in the event window.
type TeamStanding struct {
	Rank      int    `json:"rank"`
	Team      int    `json:"team"`
	Name      string `json:"name"`
	Credit    int64  `json:"credit"`
	WUs       int64  `json:"wus"`
	Credit24h int64  `json:"credit24h"`
}

// Overview is everything the main page shows.
type Overview struct {
	Now        time.Time      `json:"now"`
	Team       int            `json:"team"`
	MinDays    int            `json:"minDays"`
	MinPoints  int64          `json:"minPoints"`
	MinWUs     int64          `json:"minWus"`
	LastSample time.Time      `json:"lastSample"`
	Partial24h bool           `json:"partial24h"`
	Tracking   time.Time      `json:"tracking"`
	Unresolved []string       `json:"unresolved"`
	Start      time.Time      `json:"start"`
	End        time.Time      `json:"end"`
	Donors     int            `json:"donors"`
	Active     int            `json:"active"`
	Points     int64          `json:"points"`
	WUs        int64          `json:"wus"`
	Standings  []Standing     `json:"standings"`
	Teams      []TeamStanding `json:"teams"`
}

// View is an immutable result of Build.
type View struct {
	Overview Overview
	details  map[int64]DonorDetail
	byName   map[string]int64
}

// Detail finds a donor by name, ignoring case.
func (v *View) Detail(name string) (DonorDetail, bool) {
	id, ok := v.byName[strings.ToLower(name)]
	if !ok {
		return DonorDetail{}, false
	}
	d, ok := v.details[id]
	return d, ok
}

type series struct {
	donor   store.Donor
	samples []store.Sample
}

func (s series) points(v store.Sample) int64 {
	return max(v.Score-s.donor.BaselineScore, 0)
}

func (s series) wus(v store.Sample) int64 {
	return max(v.WUs-s.donor.BaselineWUs, 0)
}

func (s series) at(t time.Time) (store.Sample, bool) {
	i := sort.Search(len(s.samples), func(i int) bool { return s.samples[i].At.After(t) })
	if i == 0 {
		return store.Sample{}, false
	}
	return s.samples[i-1], true
}

// Build computes the full view at time now.
func Build(rules Rules, donors []store.Donor, samples []store.Sample, teams []store.TeamSample, now time.Time) *View {
	byDonor := map[int64]*series{}
	for _, d := range donors {
		byDonor[d.ID] = &series{donor: d}
	}
	var last time.Time
	var first time.Time
	for _, v := range samples {
		if v.At.After(rules.End) {
			v.At = rules.End.Add(-time.Second)
		}
		if s, ok := byDonor[v.DonorID]; ok {
			if first.IsZero() || v.At.Before(first) {
				first = v.At
			}
			s.samples = append(s.samples, v)
			if v.At.After(last) {
				last = v.At
			}
		}
	}

	all := make([]series, 0, len(byDonor))
	for _, s := range byDonor {
		if len(s.samples) > 0 {
			all = append(all, *s)
		}
	}

	standings := make([]Standing, 0, len(all))
	for _, s := range all {
		standings = append(standings, standing(rules, s, last))
	}
	rankStandings(standings)
	applyRankChange(standings, all, last.Add(-day))
	applyFinalRanks(standings)

	view := &View{
		Overview: Overview{
			Now: now, Team: rules.Team, MinDays: rules.MinDays, MinPoints: rules.MinPoints, MinWUs: rules.MinWUs,
			LastSample: last, Start: rules.Start, End: rules.End,
			Partial24h: last.Sub(first) < day,
			Tracking:   first,
			Donors:     len(standings), Standings: standings, Teams: teamStandings(teams, last),
		},
		details: make(map[int64]DonorDetail, len(standings)),
		byName:  make(map[string]int64, len(standings)),
	}

	seriesByID := make(map[int64]series, len(all))
	for _, s := range all {
		seriesByID[s.donor.ID] = s
	}
	histories := rankHistories(all)
	for i := range standings {
		st := standings[i]
		view.Overview.Points += st.Points
		view.Overview.WUs += st.WUs
		if st.Points > 0 {
			view.Overview.Active++
		}
		detail := DonorDetail{
			Standing: st,
			Field:    len(standings),
			History:  histories[st.ID],
			Daily:    daily(seriesByID[st.ID], last),
		}
		if i > 0 {
			ahead := standings[i-1]
			detail.Ahead = &ahead
		}
		if i+1 < len(standings) {
			behind := standings[i+1]
			detail.Behind = &behind
		}
		view.details[st.ID] = detail
		view.byName[strings.ToLower(st.Name)] = st.ID
	}
	return view
}

func standing(rules Rules, s series, last time.Time) Standing {
	latest := s.samples[len(s.samples)-1]
	st := Standing{
		ID:          s.donor.ID,
		Name:        s.donor.Name,
		Points:      s.points(latest),
		PointsError: s.donor.BaselineError,
		WUs:         s.wus(latest),
	}
	if old, ok := s.at(last.Add(-day)); ok {
		st.Points24h = st.Points - s.points(old)
	} else {
		st.Points24h = st.Points - s.points(s.samples[0])
	}
	st.Forecast = forecast(rules, s, st.Points, last)
	st.Eligibility = eligibility(rules, s, st, last)
	return st
}

// forecast projects from recent daily output. The trailing day is the central
// rate, and the 20th and 80th percentile days bound it. With fewer than three
// finished days the band is a flat 50% and Confident is false.
func forecast(rules Rules, s series, points int64, last time.Time) Forecast {
	remaining := rules.End.Sub(last)
	if remaining < 0 {
		remaining = 0
	}
	days := remaining.Hours() / 24

	rate := currentRate(rules, s, points, last)
	finished := finishedDays(s, last)
	f := Forecast{PointsPerDay: rate, Confident: len(finished) >= minForecastLen}

	low, high := rate*(1-wideBand), rate*(1+wideBand)
	if f.Confident {
		low = quantile(finished, lowQuantile)
		high = quantile(finished, highQuantile)
		low = math.Min(low, rate)
		high = math.Max(high, rate)
	}

	f.Final = points + int64(rate*days)
	f.Low = points + int64(low*days)
	f.High = points + int64(high*days)
	return f
}

func currentRate(rules Rules, s series, points int64, last time.Time) float64 {
	if old, ok := s.at(last.Add(-day)); ok {
		return float64(points - s.points(old))
	}
	first := s.samples[0]
	if span := last.Sub(first.At); span >= minRateSpan {
		return float64(points-s.points(first)) / span.Hours() * 24
	}
	if span := last.Sub(rules.Start); span > 0 {
		return float64(points) / span.Hours() * 24
	}
	return 0
}

func finishedDays(s series, last time.Time) []float64 {
	today := last.Truncate(day)
	var out []float64
	for _, d := range daily(s, last) {
		start, err := time.Parse(time.DateOnly, d.Day)
		if err == nil && start.Before(today) {
			out = append(out, float64(d.Points))
		}
	}
	slices.Sort(out)
	return out
}

func quantile(sorted []float64, q float64) float64 {
	pos := q * float64(len(sorted)-1)
	lo := int(pos)
	hi := min(lo+1, len(sorted)-1)
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

// daily splits a donor's samples into UTC days from the first sample to last,
// with idle days as zero. Credit lands in the day of the sweep that first
// shows it, so it can differ from the day the work unit finished.
func daily(s series, last time.Time) []DailyPoints {
	if len(s.samples) == 0 {
		return nil
	}
	first := s.samples[0].At.Truncate(day)
	out := make([]DailyPoints, 0, int(last.Sub(first)/day)+1)
	for d := first; !d.After(last); d = d.Add(day) {
		out = append(out, DailyPoints{Day: d.Format(time.DateOnly)})
	}

	var prevPoints, prevWUs int64
	if s.donor.BaselineError != 0 {
		prevPoints, prevWUs = s.points(s.samples[0]), s.wus(s.samples[0])
	}
	for _, v := range s.samples {
		i := min(int(v.At.Truncate(day).Sub(first)/day), len(out)-1)
		points, wus := s.points(v), s.wus(v)
		out[i].Points += points - prevPoints
		out[i].WUs += wus - prevWUs
		prevPoints, prevWUs = points, wus
	}
	return out
}

// eligibility counts days with a credited work unit. The API has no daily
// history, so days before the first sample are unknown. HiddenDays is the most
// of those that can have been active, and ActiveDays is a lower bound.
func eligibility(rules Rules, s series, st Standing, last time.Time) Eligibility {
	e := Eligibility{}
	days := daily(s, last)
	for _, d := range days {
		if d.WUs > 0 {
			e.ActiveDays++
		}
	}

	today := last.Truncate(day)
	firstDay := s.samples[0].At.Truncate(day)
	before := max(int(firstDay.Sub(rules.Start.Truncate(day))/day), 0)
	if s.donor.BaselineError == 0 {
		e.HiddenDays = int(max(min(s.wus(s.samples[0])-1, int64(before)), 0))
	} else {
		e.HiddenDays = before
		if days[0].WUs == 0 {
			e.HiddenDays++
		}
	}

	if left := rules.End.Sub(today); left > 0 {
		e.RemainingDays = int(math.Ceil(left.Hours() / 24))
		if days[len(days)-1].WUs > 0 {
			e.RemainingDays--
		}
	}
	e.Reachable = e.ActiveDays+e.HiddenDays+e.RemainingDays >= rules.MinDays
	e.Met = e.ActiveDays >= rules.MinDays && st.Points >= rules.MinPoints && st.WUs >= rules.MinWUs
	return e
}

func rankStandings(st []Standing) {
	slices.SortFunc(st, func(a, b Standing) int {
		return cmp.Or(cmp.Compare(b.Points, a.Points), strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)))
	})
	for i := range st {
		st[i].Rank = i + 1
	}
}

func applyRankChange(st []Standing, all []series, then time.Time) {
	type row struct {
		id     int64
		name   string
		points int64
	}
	rows := make([]row, 0, len(all))
	for _, s := range all {
		if v, ok := s.at(then); ok {
			rows = append(rows, row{s.donor.ID, s.donor.Name, s.points(v)})
		}
	}
	slices.SortFunc(rows, func(a, b row) int {
		return cmp.Or(cmp.Compare(b.points, a.points), strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name)))
	})
	old := make(map[int64]int, len(rows))
	for i, r := range rows {
		old[r.id] = i + 1
	}
	for i := range st {
		if r, ok := old[st[i].ID]; ok {
			st[i].RankChange24h = r - st[i].Rank
		}
	}
}

func applyFinalRanks(st []Standing) {
	order := make([]int, len(st))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		return cmp.Compare(st[b].Forecast.Final, st[a].Forecast.Final)
	})
	for rank, i := range order {
		st[i].Forecast.FinalRank = rank + 1
	}
}

// rankHistories replays every sample time so a donor's rank line accounts for
// everyone else who moved at that time.
func rankHistories(all []series) map[int64][]Point {
	seen := map[time.Time]bool{}
	var times []time.Time
	for _, s := range all {
		for _, v := range s.samples {
			if !seen[v.At] {
				seen[v.At] = true
				times = append(times, v.At)
			}
		}
	}
	slices.SortFunc(times, time.Time.Compare)
	if len(times) > maxChartPoints {
		step := float64(len(times)-1) / float64(maxChartPoints-1)
		picked := make([]time.Time, 0, maxChartPoints)
		for i := range maxChartPoints {
			picked = append(picked, times[int(float64(i)*step)])
		}
		picked[len(picked)-1] = times[len(times)-1]
		times = picked
	}

	out := make(map[int64][]Point, len(all))
	type row struct {
		id     int64
		name   string
		points int64
	}
	for _, t := range times {
		rows := make([]row, 0, len(all))
		for _, s := range all {
			if v, ok := s.at(t); ok {
				rows = append(rows, row{s.donor.ID, s.donor.Name, s.points(v)})
			}
		}
		slices.SortFunc(rows, func(a, b row) int {
			return cmp.Or(cmp.Compare(b.points, a.points), strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name)))
		})
		for i, r := range rows {
			out[r.id] = append(out[r.id], Point{At: t, Points: r.points, Rank: i + 1})
		}
	}
	return out
}

// teamStandings adds each team's latest credit across the months of the
// event, which keeps totals correct when the monthly board rolls over.
func teamStandings(samples []store.TeamSample, last time.Time) []TeamStanding {
	type key struct {
		team, month int
	}
	latest := map[key]store.TeamSample{}
	dayAgo := map[key]store.TeamSample{}
	then := last.Add(-day)
	for _, v := range samples {
		k := key{v.Team, v.Month}
		latest[k] = v
		if _, ok := dayAgo[k]; !ok || !v.At.After(then) {
			dayAgo[k] = v
		}
	}

	byTeam := map[int]*TeamStanding{}
	for k, v := range latest {
		t, ok := byTeam[k.team]
		if !ok {
			t = &TeamStanding{Team: k.team, Name: v.Name}
			byTeam[k.team] = t
		}
		t.Credit += v.Credit
		t.WUs += v.WUs
		t.Credit24h += v.Credit - dayAgo[k].Credit
	}

	out := make([]TeamStanding, 0, len(byTeam))
	for _, t := range byTeam {
		out = append(out, *t)
	}
	slices.SortFunc(out, func(a, b TeamStanding) int {
		return cmp.Or(cmp.Compare(b.Credit, a.Credit), cmp.Compare(a.Team, b.Team))
	})
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}
