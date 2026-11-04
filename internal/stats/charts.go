package stats

import (
	"fmt"
	"math"
	"strconv"

	"github.com/casmith/openshelf/internal/db"
)

// Kind colors are a CVD-validated categorical set, stepped separately for the
// dark surface. Class strings stay literal so Tailwind picks them up.
type kindStyle struct {
	Label, Fill, Stroke, Swatch string
}

var kindStyles = map[db.MediaKind]kindStyle{
	db.MediaKindGame: {"Games", "fill-[#2a78d6] dark:fill-[#3987e5]", "stroke-[#2a78d6] dark:stroke-[#3987e5]", "bg-[#2a78d6] dark:bg-[#3987e5]"},
	db.MediaKindBook: {"Books", "fill-[#eb6834] dark:fill-[#d95926]", "stroke-[#eb6834] dark:stroke-[#d95926]", "bg-[#eb6834] dark:bg-[#d95926]"},
	db.MediaKindFilm: {"Movies", "fill-[#1baf7a] dark:fill-[#199e70]", "stroke-[#1baf7a] dark:stroke-[#199e70]", "bg-[#1baf7a] dark:bg-[#199e70]"},
	db.MediaKindShow: {"TV", "fill-[#eda100] dark:fill-[#c98500]", "stroke-[#eda100] dark:stroke-[#c98500]", "bg-[#eda100] dark:bg-[#c98500]"},
}

func kindLabel(k db.MediaKind) string { return kindStyles[k].Label }

func f2(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

func pct(n, total int) string {
	if total == 0 {
		return "0%"
	}
	return fmt.Sprintf("%d%%", int(math.Round(float64(n)*100/float64(total))))
}

// Donut segments use a circle whose circumference is 100, so dash lengths are percentages.
const donutR = 15.9155

type donutSegment struct {
	Class, DashArray, DashOffset, Title string
}

func donutSegments(s Stats) []donutSegment {
	var out []donutSegment
	nonzero := 0
	for _, k := range s.ByKind {
		if k.Count > 0 {
			nonzero++
		}
	}
	gap := 0.0
	if nonzero > 1 {
		gap = 0.8
	}
	cum := 0.0
	for _, k := range s.ByKind {
		if k.Count == 0 {
			continue
		}
		share := float64(k.Count) * 100 / float64(s.Completed)
		seg := math.Max(share-gap, 0.1)
		out = append(out, donutSegment{
			Class:      kindStyles[k.Kind].Stroke,
			DashArray:  f2(seg) + " " + f2(100-seg),
			DashOffset: f2(25 - cum),
			Title:      fmt.Sprintf("%s: %d (%s)", kindLabel(k.Kind), k.Count, pct(k.Count, s.Completed)),
		})
		cum += share
	}
	return out
}

const (
	chartW    = 640.0
	chartH    = 200.0
	padLeft   = 36.0
	padRight  = 8.0
	padTop    = 10.0
	padBottom = 24.0
	segGap    = 2.0
)

type barRect struct {
	X, Y, W, H, Class, Title string
}

type axisTick struct {
	Y, Label string
}

type axisLabel struct {
	X, Label string
}

type barChart struct {
	Label       string
	Rects       []barRect
	Ticks       []axisTick
	XLabels     []axisLabel
	Left, Right string
	Base        string
}

type series struct {
	Class, Label string
}

// niceStep picks an integer tick step from 1, 2, 5 × 10^k so about four ticks cover max.
func niceStep(max int) int {
	if max <= 4 {
		return 1
	}
	raw := float64(max) / 4
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	for _, m := range []float64{1, 2, 5, 10} {
		if m*mag >= raw {
			return int(m * mag)
		}
	}
	return int(10 * mag)
}

// stackedBars lays out one stacked bar per label; values[i][j] is label i, series j.
func stackedBars(label string, labels, longs []string, ser []series, values [][]int) barChart {
	c := barChart{Label: label, Left: f2(padLeft), Right: f2(chartW - padRight), Base: f2(chartH - padBottom)}
	maxTotal := 0
	for _, row := range values {
		t := 0
		for _, v := range row {
			t += v
		}
		maxTotal = max(maxTotal, t)
	}
	step := niceStep(maxTotal)
	top := max(step, int(math.Ceil(float64(maxTotal)/float64(step)))*step)
	plotH := chartH - padTop - padBottom
	base := chartH - padBottom
	for v := 0; v <= top; v += step {
		c.Ticks = append(c.Ticks, axisTick{Y: f2(base - float64(v)/float64(top)*plotH), Label: strconv.Itoa(v)})
	}
	n := len(labels)
	if n == 0 {
		return c
	}
	slot := (chartW - padLeft - padRight) / float64(n)
	w := math.Min(slot*0.6, 40)
	every := 1
	if n > 16 {
		every = int(math.Ceil(float64(n) / 12))
	}
	for i, l := range labels {
		cx := padLeft + slot*(float64(i)+0.5)
		if i%every == 0 {
			c.XLabels = append(c.XLabels, axisLabel{X: f2(cx), Label: l})
		}
		y := base
		for j, v := range values[i] {
			if v == 0 {
				continue
			}
			h := float64(v) / float64(top) * plotH
			drawn := h
			if y < base {
				drawn = math.Max(h-segGap, 0.5)
			}
			y -= h
			title := fmt.Sprintf("%s: %d", longs[i], v)
			if len(ser) > 1 {
				title = fmt.Sprintf("%s · %s: %d", longs[i], ser[j].Label, v)
			}
			c.Rects = append(c.Rects, barRect{
				X: f2(cx - w/2), Y: f2(y), W: f2(w), H: f2(drawn),
				Class: ser[j].Class, Title: title,
			})
		}
	}
	return c
}

func timelineChart(s Stats) barChart {
	var ser []series
	for _, k := range Kinds {
		ser = append(ser, series{Class: kindStyles[k].Fill, Label: kindLabel(k)})
	}
	var labels, longs []string
	var values [][]int
	for _, b := range s.Timeline {
		labels = append(labels, b.Label)
		longs = append(longs, b.Long)
		row := make([]int, len(Kinds))
		for j, k := range Kinds {
			row[j] = b.ByKind[k]
		}
		values = append(values, row)
	}
	return stackedBars("Completions per "+timelineUnit(s), labels, longs, ser, values)
}

func timelineUnit(s Stats) string {
	if s.Period.AllTime() {
		return "year"
	}
	return "month"
}

func ratingChart(s Stats) barChart {
	ser := []series{{Class: "fill-accent", Label: "Ratings"}}
	var labels []string
	var values [][]int
	for _, b := range s.Distribution {
		labels = append(labels, b.Label)
		values = append(values, []int{b.Count})
	}
	return stackedBars("Rating distribution", labels, labels, ser, values)
}

func genreWidth(g Genre, s Stats) string {
	if len(s.Genres) == 0 || s.Genres[0].Count == 0 {
		return "0"
	}
	return f2(float64(g.Count) * 100 / float64(s.Genres[0].Count))
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
