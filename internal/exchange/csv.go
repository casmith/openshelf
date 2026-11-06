package exchange

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/activity"
	"github.com/casmith/openshelf/internal/db"
)

var csvColumns = []string{"kind", "title", "year", "isbn13", "tmdb_id", "igdb_id", "platform", "ownership",
	"backlog", "status", "started_on", "finished_on", "rating_100", "review", "tags"}

type csvRow struct {
	item       itemSpec
	platform   string
	ownership  *db.Ownership
	backlog    *bool
	state      *db.ExperienceState
	startedOn  *time.Time
	finishedOn *time.Time
	rating     *int16
	review     *string
	tags       []string
}

func (im *importer) importCSV(tx pgx.Tx, content []byte) error {
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
	r := csv.NewReader(bytes.NewReader(content))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	header, err := r.Read()
	if err != nil {
		return &fileError{"The file has no CSV header row."}
	}
	cols := map[string]int{}
	for i, h := range header {
		cols[strings.ToLower(strings.TrimSpace(h))] = i
	}
	if _, ok := cols["kind"]; !ok {
		return &fileError{"The CSV header must include kind and title columns."}
	}
	if _, ok := cols["title"]; !ok {
		return &fileError{"The CSV header must include kind and title columns."}
	}
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		var pe *csv.ParseError
		if errors.As(err, &pe) {
			im.res.Errors = append(im.res.Errors, RowError{Line: pe.StartLine, Message: "Malformed CSV: " + pe.Err.Error()})
			continue
		} else if err != nil {
			return &fileError{"Could not read the CSV file: " + err.Error()}
		}
		line, _ := r.FieldPos(0)
		get := func(name string) string {
			if i, ok := cols[name]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		if strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		if err := im.row(tx, line, "", true, func() error {
			row, err := parseCSVRow(get)
			if err != nil {
				return err
			}
			return im.applyCSVRow(row)
		}); err != nil {
			return err
		}
	}
}

func parseKind(s string) (db.MediaKind, bool) {
	switch strings.ToLower(s) {
	case "game":
		return db.MediaKindGame, true
	case "book":
		return db.MediaKindBook, true
	case "film", "movie":
		return db.MediaKindFilm, true
	case "show", "tv":
		return db.MediaKindShow, true
	}
	return "", false
}

func parseDate(col, s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return nil, badRow("%s must be a date (YYYY-MM-DD).", col)
	}
	return &t, nil
}

func parseCSVRow(get func(string) string) (csvRow, error) {
	var row csvRow
	kind, ok := parseKind(get("kind"))
	if !ok {
		return row, badRow("kind must be game, book, film, or show.")
	}
	title := get("title")
	if title == "" {
		return row, badRow("title is required.")
	}
	if len(title) > 500 {
		return row, badRow("title is too long.")
	}
	row.item = itemSpec{Kind: kind, Title: title}
	if v := get("year"); v != "" {
		y, err := strconv.Atoi(v)
		if err != nil || y < 1000 || y > 3000 {
			return row, badRow("year must be a four-digit year.")
		}
		y16 := int16(y)
		row.item.Year = &y16
	}
	if v := get("isbn13"); v != "" && kind == db.MediaKindBook {
		isbn := strings.NewReplacer("-", "", " ", "").Replace(v)
		if len(isbn) != 13 || strings.Trim(isbn, "0123456789") != "" {
			return row, badRow("isbn13 must have 13 digits.")
		}
		row.item.IDs = append(row.item.IDs, ExternalID{Provider: "isbn13", Value: isbn})
	}
	if v := get("tmdb_id"); v != "" && (kind == db.MediaKindFilm || kind == db.MediaKindShow) {
		if _, err := strconv.ParseUint(v, 10, 64); err != nil {
			return row, badRow("tmdb_id must be a number.")
		}
		provider := "tmdb_movie"
		if kind == db.MediaKindShow {
			provider = "tmdb_tv"
		}
		row.item.IDs = append(row.item.IDs, ExternalID{Provider: provider, Value: v})
	}
	if v := get("igdb_id"); v != "" && kind == db.MediaKindGame {
		if _, err := strconv.ParseUint(v, 10, 64); err != nil {
			return row, badRow("igdb_id must be a number.")
		}
		row.item.IDs = append(row.item.IDs, ExternalID{Provider: "igdb_game", Value: v})
	}
	row.platform = get("platform")
	if row.platform != "" && kind != db.MediaKindGame {
		return row, badRow("platform only applies to games.")
	}
	if v := strings.ToLower(get("ownership")); v != "" {
		o := db.Ownership(v)
		if !o.Valid() {
			return row, badRow("ownership must be owned, borrowed, or not_owned.")
		}
		row.ownership = &o
	}
	switch strings.ToLower(get("backlog")) {
	case "":
	case "true", "yes", "1", "y":
		b := true
		row.backlog = &b
	case "false", "no", "0", "n":
		b := false
		row.backlog = &b
	default:
		return row, badRow("backlog must be true or false.")
	}
	var err error
	if row.startedOn, err = parseDate("started_on", get("started_on")); err != nil {
		return row, err
	}
	if row.finishedOn, err = parseDate("finished_on", get("finished_on")); err != nil {
		return row, err
	}
	var state db.ExperienceState
	switch strings.ToLower(get("status")) {
	case "":
		switch {
		case row.finishedOn != nil:
			state = db.ExperienceStateFinished
		case row.startedOn != nil:
			state = db.ExperienceStateActive
		}
	case "in_progress":
		state = db.ExperienceStateActive
	case "finished":
		state = db.ExperienceStateFinished
	case "abandoned":
		state = db.ExperienceStateAbandoned
	default:
		return row, badRow("status must be in_progress, finished, or abandoned.")
	}
	if state != "" {
		row.state = &state
	}
	if state == db.ExperienceStateActive && row.finishedOn != nil {
		return row, badRow("finished_on needs status finished or abandoned.")
	}
	if row.startedOn != nil && row.finishedOn != nil && row.finishedOn.Before(*row.startedOn) {
		return row, badRow("finished_on is before started_on.")
	}
	if v := get("rating_100"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 100 {
			return row, badRow("rating_100 must be a whole number from 0 to 100.")
		}
		r := int16(n)
		row.rating = &r
	}
	if v := get("review"); v != "" {
		if len(v) > 20000 {
			return row, badRow("review is too long.")
		}
		row.review = &v
	}
	for _, t := range strings.FieldsFunc(get("tags"), func(r rune) bool { return r == ';' || r == ',' }) {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" && !slices.Contains(row.tags, t) {
			row.tags = append(row.tags, t)
		}
	}
	return row, nil
}

// historical returns a stable timestamp for a known date (noon UTC keeps the day in most timezones).
func historical(d *time.Time) time.Time {
	if d == nil {
		return time.Time{}
	}
	return time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, time.UTC)
}

func (im *importer) applyCSVRow(row csvRow) error {
	itemID, err := im.resolveItem(row.item)
	if err != nil {
		return err
	}
	var editionID *int64
	if row.platform != "" {
		p, err := im.q.FindPlatformByName(im.ctx, row.platform)
		if noRows(err) {
			return badRow("unknown platform %q.", row.platform)
		} else if err != nil {
			return err
		}
		id, err := im.q.ExchangeFindEditionByPlatform(im.ctx, db.ExchangeFindEditionByPlatformParams{MediaItemID: itemID, PlatformID: &p.ID})
		if noRows(err) {
			id, err = im.q.ExchangeCreateEdition(im.ctx, db.ExchangeCreateEditionParams{MediaItemID: itemID, PlatformID: &p.ID})
			im.wrote = true
		}
		if err != nil {
			return err
		}
		editionID = &id
	}

	entryID, newEntry, err := im.ensureEntry(itemID, editionID, entryFields{Ownership: row.ownership, Backlog: row.backlog, Tags: row.tags})
	if err != nil {
		return err
	}
	if newEntry {
		first := row.startedOn
		if first == nil {
			first = row.finishedOn
		}
		if err := im.record(activity.Event{Verb: db.ActivityVerbAdded, MediaItemID: itemID, LibraryEntryID: &entryID, OccurredAt: historical(first)}); err != nil {
			return err
		}
	}

	var expID *int64
	if row.state != nil {
		id, isNew, err := im.ensureExperience(db.ExchangeInsertExperienceParams{
			MediaItemID: itemID, EditionID: editionID, State: *row.state, StartedOn: row.startedOn, FinishedOn: row.finishedOn,
		})
		if err != nil {
			return err
		}
		expID = &id
		if isNew {
			if row.startedOn != nil || *row.state == db.ExperienceStateActive {
				if err := im.record(activity.Event{Verb: db.ActivityVerbStarted, MediaItemID: itemID, ExperienceID: &id, OccurredAt: historical(row.startedOn)}); err != nil {
					return err
				}
			}
			if *row.state != db.ExperienceStateActive && row.finishedOn != nil {
				verb := db.ActivityVerbFinished
				if *row.state == db.ExperienceStateAbandoned {
					verb = db.ActivityVerbAbandoned
				}
				if err := im.record(activity.Event{Verb: verb, MediaItemID: itemID, ExperienceID: &id, OccurredAt: historical(row.finishedOn)}); err != nil {
					return err
				}
			}
		}
	}

	if row.rating != nil || row.review != nil {
		id, isNew, err := im.ensureReview(itemID, expID, row.rating, row.review, nil)
		if err != nil {
			return err
		}
		if isNew {
			verb := db.ActivityVerbRated
			if row.review != nil {
				verb = db.ActivityVerbReviewed
			}
			if err := im.record(activity.Event{Verb: verb, MediaItemID: itemID, ReviewID: &id, OccurredAt: historical(row.finishedOn)}); err != nil {
				return err
			}
		}
	}
	return nil
}
