package ui

import (
	"math"
	"strconv"
)

// Hours rounds to the nearest half hour below 10 h and to whole hours above.
func Hours(minutes int32) string {
	h := float64(minutes) / 60
	if h < 10 {
		h = max(math.Round(h*2)/2, 0.5)
		return strconv.FormatFloat(h, 'f', -1, 64) + " h"
	}
	return strconv.Itoa(int(math.Round(h))) + " h"
}
