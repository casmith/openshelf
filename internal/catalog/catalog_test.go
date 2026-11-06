package catalog

import "testing"

func TestSortTitle(t *testing.T) {
	for in, want := range map[string]string{
		"The Expanse":            "Expanse, The",
		"A Memory Called Empire": "Memory Called Empire, A",
		"Anathem":                "Anathem",
		"The":                    "The",
	} {
		if got := SortTitle(in); got != want {
			t.Errorf("SortTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHours(t *testing.T) {
	for m, want := range map[int32]string{5: "0.5 h", 45: "1 h", 62: "1 h", 149: "2.5 h", 240: "4 h", 580: "9.5 h", 595: "10 h", 610: "10 h", 3840: "64 h"} {
		if got := hours(m); got != want {
			t.Errorf("hours(%d) = %q, want %q", m, got, want)
		}
	}
}
