package catalog

import (
	"strconv"
	"strings"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/web/form"
	"github.com/casmith/openshelf/internal/web/ui"
)

var kindOrder = []db.MediaKind{db.MediaKindGame, db.MediaKindBook, db.MediaKindFilm, db.MediaKindShow}

var creditRolesByKind = map[db.MediaKind][]db.CreditRole{
	db.MediaKindGame: {db.CreditRoleDeveloper, db.CreditRolePublisher, db.CreditRoleDirector, db.CreditRoleWriter, db.CreditRoleComposer, db.CreditRoleOther},
	db.MediaKindBook: {db.CreditRoleAuthor, db.CreditRoleNarrator, db.CreditRoleTranslator, db.CreditRoleIllustrator, db.CreditRoleOther},
	db.MediaKindFilm: {db.CreditRoleDirector, db.CreditRoleWriter, db.CreditRoleActor, db.CreditRoleComposer, db.CreditRoleOther},
	db.MediaKindShow: {db.CreditRoleCreator, db.CreditRoleWriter, db.CreditRoleActor, db.CreditRoleDirector, db.CreditRoleComposer, db.CreditRoleOther},
}

var formatsByKind = map[db.MediaKind][]db.EditionFormat{
	db.MediaKindGame: {db.EditionFormatPhysical, db.EditionFormatDigital, db.EditionFormatOther},
	db.MediaKindBook: {db.EditionFormatHardcover, db.EditionFormatPaperback, db.EditionFormatEbook, db.EditionFormatAudiobook, db.EditionFormatOther},
	db.MediaKindFilm: {db.EditionFormatBluray, db.EditionFormatUhdBluray, db.EditionFormatDvd, db.EditionFormatStreaming, db.EditionFormatDigital, db.EditionFormatOther},
	db.MediaKindShow: {db.EditionFormatBluray, db.EditionFormatUhdBluray, db.EditionFormatDvd, db.EditionFormatStreaming, db.EditionFormatDigital, db.EditionFormatOther},
}

var showStatuses = []string{"returning", "ended", "canceled"}

func optStr(f *form.Form, key string, max int) *string {
	f.MaxLen(key, max)
	return f.Optional(key)
}

func optInt32(f *form.Form, key string, max int) *int32 {
	n := f.Int(key, 0, max)
	if n == nil {
		return nil
	}
	v := int32(*n)
	return &v
}

// parseItemForm validates the item form for kind; check f.Valid() afterwards.
func parseItemForm(f *form.Form, kind db.MediaKind) ItemInput {
	f.Required("title")
	f.MaxLen("title", 500)
	in := ItemInput{
		Kind:          kind,
		Title:         f.Get("title"),
		OriginalTitle: optStr(f, "original_title", 500),
		ReleaseDate:   f.Date("release_date"),
		Synopsis:      optStr(f, "synopsis", 10000),
		Genres:        normalizeGenres(f.Get("genres")),
	}
	if y := f.Int("release_year", 1000, 3000); y != nil {
		v := int16(*y)
		in.ReleaseYear = &v
	} else if in.ReleaseDate != nil && f.Error("release_year") == "" {
		v := int16(in.ReleaseDate.Year())
		in.ReleaseYear = &v
	}
	switch kind {
	case db.MediaKindGame:
		in.Franchise = optStr(f, "franchise", 200)
	case db.MediaKindBook:
		in.SeriesName = optStr(f, "series_name", 200)
		if v := f.Get("series_position"); v != "" {
			p, err := strconv.ParseFloat(v, 64)
			if err != nil || p < 0 || p >= 10000 {
				f.Add("series_position", "Must be a number between 0 and 9999.")
			} else {
				in.SeriesPosition = &p
			}
		}
	case db.MediaKindFilm:
		in.RuntimeMinutes = optInt32(f, "runtime_minutes", 10000)
	case db.MediaKindShow:
		in.Network = optStr(f, "network", 200)
		f.OneOf("status", showStatuses...)
		in.Status = f.Optional("status")
		in.SeasonCount = optInt32(f, "season_count", 1000)
		in.EpisodeCount = optInt32(f, "episode_count", 100000)
	}
	names, roles := f.Values["credit_name"], f.Values["credit_role"]
	for i, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		role := db.CreditRoleOther
		if i < len(roles) && db.CreditRole(roles[i]).Valid() {
			role = db.CreditRole(roles[i])
		}
		if len(name) > 200 {
			f.Add("credits", "Creator names must be at most 200 characters.")
			continue
		}
		in.Credits = append(in.Credits, CreditInput{Name: name, Role: role})
	}
	return in
}

// parseEditionForm validates the edition form. Platform resolution happens in the handler.
func parseEditionForm(f *form.Form, kind db.MediaKind) EditionInput {
	in := EditionInput{
		Title:       optStr(f, "title", 500),
		Region:      optStr(f, "region", 50),
		Publisher:   optStr(f, "publisher", 200),
		ReleaseDate: f.Date("release_date"),
	}
	if v := f.Get("format"); v != "" {
		allowed := make([]string, len(formatsByKind[kind]))
		for i, x := range formatsByKind[kind] {
			allowed[i] = string(x)
		}
		f.OneOf("format", allowed...)
		ef := db.EditionFormat(v)
		in.Format = &ef
	}
	switch kind {
	case db.MediaKindBook:
		in.PageCount = optInt32(f, "page_count", 100000)
		in.DurationMinutes = optInt32(f, "duration_minutes", 100000)
	case db.MediaKindFilm, db.MediaKindShow:
		in.DurationMinutes = optInt32(f, "duration_minutes", 100000)
	}
	return in
}

func editionValues(e db.Edition) map[string][]string {
	v := map[string][]string{
		"title":            {str(e.Title)},
		"region":           {str(e.Region)},
		"publisher":        {str(e.Publisher)},
		"release_date":     {dateStr(e.ReleaseDate)},
		"page_count":       {num(e.PageCount)},
		"duration_minutes": {num(e.DurationMinutes)},
		"platform_id":      {num(e.PlatformID)},
	}
	if e.Format != nil {
		v["format"] = []string{string(*e.Format)}
	}
	return v
}

func formatLabel(f db.EditionFormat) string {
	switch f {
	case db.EditionFormatUhdBluray:
		return "4K UHD Blu-ray"
	case db.EditionFormatBluray:
		return "Blu-ray"
	case db.EditionFormatDvd:
		return "DVD"
	case db.EditionFormatEbook:
		return "E-book"
	}
	s := string(f)
	return capitalize(s)
}

func formatOptions(kind db.MediaKind) []ui.Option {
	opts := []ui.Option{{Value: "", Label: "—"}}
	for _, x := range formatsByKind[kind] {
		opts = append(opts, ui.Option{Value: string(x), Label: formatLabel(x)})
	}
	return opts
}

func roleOptions(kind db.MediaKind) []ui.Option {
	var opts []ui.Option
	for _, r := range creditRolesByKind[kind] {
		s := string(r)
		opts = append(opts, ui.Option{Value: s, Label: capitalize(s)})
	}
	return opts
}

func statusOptions() []ui.Option {
	opts := []ui.Option{{Value: "", Label: "—"}}
	for _, s := range showStatuses {
		opts = append(opts, ui.Option{Value: s, Label: capitalize(s)})
	}
	return opts
}

type creditRow struct{ Name, Role string }

// creditRows returns the submitted credits plus blank rows for adding more.
func creditRows(f *form.Form, kind db.MediaKind) []creditRow {
	var rows []creditRow
	names, roles := f.Values["credit_name"], f.Values["credit_role"]
	for i, n := range names {
		if strings.TrimSpace(n) == "" {
			continue
		}
		r := ""
		if i < len(roles) {
			r = roles[i]
		}
		rows = append(rows, creditRow{Name: n, Role: r})
	}
	def := string(creditRolesByKind[kind][0])
	for range 3 {
		rows = append(rows, creditRow{Role: def})
	}
	return rows
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + strings.ReplaceAll(s[1:], "_", " ")
}
