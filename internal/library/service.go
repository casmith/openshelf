package library

import (
	"context"
	"encoding/base64"
	"errors"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/casmith/openshelf/internal/activity"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
)

const (
	defaultPerPage = 60
	maxPerPage     = 500
	maxTags        = 20
	maxTagLen      = 40
	maxNotesLen    = 5000
)

var ErrDuplicate = errors.New("library: entry already exists")

var (
	statuses = []string{"in_progress", "finished", "backlog", "played", "not_played"}
	sorts    = []string{"title", "added", "release", "rating", "length"}
)

// lengthBuckets bound the main time-to-beat estimate in minutes as [min, max); max 0 is open.
var lengthBuckets = map[string][2]int32{
	"under5": {0, 5 * 60},
	"5to15":  {5 * 60, 15 * 60},
	"15to40": {15 * 60, 40 * 60},
	"40plus": {40 * 60, 0},
	"none":   {},
}

// gamesOnly reports whether length sorting and filtering apply to this tab.
func gamesOnly(kind *db.MediaKind) bool { return kind == nil || *kind == db.MediaKindGame }

func defaultDesc(sort string) bool { return sort != "title" && sort != "length" }

// Filter is the library page state. It round-trips through the query string.
type Filter struct {
	Kind      *db.MediaKind
	Ownership *db.Ownership
	Status    string
	Platform  string
	Format    *db.EditionFormat
	Genre     string
	Tag       string
	Rating    string // "rated", "unrated", or a minimum 0–100
	Length    string // a lengthBuckets key
	AddedFrom *time.Time
	AddedTo   *time.Time
	Sort      string
	Desc      bool
	View      string
	After     string
	Before    string
	Per       int
}

// ParseFilter reads filter state from a query string; invalid values are ignored.
func ParseFilter(kind *db.MediaKind, v url.Values) Filter {
	f := Filter{Kind: kind, Sort: "title", Per: defaultPerPage}
	get := func(k string) string { return strings.TrimSpace(v.Get(k)) }
	if o := db.Ownership(get("ownership")); o.Valid() {
		f.Ownership = &o
	}
	if s := get("status"); slices.Contains(statuses, s) {
		f.Status = s
	}
	f.Platform = get("platform")
	if fm := db.EditionFormat(get("format")); fm.Valid() {
		f.Format = &fm
	}
	f.Genre = strings.ToLower(get("genre"))
	f.Tag = strings.ToLower(get("tag"))
	if r := get("rating"); r == "rated" || r == "unrated" {
		f.Rating = r
	} else if n, err := strconv.Atoi(r); err == nil && n >= 0 && n <= 100 {
		f.Rating = r
	}
	if t, err := time.Parse(time.DateOnly, get("added_from")); err == nil {
		f.AddedFrom = &t
	}
	if t, err := time.Parse(time.DateOnly, get("added_to")); err == nil {
		f.AddedTo = &t
	}
	if _, ok := lengthBuckets[get("length")]; ok && gamesOnly(kind) {
		f.Length = get("length")
	}
	if s := get("sort"); slices.Contains(sorts, s) && (s != "length" || gamesOnly(kind)) {
		f.Sort = s
	}
	f.Desc = defaultDesc(f.Sort)
	switch get("dir") {
	case "asc":
		f.Desc = false
	case "desc":
		f.Desc = true
	}
	if vw := get("view"); vw == "grid" || vw == "table" {
		f.View = vw
	}
	f.After, f.Before = get("after"), get("before")
	if n, err := strconv.Atoi(get("per")); err == nil && n > 0 && n <= maxPerPage {
		f.Per = n
	}
	return f
}

// Query encodes the filter (without kind, view, or cursors) as URL values.
func (f Filter) Query() url.Values {
	v := url.Values{}
	set := func(k, val string) {
		if val != "" {
			v.Set(k, val)
		}
	}
	if f.Ownership != nil {
		set("ownership", string(*f.Ownership))
	}
	set("status", f.Status)
	set("platform", f.Platform)
	if f.Format != nil {
		set("format", string(*f.Format))
	}
	set("genre", f.Genre)
	set("tag", f.Tag)
	set("rating", f.Rating)
	set("length", f.Length)
	if f.AddedFrom != nil {
		set("added_from", f.AddedFrom.Format(time.DateOnly))
	}
	if f.AddedTo != nil {
		set("added_to", f.AddedTo.Format(time.DateOnly))
	}
	if f.Sort != "title" {
		set("sort", f.Sort)
	}
	if f.Desc != defaultDesc(f.Sort) {
		set("dir", map[bool]string{true: "desc", false: "asc"}[f.Desc])
	}
	if f.Per != defaultPerPage {
		set("per", strconv.Itoa(f.Per))
	}
	return v
}

type cursor struct {
	num  float64
	text string
	id   uuid.UUID
}

func encodeCursor(r db.LibraryListEntriesRow) string {
	raw := strconv.FormatFloat(r.KNum, 'g', -1, 64) + "~" + r.PublicID.String() + "~" + r.KText
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(s string) (cursor, bool) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, false
	}
	parts := strings.SplitN(string(b), "~", 3)
	if len(parts) != 3 {
		return cursor{}, false
	}
	n, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return cursor{}, false
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return cursor{}, false
	}
	return cursor{num: n, id: id, text: parts[2]}, true
}

type Page struct {
	Entries    []db.LibraryListEntriesRow
	NextCursor string
	PrevCursor string
}

// List returns one keyset page of the viewer's library.
func List(ctx context.Context, q *db.Queries, viewer db.User, f Filter) (Page, error) {
	p := db.LibraryListEntriesParams{
		UserID:        viewer.ID,
		Kind:          f.Kind,
		Ownership:     f.Ownership,
		EditionFormat: f.Format,
		AddedFrom:     f.AddedFrom,
		AddedTo:       f.AddedTo,
		Tz:            viewer.Timezone,
		Sort:          f.Sort,
		Lim:           int32(f.Per + 1),
	}
	opt := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	p.Status, p.Platform, p.Genre, p.Tag = opt(f.Status), opt(f.Platform), opt(f.Genre), opt(f.Tag)
	switch f.Rating {
	case "":
	case "rated":
		zero := int32(0)
		p.RatingMin = &zero
	case "unrated":
		p.Unrated = true
	default:
		n, _ := strconv.Atoi(f.Rating)
		m := int32(n)
		p.RatingMin = &m
	}
	if b, ok := lengthBuckets[f.Length]; ok {
		p.LengthFilter = true
		p.NoLength = f.Length == "none"
		if !p.NoLength {
			p.LengthMin = &b[0]
			if b[1] > 0 {
				p.LengthMax = &b[1]
			}
		}
	}
	p.NumDesc = f.Desc
	p.NullKey = math.Inf(1)
	if f.Desc {
		p.NullKey = math.Inf(-1)
	}
	p.TextDesc = f.Sort == "title" && f.Desc

	backward := false
	if c, ok := decodeCursor(f.Before); ok {
		backward = true
		p.NumDesc, p.TextDesc = !p.NumDesc, !p.TextDesc
		p.HasCursor, p.CursorNum, p.CursorText, p.CursorID = true, c.num, c.text, c.id
	} else if c, ok := decodeCursor(f.After); ok {
		p.HasCursor, p.CursorNum, p.CursorText, p.CursorID = true, c.num, c.text, c.id
	}

	rows, err := q.LibraryListEntries(ctx, p)
	if err != nil {
		return Page{}, err
	}
	more := len(rows) > f.Per
	if more {
		rows = rows[:f.Per]
	}
	if backward {
		slices.Reverse(rows)
	}
	out := Page{Entries: rows}
	if len(rows) == 0 {
		return out, nil
	}
	if (backward && more) || (!backward && p.HasCursor) {
		out.PrevCursor = encodeCursor(rows[0])
	}
	if backward || more {
		out.NextCursor = encodeCursor(rows[len(rows)-1])
	}
	return out, nil
}

// Facets groups the filter options present in the viewer's library.
type Facets struct {
	Platforms, Formats, Genres, Tags []db.LibraryFacetsRow
}

func LoadFacets(ctx context.Context, q *db.Queries, viewer db.User, kind *db.MediaKind) (Facets, error) {
	rows, err := q.LibraryFacets(ctx, db.LibraryFacetsParams{UserID: viewer.ID, Kind: kind})
	if err != nil {
		return Facets{}, err
	}
	var f Facets
	for _, r := range rows {
		switch r.Facet {
		case "platform":
			f.Platforms = append(f.Platforms, r)
		case "format":
			f.Formats = append(f.Formats, r)
		case "genre":
			f.Genres = append(f.Genres, r)
		case "tag":
			f.Tags = append(f.Tags, r)
		}
	}
	return f, nil
}

// EntryInput holds the user-editable fields of an entry.
type EntryInput struct {
	Ownership  *db.Ownership
	Backlog    bool
	AcquiredOn *time.Time
	Tags       []string
	Notes      *string
	Visibility db.Visibility
}

// Add creates an entry and records an "added" activity event in one transaction.
func Add(ctx context.Context, d *deps.Deps, viewer db.User, item db.MediaItem, editionID *int64, in EntryInput) (db.LibraryEntry, error) {
	var entry db.LibraryEntry
	err := d.WithTx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		var err error
		entry, err = q.LibraryInsertEntry(ctx, db.LibraryInsertEntryParams{
			UserID:        viewer.ID,
			MediaItemID:   item.ID,
			EditionID:     editionID,
			OwnershipUser: in.Ownership,
			Backlog:       in.Backlog,
			AcquiredOn:    in.AcquiredOn,
			Tags:          in.Tags,
			Notes:         in.Notes,
			Visibility:    in.Visibility,
		})
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return ErrDuplicate
			}
			return err
		}
		_, err = activity.Record(ctx, q, activity.Event{
			UserID:         viewer.ID,
			Verb:           db.ActivityVerbAdded,
			MediaItemID:    item.ID,
			LibraryEntryID: &entry.ID,
			Visibility:     entry.Visibility,
			OccurredAt:     d.Now(),
		})
		return err
	})
	return entry, err
}

// NormalizeTags splits a comma-separated list into lowercase, trimmed, unique tags.
func NormalizeTags(s string) []string {
	out := []string{}
	for _, t := range strings.Split(s, ",") {
		t = strings.ToLower(strings.Join(strings.Fields(t), " "))
		if t != "" && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}
