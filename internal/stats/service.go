package stats

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"time"
	_ "time/tzdata"

	"github.com/casmith/openshelf/internal/db"
)

const topGenres = 8

// Period is a calendar-year window in the viewer's timezone, or all time when Year is 0.
type Period struct {
	Key  string // "this-year", "last-year", "all", or "year"
	Year int
	Loc  *time.Location
}

func (p Period) AllTime() bool { return p.Year == 0 }

func (p Period) Label() string {
	switch p.Key {
	case "this-year":
		return fmt.Sprintf("This year (%d)", p.Year)
	case "last-year":
		return fmt.Sprintf("Last year (%d)", p.Year)
	case "all":
		return "All time"
	}
	return strconv.Itoa(p.Year)
}

// dates bounds finished_on, a calendar date with no timezone.
func (p Period) dates() (from, to *time.Time) {
	if p.AllTime() {
		return nil, nil
	}
	f := time.Date(p.Year, 1, 1, 0, 0, 0, 0, time.UTC)
	t := time.Date(p.Year+1, 1, 1, 0, 0, 0, 0, time.UTC)
	return &f, &t
}

// instants bounds timestamps at the viewer's local midnight.
func (p Period) instants() (from, to *time.Time) {
	if p.AllTime() {
		return nil, nil
	}
	f := time.Date(p.Year, 1, 1, 0, 0, 0, 0, p.Loc)
	t := time.Date(p.Year+1, 1, 1, 0, 0, 0, 0, p.Loc)
	return &f, &t
}

func location(tz string) *time.Location {
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	return time.UTC
}

// ParsePeriod reads ?period=this-year|last-year|all or ?year=YYYY, defaulting to this year.
func ParsePeriod(v url.Values, now time.Time, tz string) Period {
	loc := location(tz)
	thisYear := now.In(loc).Year()
	if y, err := strconv.Atoi(v.Get("year")); err == nil && y >= 1900 && y <= 9999 {
		return Period{Key: "year", Year: y, Loc: loc}
	}
	switch v.Get("period") {
	case "last-year":
		return Period{Key: "last-year", Year: thisYear - 1, Loc: loc}
	case "all":
		return Period{Key: "all", Loc: loc}
	}
	return Period{Key: "this-year", Year: thisYear, Loc: loc}
}

var Kinds = []db.MediaKind{db.MediaKindGame, db.MediaKindBook, db.MediaKindFilm, db.MediaKindShow}

type KindCount struct {
	Kind  db.MediaKind
	Count int
}

// Bucket is one bar in a time series: a month, or a year for all time.
type Bucket struct {
	Label  string
	Long   string
	ByKind map[db.MediaKind]int
	Total  int
}

type RatingBar struct {
	Label string
	Count int
}

type Genre struct {
	Name  string
	Count int
}

type Stats struct {
	Period       Period
	Display      db.RatingDisplay
	Completed    int
	Undated      int // all-time completions without a date, which the timeline can't place
	ByKind       []KindCount
	Timeline     []Bucket
	RatedCount   int
	AvgRating    *int16
	Distribution []RatingBar
	Pages        int
	Minutes      int
	Genres       []Genre
}

func (s Stats) Hours() int { return int(math.Round(float64(s.Minutes) / 60)) }

func (s Stats) Empty() bool {
	return s.Completed == 0 && s.RatedCount == 0 && s.Pages == 0 && s.Minutes == 0
}

// Load runs one query per widget, each scoped to the viewer.
func Load(ctx context.Context, q *db.Queries, viewer db.User, p Period) (Stats, error) {
	fromDate, toDate := p.dates()
	fromTS, toTS := p.instants()
	s := Stats{Period: p, Display: viewer.RatingDisplay}

	kinds, err := q.StatsCompletedByKind(ctx, db.StatsCompletedByKindParams{UserID: viewer.ID, FromDate: fromDate, ToDate: toDate})
	if err != nil {
		return s, err
	}
	counts := map[db.MediaKind]int{}
	for _, k := range kinds {
		counts[k.Kind] = int(k.Items)
		s.Completed += int(k.Items)
		s.Undated += int(k.Undated)
	}
	for _, k := range Kinds {
		s.ByKind = append(s.ByKind, KindCount{Kind: k, Count: counts[k]})
	}

	months, err := q.StatsCompletionsByMonth(ctx, db.StatsCompletionsByMonthParams{UserID: viewer.ID, FromDate: fromDate, ToDate: toDate})
	if err != nil {
		return s, err
	}
	s.Timeline = timeline(p, months)

	ratings, err := q.StatsRatingCounts(ctx, db.StatsRatingCountsParams{UserID: viewer.ID, FromTs: fromTS, ToTs: toTS})
	if err != nil {
		return s, err
	}
	s.RatedCount, s.AvgRating, s.Distribution = ratingSummary(viewer.RatingDisplay, ratings)

	prog, err := q.StatsProgressTotals(ctx, db.StatsProgressTotalsParams{UserID: viewer.ID, FromDate: fromDate, ToDate: toDate, FromTs: fromTS, ToTs: toTS})
	if err != nil {
		return s, err
	}
	s.Pages = int(math.Round(prog.Pages))
	s.Minutes = int(math.Round(prog.Minutes))

	genres, err := q.StatsTopGenres(ctx, db.StatsTopGenresParams{UserID: viewer.ID, FromDate: fromDate, ToDate: toDate, Lim: topGenres})
	if err != nil {
		return s, err
	}
	for _, g := range genres {
		s.Genres = append(s.Genres, Genre{Name: g.Genre, Count: int(g.Items)})
	}
	return s, nil
}

// timeline yields 12 months for a year, or one bucket per year (first to last with data) for all time.
func timeline(p Period, rows []db.StatsCompletionsByMonthRow) []Bucket {
	var out []Bucket
	index := map[int]int{}
	add := func(key int, label, long string) {
		index[key] = len(out)
		out = append(out, Bucket{Label: label, Long: long, ByKind: map[db.MediaKind]int{}})
	}
	if p.AllTime() {
		if len(rows) == 0 {
			return nil
		}
		first, last := rows[0].Month.Year(), rows[len(rows)-1].Month.Year()
		for y := first; y <= last; y++ {
			add(y, strconv.Itoa(y), strconv.Itoa(y))
		}
	} else {
		for m := time.January; m <= time.December; m++ {
			add(int(m), m.String()[:3], fmt.Sprintf("%s %d", m, p.Year))
		}
	}
	for _, r := range rows {
		key := int(r.Month.Month())
		if p.AllTime() {
			key = r.Month.Year()
		}
		i, ok := index[key]
		if !ok {
			continue
		}
		out[i].ByKind[r.Kind] += int(r.Items)
		out[i].Total += int(r.Items)
	}
	return out
}

// ratingSummary converts raw 0–100 counts into the viewer's scale.
func ratingSummary(display db.RatingDisplay, rows []db.StatsRatingCountsRow) (n int, avg *int16, bars []RatingBar) {
	var labels []string
	bucket := map[string]int{}
	var zero int
	switch display {
	case db.RatingDisplayTen:
		for i := 1; i <= 10; i++ {
			labels = append(labels, strconv.Itoa(i))
		}
	case db.RatingDisplayHundred:
		for i := 0; i < 10; i++ {
			hi := i*10 + 9
			if i == 9 {
				hi = 100
			}
			labels = append(labels, fmt.Sprintf("%d–%d", i*10, hi))
		}
	default:
		for i := 1; i <= 10; i++ {
			labels = append(labels, starLabel(float64(i)/2))
		}
	}
	sum := 0
	for _, r := range rows {
		c := int(r.N)
		n += c
		sum += int(r.Rating) * c
		var key string
		switch display {
		case db.RatingDisplayTen:
			v := int(math.Round(float64(r.Rating) / 10))
			if v == 0 {
				zero += c
				continue
			}
			key = strconv.Itoa(v)
		case db.RatingDisplayHundred:
			key = labels[min(int(r.Rating)/10, 9)]
		default:
			v := math.Round(float64(r.Rating)/20*2) / 2
			if v == 0 {
				zero += c
				continue
			}
			key = starLabel(v)
		}
		bucket[key] += c
	}
	if zero > 0 {
		bars = append(bars, RatingBar{Label: "0", Count: zero})
	}
	for _, l := range labels {
		bars = append(bars, RatingBar{Label: l, Count: bucket[l]})
	}
	if n > 0 {
		a := int16(math.Round(float64(sum) / float64(n)))
		avg = &a
	}
	return n, avg, bars
}

func starLabel(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64) + "★"
}
