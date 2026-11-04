package exchange

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/activity"
	"github.com/casmith/openshelf/internal/catalog"
	"github.com/casmith/openshelf/internal/db"
)

const MaxUploadBytes = 10 << 20

// RowError reports a row that was skipped. Line is set for CSV, Ref for JSON.
type RowError struct {
	Line    int    `json:"line,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Message string `json:"message"`
}

func (e RowError) Where() string {
	if e.Line > 0 {
		return fmt.Sprintf("Line %d", e.Line)
	}
	return e.Ref
}

// Result counts rows: created needed a new catalog item, matched wrote personal data
// for an existing item, skipped had nothing new to write.
type Result struct {
	Created int
	Matched int
	Skipped int
	Errors  []RowError
}

// fileError fails the whole import (unreadable file, wrong format).
type fileError struct{ msg string }

func (e *fileError) Error() string { return e.msg }

// rowErr is a validation problem with one row.
type rowErr struct{ msg string }

func (e *rowErr) Error() string { return e.msg }

func badRow(format string, args ...any) error { return &rowErr{fmt.Sprintf(format, args...)} }

type importer struct {
	ctx    context.Context
	userID int64
	q      *db.Queries
	res    Result

	created bool
	wrote   bool
}

// Run imports content for userID inside tx. Each row runs in its own savepoint so a
// failing row is reported and skipped without aborting the rest.
func Run(ctx context.Context, tx pgx.Tx, userID int64, format string, content []byte) (Result, error) {
	im := &importer{ctx: ctx, userID: userID}
	var err error
	switch format {
	case "csv":
		err = im.importCSV(tx, content)
	case "json":
		err = im.importJSON(tx, content)
	default:
		err = &fileError{"Unsupported file format."}
	}
	return im.res, err
}

// row runs fn in a savepoint. counted=false rows (journal events) only report errors.
func (im *importer) row(tx pgx.Tx, line int, ref string, counted bool, fn func() error) error {
	sp, err := tx.Begin(im.ctx)
	if err != nil {
		return err
	}
	im.q = db.New(sp)
	im.created, im.wrote = false, false
	if err := fn(); err != nil {
		if rbErr := sp.Rollback(im.ctx); rbErr != nil {
			return rbErr
		}
		msg := err.Error()
		var re *rowErr
		if !errors.As(err, &re) {
			msg = "Could not import: " + msg
		}
		im.res.Errors = append(im.res.Errors, RowError{Line: line, Ref: ref, Message: msg})
		return nil
	}
	if err := sp.Commit(im.ctx); err != nil {
		return err
	}
	if counted {
		switch {
		case im.created:
			im.res.Created++
		case im.wrote:
			im.res.Matched++
		default:
			im.res.Skipped++
		}
	}
	return nil
}

func noRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

type itemSpec struct {
	PublicID      *uuid.UUID
	Kind          db.MediaKind
	Title         string
	OriginalTitle *string
	Year          *int16
	ReleaseDate   *time.Time
	IDs           []ExternalID
}

// resolveItem matches by public ID, then external IDs, then exact title+year+kind,
// and otherwise creates a manual item owned by the importer.
func (im *importer) resolveItem(s itemSpec) (int64, error) {
	if s.PublicID != nil {
		m, err := im.q.GetMediaItemByPublicID(im.ctx, *s.PublicID)
		if err == nil && m.Kind == s.Kind {
			return m.ID, nil
		} else if err != nil && !noRows(err) {
			return 0, err
		}
	}
	for _, x := range s.IDs {
		m, err := im.q.ExchangeFindItemByExternalID(im.ctx, db.ExchangeFindItemByExternalIDParams{Provider: x.Provider, Value: x.Value})
		if err == nil && m.Kind == s.Kind {
			return m.ID, nil
		} else if err != nil && !noRows(err) {
			return 0, err
		}
	}
	m, err := im.q.ExchangeFindItemByTitle(im.ctx, db.ExchangeFindItemByTitleParams{Kind: s.Kind, Title: s.Title, ReleaseYear: s.Year})
	if err == nil {
		return m.ID, nil
	} else if !noRows(err) {
		return 0, err
	}
	uid := im.userID
	m, err = im.q.ExchangeCreateItem(im.ctx, db.ExchangeCreateItemParams{
		Kind: s.Kind, Title: s.Title, SortTitle: catalog.SortTitle(s.Title), OriginalTitle: s.OriginalTitle,
		ReleaseYear: s.Year, ReleaseDate: s.ReleaseDate, CreatedByUserID: &uid,
	})
	if err != nil {
		return 0, err
	}
	for _, x := range s.IDs {
		if err := im.q.ExchangeAddExternalID(im.ctx, db.ExchangeAddExternalIDParams{Provider: x.Provider, Value: x.Value, MediaItemID: &m.ID}); err != nil {
			return 0, err
		}
	}
	im.created, im.wrote = true, true
	return m.ID, nil
}

// entryFields are merged into an existing entry: nil leaves a value alone and tags are unioned.
type entryFields struct {
	Ownership  *db.Ownership
	Backlog    *bool
	Played     *bool
	AcquiredOn *time.Time
	Tags       []string
	Notes      *string
}

func (im *importer) ensureEntry(itemID int64, editionID *int64, f entryFields) (id int64, isNew bool, err error) {
	e, err := im.q.ExchangeGetEntry(im.ctx, db.ExchangeGetEntryParams{UserID: im.userID, MediaItemID: itemID, EditionID: editionID})
	if noRows(err) {
		tags := f.Tags
		if tags == nil {
			tags = []string{}
		}
		e, err = im.q.ExchangeInsertEntry(im.ctx, db.ExchangeInsertEntryParams{
			UserID: im.userID, MediaItemID: itemID, EditionID: editionID, OwnershipUser: f.Ownership,
			Backlog: f.Backlog != nil && *f.Backlog, PlayedUser: f.Played, AcquiredOn: f.AcquiredOn, Tags: tags, Notes: f.Notes,
		})
		if err != nil {
			return 0, false, err
		}
		im.wrote = true
		return e.ID, true, nil
	} else if err != nil {
		return 0, false, err
	}
	p := db.ExchangeUpdateEntryParams{ID: e.ID, UserID: im.userID, OwnershipUser: e.OwnershipUser, Backlog: e.Backlog,
		PlayedUser: e.PlayedUser, AcquiredOn: e.AcquiredOn, Tags: e.Tags, Notes: e.Notes}
	changed := false
	if f.Ownership != nil && (e.OwnershipUser == nil || *e.OwnershipUser != *f.Ownership) {
		p.OwnershipUser, changed = f.Ownership, true
	}
	if f.Backlog != nil && *f.Backlog != e.Backlog {
		p.Backlog, changed = *f.Backlog, true
	}
	if f.Played != nil && (e.PlayedUser == nil || *e.PlayedUser != *f.Played) {
		p.PlayedUser, changed = f.Played, true
	}
	if f.AcquiredOn != nil && (e.AcquiredOn == nil || !e.AcquiredOn.Equal(*f.AcquiredOn)) {
		p.AcquiredOn, changed = f.AcquiredOn, true
	}
	if f.Notes != nil && (e.Notes == nil || *e.Notes != *f.Notes) {
		p.Notes, changed = f.Notes, true
	}
	for _, t := range f.Tags {
		if !slices.Contains(p.Tags, t) {
			p.Tags, changed = append(p.Tags, t), true
		}
	}
	if changed {
		if err := im.q.ExchangeUpdateEntry(im.ctx, p); err != nil {
			return 0, false, err
		}
		im.wrote = true
	}
	return e.ID, false, nil
}

func (im *importer) ensureExperience(p db.ExchangeInsertExperienceParams) (id int64, isNew bool, err error) {
	id, err = im.q.ExchangeFindExperience(im.ctx, db.ExchangeFindExperienceParams{
		UserID: im.userID, MediaItemID: p.MediaItemID, EditionID: p.EditionID, State: p.State,
		StartedOn: p.StartedOn, FinishedOn: p.FinishedOn,
	})
	if err == nil {
		return id, false, nil
	} else if !noRows(err) {
		return 0, false, err
	}
	p.UserID = im.userID
	id, err = im.q.ExchangeInsertExperience(im.ctx, p)
	if err != nil {
		return 0, false, err
	}
	im.wrote = true
	return id, true, nil
}

// ensureReview creates or updates the user's review of an item; nil rating/body/spoilers keep existing values.
func (im *importer) ensureReview(itemID int64, expID *int64, rating *int16, body *string, spoilers *bool) (id int64, isNew bool, err error) {
	r, err := im.q.ExchangeGetReview(im.ctx, db.ExchangeGetReviewParams{UserID: im.userID, MediaItemID: itemID})
	if noRows(err) {
		id, err = im.q.ExchangeInsertReview(im.ctx, db.ExchangeInsertReviewParams{
			UserID: im.userID, MediaItemID: itemID, ExperienceID: expID, Rating: rating, Body: body,
			ContainsSpoilers: spoilers != nil && *spoilers,
		})
		if err != nil {
			return 0, false, err
		}
		im.wrote = true
		return id, true, nil
	} else if err != nil {
		return 0, false, err
	}
	p := db.ExchangeUpdateReviewParams{ID: r.ID, UserID: im.userID, Rating: r.Rating, Body: r.Body, ContainsSpoilers: r.ContainsSpoilers}
	changed := false
	if rating != nil && (r.Rating == nil || *r.Rating != *rating) {
		p.Rating, changed = rating, true
	}
	if body != nil && (r.Body == nil || *r.Body != *body) {
		p.Body, changed = body, true
	}
	if spoilers != nil && *spoilers != r.ContainsSpoilers {
		p.ContainsSpoilers, changed = *spoilers, true
	}
	if changed {
		if err := im.q.ExchangeUpdateReview(im.ctx, p); err != nil {
			return 0, false, err
		}
		im.wrote = true
	}
	return r.ID, false, nil
}

// record writes a private journal event unless an identical one already exists.
func (im *importer) record(ev activity.Event) error {
	ev.UserID = im.userID
	ev.Visibility = db.VisibilityPrivate
	if !ev.OccurredAt.IsZero() {
		exists, err := im.q.ExchangeActivityExists(im.ctx, db.ExchangeActivityExistsParams{
			UserID: im.userID, Verb: ev.Verb, MediaItemID: ev.MediaItemID, OccurredAt: ev.OccurredAt,
			LibraryEntryID: ev.LibraryEntryID, ExperienceID: ev.ExperienceID, ReviewID: ev.ReviewID, ListID: ev.ListID,
		})
		if err != nil || exists {
			return err
		}
	}
	_, err := activity.Record(im.ctx, im.q, ev)
	return err
}
