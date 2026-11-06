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
