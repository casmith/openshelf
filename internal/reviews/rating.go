package reviews

import (
	"errors"
	"math"
	"strconv"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/web/ui"
)

var ErrRatingRange = errors.New("rating must be a whole number from 0 to 100")

// Stars converts a 0–100 rating to half-star steps on a 5-star scale.
func Stars(r int16) float64 { return math.Round(float64(r)/10) / 2 }

// Ten converts a 0–100 rating to a 0–10 scale.
func Ten(r int16) int { return int(math.Round(float64(r) / 10)) }

// FormatRating renders a stored rating as text in the given display scale.
func FormatRating(display db.RatingDisplay, r int16) string {
	switch display {
	case db.RatingDisplayTen:
		return strconv.Itoa(Ten(r)) + "/10"
	case db.RatingDisplayHundred:
		return strconv.Itoa(int(r)) + "/100"
	default:
		return formatStars(r) + "/5"
	}
}

func formatStars(r int16) string { return strconv.FormatFloat(Stars(r), 'f', -1, 64) }

// ParseRating parses a submitted RatingInput value; every scale submits 0–100.
func ParseRating(s string) (*int16, error) {
	if s == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 100 {
		return nil, ErrRatingRange
	}
	v := int16(n)
	return &v, nil
}

// starFill returns 0 (empty), 1 (half), or 2 (full) for the i-th star (0-based).
func starFill(r int16, i int) int {
	return max(0, min(2, int(Stars(r)*2)-i*2))
}

type starOption struct {
	Value string
	Label string
	Half  bool
}

func starOptions() []starOption {
	opts := make([]starOption, 0, 10)
	for v := 10; v <= 100; v += 10 {
		label := strconv.FormatFloat(float64(v)/20, 'f', -1, 64) + " stars"
		if v == 20 {
			label = "1 star"
		}
		opts = append(opts, starOption{Value: strconv.Itoa(v), Label: label, Half: v%20 != 0})
	}
	return opts
}

// snapStars returns the radio value to pre-check for a stored rating.
func snapStars(r *int16) string {
	if r == nil || *r < 5 {
		return ""
	}
	return strconv.Itoa(int(Stars(*r) * 20))
}

func snapTen(r *int16) string {
	if r == nil {
		return ""
	}
	return strconv.Itoa(Ten(*r) * 10)
}

func tenOptions() []ui.Option {
	opts := []ui.Option{{Value: "", Label: "No rating"}}
	for i := 0; i <= 10; i++ {
		opts = append(opts, ui.Option{Value: strconv.Itoa(i * 10), Label: strconv.Itoa(i) + "/10"})
	}
	return opts
}
