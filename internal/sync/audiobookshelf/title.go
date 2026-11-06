package audiobookshelf

import (
	"regexp"
	"strings"
)

var editionSuffixes = func() []*regexp.Regexp {
	markers := `(?:un)?abridged(?:\s+edition)?|dramati[sz]ed\s+adaptation|a\s+novel`
	return []*regexp.Regexp{
		regexp.MustCompile(`(?i)\s*[(\[]\s*(?:` + markers + `)\s*[)\]]$`),
		regexp.MustCompile(`(?i)\s*(?:[:,]|\s[-–—])\s*(?:` + markers + `)$`),
	}
}()

// CleanTitle strips trailing edition markers such as "(Unabridged)" or ": A Novel"
// so titles match catalog works.
func CleanTitle(title string) string {
	t := strings.TrimSpace(title)
	for changed := true; changed; {
		changed = false
		for _, re := range editionSuffixes {
			if next := strings.TrimSpace(re.ReplaceAllString(t, "")); next != t && next != "" {
				t, changed = next, true
			}
		}
	}
	return t
}
