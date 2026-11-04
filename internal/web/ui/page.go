package ui

import "github.com/casmith/openshelf/internal/db"

// Page carries what the layout needs to render around page content.
type Page struct {
	Title string
	Nav   string // active nav key: "home", "library", "journal", "lists", "stats", "discover", or a kind
	User  *db.User
	Flash string
}

type NavItem struct {
	Key, Label, Href, Icon string
}

var MainNav = []NavItem{
	{"home", "Home", "/", "home"},
	{"library", "Library", "/library", "library"},
	{"journal", "Journal", "/journal", "journal"},
	{"lists", "Lists", "/lists", "list"},
	{"stats", "Stats", "/stats", "stats"},
	{"discover", "Discover", "/discover", "search"},
}

var LibraryNav = []NavItem{
	{"game", "Games", "/library/game", "game"},
	{"book", "Books", "/library/book", "book"},
	{"film", "Movies", "/library/film", "film"},
	{"show", "TV", "/library/show", "show"},
}

type Option struct {
	Value, Label string
}

type Tab struct {
	Label, Href string
	Active      bool
}

type CoverCardProps struct {
	Href     string
	Title    string
	Subtitle string
	CoverURL string
	Kind     string
	Chip     string
}

func cx(classes ...string) string {
	out := ""
	for _, c := range classes {
		if c == "" {
			continue
		}
		if out != "" {
			out += " "
		}
		out += c
	}
	return out
}
