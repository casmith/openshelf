package library

import (
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/suggestions"
	"github.com/casmith/openshelf/internal/web/form"
	"github.com/casmith/openshelf/internal/web/ui"
)

func kindPlural(k db.MediaKind) string {
	for _, n := range ui.LibraryNav {
		if n.Key == string(k) {
			return n.Label
		}
	}
	return string(k)
}

func libraryTitle(k *db.MediaKind) string {
	if k == nil {
		return "Library"
	}
	return kindPlural(*k)
}

func ownershipLabel(o db.Ownership) string {
	switch o {
	case db.OwnershipOwned:
		return "Owned"
	case db.OwnershipBorrowed:
		return "Borrowed"
	}
	return "Not owned"
}

func visibilityLabel(v db.Visibility) string {
	switch v {
	case db.VisibilityInstance:
		return "Instance"
	case db.VisibilityPublic:
		return "Public"
	}
	return "Private"
}

var formatLabels = map[db.EditionFormat]string{
	db.EditionFormatEbook:     "eBook",
	db.EditionFormatBluray:    "Blu-ray",
	db.EditionFormatUhdBluray: "4K Blu-ray",
	db.EditionFormatDvd:       "DVD",
}

func formatLabel(f db.EditionFormat) string {
	if l, ok := formatLabels[f]; ok {
		return l
	}
	s := string(f)
	return strings.ToUpper(s[:1]) + s[1:]
}

func statusChip(e db.LibraryListEntriesRow) string {
	switch {
	case e.InProgress:
		return "In progress"
	case e.Finished:
		return "Finished"
	case e.InBacklog:
		return "Backlog"
	}
	return ""
}

func entrySubtitle(e db.LibraryListEntriesRow) string {
	if e.PlatformShortName != nil {
		return *e.PlatformShortName
	}
	if e.PlatformName != nil {
		return *e.PlatformName
	}
	if e.EditionFormat != nil {
		return formatLabel(*e.EditionFormat)
	}
	if e.ReleaseYear != nil {
		return strconv.Itoa(int(*e.ReleaseYear))
	}
	return ""
}

func editionLabel(title *string, platform, platformShort *string, format *db.EditionFormat, region *string) string {
	var parts []string
	if title != nil {
		parts = append(parts, *title)
	}
	if platformShort != nil {
		parts = append(parts, *platformShort)
	} else if platform != nil {
		parts = append(parts, *platform)
	}
	if format != nil {
		parts = append(parts, formatLabel(*format))
	}
	if region != nil {
		parts = append(parts, *region)
	}
	if len(parts) == 0 {
		return "Edition"
	}
	return strings.Join(parts, " · ")
}

func editionOptions(editions []db.ListEditionsForItemRow) []ui.Option {
	opts := []ui.Option{{Value: "", Label: "No specific edition"}}
	for _, e := range editions {
		label := editionLabel(e.Title, e.PlatformName, e.PlatformShortName, e.Format, e.Region)
		if e.ReleaseDate != nil {
			label += " · " + strconv.Itoa(e.ReleaseDate.Year())
		}
		opts = append(opts, ui.Option{Value: e.PublicID.String(), Label: label})
	}
	return opts
}

var (
	ownershipOptions = []ui.Option{
		{Value: "owned", Label: "Owned"},
		{Value: "borrowed", Label: "Borrowed"},
		{Value: "not_owned", Label: "Not owned"},
	}
	visibilityOptions = []ui.Option{
		{Value: "private", Label: "Private"},
		{Value: "instance", Label: "Instance (signed-in users)"},
		{Value: "public", Label: "Public"},
	}
	statusOptions = []ui.Option{
		{Value: "", Label: "Any status"},
		{Value: "in_progress", Label: "In progress"},
		{Value: "finished", Label: "Finished"},
		{Value: "backlog", Label: "Backlog"},
		{Value: "played", Label: "Played"},
		{Value: "not_played", Label: "Not played"},
	}
	sortOptions = []ui.Option{
		{Value: "title", Label: "Title"},
		{Value: "added", Label: "Date added"},
		{Value: "release", Label: "Release date"},
		{Value: "rating", Label: "Rating"},
	}
	lengthOptions = []ui.Option{
		{Value: "", Label: "Any length"},
		{Value: "under5", Label: "Under 5 h"},
		{Value: "5to15", Label: "5–15 h"},
		{Value: "15to40", Label: "15–40 h"},
		{Value: "40plus", Label: "40 h+"},
		{Value: "none", Label: "No estimate"},
	}
)

func statusOptionsFor(kind *db.MediaKind) []ui.Option {
	if kind == nil || *kind != db.MediaKindBook {
		return statusOptions
	}
	opts := slices.Clone(statusOptions)
	for i, o := range opts {
		switch o.Value {
		case "played":
			opts[i].Label = "Started"
		case "not_played":
			opts[i].Label = "Not started"
		}
	}
	return opts
}

func sortOptionsFor(kind *db.MediaKind) []ui.Option {
	if !gamesOnly(kind) {
		return sortOptions
	}
	return append(slices.Clip(sortOptions), ui.Option{Value: "length", Label: "Length"})
}

func dirOptions(sort string) []ui.Option {
	if sort == "length" {
		return []ui.Option{{Value: "", Label: "Shortest first"}, {Value: "desc", Label: "Longest first"}}
	}
	return []ui.Option{{Value: "", Label: "Default order"}, {Value: "asc", Label: "Ascending"}, {Value: "desc", Label: "Descending"}}
}

// hours mirrors catalog's time-to-beat rounding: nearest half hour below 10 h, whole hours above.
func hours(minutes int32) string {
	h := float64(minutes) / 60
	if h < 10 {
		h = max(math.Round(h*2)/2, 0.5)
		return strconv.FormatFloat(h, 'f', -1, 64) + " h"
	}
	return strconv.Itoa(int(math.Round(h))) + " h"
}

func lengthText(e db.LibraryListEntriesRow) string {
	if e.LengthMinutes == nil {
		return ""
	}
	return hours(*e.LengthMinutes)
}

func filterOwnershipOptions() []ui.Option {
	return append([]ui.Option{{Value: "", Label: "Any ownership"}}, ownershipOptions...)
}

func facetOptions(any string, rows []db.LibraryFacetsRow, format bool) []ui.Option {
	opts := []ui.Option{{Value: "", Label: any}}
	for _, r := range rows {
		label := r.Label
		if format {
			label = formatLabel(db.EditionFormat(r.Value))
		}
		opts = append(opts, ui.Option{Value: r.Value, Label: label})
	}
	return opts
}

func ratingOptions(d db.RatingDisplay) []ui.Option {
	opts := []ui.Option{{Value: "", Label: "Any rating"}, {Value: "rated", Label: "Rated"}, {Value: "unrated", Label: "Unrated"}}
	for _, n := range []int{80, 60, 40} {
		var label string
		switch d {
		case db.RatingDisplayTen:
			label = strconv.Itoa(n/10) + "+ / 10"
		case db.RatingDisplayHundred:
			label = strconv.Itoa(n) + "+ / 100"
		default:
			label = strconv.Itoa(n/20) + "+ stars"
		}
		opts = append(opts, ui.Option{Value: strconv.Itoa(n), Label: label})
	}
	return opts
}

func strOr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func dateValue(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.DateOnly)
}

func ownershipValue(o *db.Ownership) string {
	if o == nil {
		return ""
	}
	return string(*o)
}

func formatValue(f *db.EditionFormat) string {
	if f == nil {
		return ""
	}
	return string(*f)
}

func withQuery(path string, v url.Values) string {
	if s := v.Encode(); s != "" {
		return path + "?" + s
	}
	return path
}

type libraryView struct {
	Path    string
	Filter  Filter
	Page    Page
	Facets  Facets
	Display db.RatingDisplay
	// Suggested counts finish suggestions, linked from the header.
	Suggested suggestions.Tally
}

func (v libraryView) kindTabs() []ui.Tab {
	q := v.Filter.Query()
	for _, k := range []string{"platform", "format", "genre"} {
		q.Del(k)
	}
	tabs := []ui.Tab{{Label: "All", Href: withQuery("/library", q), Active: v.Filter.Kind == nil}}
	for _, n := range ui.LibraryNav {
		tabs = append(tabs, ui.Tab{Label: n.Label, Href: withQuery(n.Href, q), Active: v.Filter.Kind != nil && string(*v.Filter.Kind) == n.Key})
	}
	return tabs
}

func (v libraryView) viewHref(view string) string {
	q := v.Filter.Query()
	q.Set("view", view)
	return withQuery(v.Path, q)
}

func (v libraryView) pageHref(key, cursor string) string {
	if cursor == "" {
		return ""
	}
	q := v.Filter.Query()
	q.Set(key, cursor)
	return withQuery(v.Path, q)
}

func (v libraryView) quickWinsHref() string {
	q := v.Filter.Query()
	q.Set("status", "backlog")
	q.Set("sort", "length")
	q.Del("dir")
	return withQuery(v.Path, q)
}

func (v libraryView) quickWinsActive() bool {
	return v.Filter.Status == "backlog" && v.Filter.Sort == "length" && !v.Filter.Desc
}

func (v libraryView) filtered() bool {
	q := v.Filter.Query()
	for _, k := range []string{"sort", "dir", "per"} {
		q.Del(k)
	}
	return len(q) > 0
}

func entryOwnershipOptions() []ui.Option {
	return append([]ui.Option{{Value: "", Label: "Not set"}}, ownershipOptions...)
}

func entryFormValues(e db.LibraryGetEntryForEditRow) *form.Form {
	f := form.New(nil)
	f.Values.Set("ownership", ownershipValue(e.OwnershipUser))
	if e.Backlog {
		f.Values.Set("backlog", "on")
	}
	f.Values.Set("acquired_on", dateValue(e.AcquiredOn))
	f.Values.Set("tags", strings.Join(e.Tags, ", "))
	f.Values.Set("notes", strOr(e.Notes))
	f.Values.Set("visibility", string(e.Visibility))
	return f
}

func searchPlaceholder(k *db.MediaKind) string {
	switch {
	case k == nil:
		return "Search your library"
	case *k == db.MediaKindGame:
		return "Search games"
	}
	return "Search " + strings.ToLower(kindPlural(*k)) + " by title or creator"
}
