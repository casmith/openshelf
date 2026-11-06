package ui

import "testing"

func TestHours(t *testing.T) {
	for m, want := range map[int32]string{5: "0.5 h", 45: "1 h", 62: "1 h", 149: "2.5 h", 240: "4 h", 580: "9.5 h", 595: "10 h", 610: "10 h", 3840: "64 h"} {
		if got := Hours(m); got != want {
			t.Errorf("Hours(%d) = %q, want %q", m, got, want)
		}
	}
}
