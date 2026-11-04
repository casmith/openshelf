package exchange

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/casmith/openshelf/internal/db"
)

const FormatV1 = "openshelf.export.v1"

// Export is the openshelf.export.v1 document (docs/export-format.md).
type Export struct {
	Format         string       `json:"format"`
	ExportedAt     time.Time    `json:"exported_at"`
	User           ExportUser   `json:"user"`
	Items          []Item       `json:"items"`
	Editions       []Edition    `json:"editions"`
	LibraryEntries []Entry      `json:"library_entries"`
	Experiences    []Experience `json:"experiences"`
	Reviews        []Review     `json:"reviews"`
	Lists          []List       `json:"lists"`
	Activity       []Activity   `json:"activity"`
}

type ExportUser struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

type ExternalID struct {
	Provider string `json:"provider"`
	Value    string `json:"value"`
}

type Item struct {
	ID            uuid.UUID    `json:"id"`
	Kind          db.MediaKind `json:"kind"`
	Title         string       `json:"title"`
	OriginalTitle *string      `json:"original_title,omitempty"`
	Year          *int16       `json:"year,omitempty"`
	ReleaseDate   *string      `json:"release_date,omitempty"`
	ExternalIDs   []ExternalID `json:"external_ids"`
}

type Edition struct {
	ID          uuid.UUID         `json:"id"`
	Item        uuid.UUID         `json:"item"`
	Title       *string           `json:"title,omitempty"`
	Format      *db.EditionFormat `json:"format,omitempty"`
	Platform    *string           `json:"platform,omitempty"`
	Region      *string           `json:"region,omitempty"`
	Publisher   *string           `json:"publisher,omitempty"`
	ReleaseDate *string           `json:"release_date,omitempty"`
}

type Entry struct {
	ID         uuid.UUID     `json:"id"`
	Item       uuid.UUID     `json:"item"`
	Edition    *uuid.UUID    `json:"edition,omitempty"`
	Ownership  *db.Ownership `json:"ownership,omitempty"`
	Backlog    bool          `json:"backlog"`
	Played     *bool         `json:"played,omitempty"`
	AcquiredOn *string       `json:"acquired_on,omitempty"`
	Tags       []string      `json:"tags"`
	Notes      *string       `json:"notes,omitempty"`
	Visibility db.Visibility `json:"visibility"`
	CreatedAt  time.Time     `json:"created_at"`
	UpdatedAt  time.Time     `json:"updated_at"`
}

type Experience struct {
	ID              uuid.UUID          `json:"id"`
	Item            uuid.UUID          `json:"item"`
	Edition         *uuid.UUID         `json:"edition,omitempty"`
	State           db.ExperienceState `json:"state"`
	StartedOn       *string            `json:"started_on,omitempty"`
	FinishedOn      *string            `json:"finished_on,omitempty"`
	ProgressUnit    *db.ProgressUnit   `json:"progress_unit,omitempty"`
	ProgressCurrent *float64           `json:"progress_current,omitempty"`
	ProgressTotal   *float64           `json:"progress_total,omitempty"`
	Notes           *string            `json:"notes,omitempty"`
	Visibility      db.Visibility      `json:"visibility"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
	ProgressEvents  []ProgressEvent    `json:"progress_events"`
}

type ProgressEvent struct {
	OccurredAt time.Time `json:"occurred_at"`
	Value      float64   `json:"value"`
	Note       *string   `json:"note,omitempty"`
}

type Review struct {
	ID               uuid.UUID     `json:"id"`
	Item             uuid.UUID     `json:"item"`
	Experience       *uuid.UUID    `json:"experience,omitempty"`
	Rating           *int16        `json:"rating,omitempty"`
	Body             *string       `json:"body,omitempty"`
	ContainsSpoilers bool          `json:"contains_spoilers"`
	Visibility       db.Visibility `json:"visibility"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
}

type List struct {
	ID          uuid.UUID     `json:"id"`
	Name        string        `json:"name"`
	Description *string       `json:"description,omitempty"`
	Ranked      bool          `json:"ranked"`
	Visibility  db.Visibility `json:"visibility"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
	Items       []ListItem    `json:"items"`
}

type ListItem struct {
	Item     uuid.UUID `json:"item"`
	Position int32     `json:"position"`
	Note     *string   `json:"note,omitempty"`
}

type Activity struct {
	ID           uuid.UUID       `json:"id"`
	Verb         db.ActivityVerb `json:"verb"`
	Item         uuid.UUID       `json:"item"`
	LibraryEntry *uuid.UUID      `json:"library_entry,omitempty"`
	Experience   *uuid.UUID      `json:"experience,omitempty"`
	Review       *uuid.UUID      `json:"review,omitempty"`
	List         *uuid.UUID      `json:"list,omitempty"`
	Visibility   db.Visibility   `json:"visibility"`
	OccurredAt   time.Time       `json:"occurred_at"`
}

func dateString(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.DateOnly)
	return &s
}

// streamWriter writes the export one JSON value at a time so the document is never built in memory.
type streamWriter struct {
	w     *bufio.Writer
	err   error
	first bool
}

func (s *streamWriter) raw(str string) {
	if s.err == nil {
		_, s.err = s.w.WriteString(str)
	}
}

func (s *streamWriter) value(v any) {
	if s.err != nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		s.err = err
		return
	}
	_, s.err = s.w.Write(b)
}

func (s *streamWriter) field(name string, v any) {
	s.raw(",")
	s.value(name)
	s.raw(":")
	s.value(v)
}

func (s *streamWriter) beginArray(name string) {
	s.raw(",")
	s.value(name)
	s.raw(":[")
	s.first = true
}

func (s *streamWriter) elem(v any) {
	if !s.first {
		s.raw(",")
	}
	s.first = false
	s.value(v)
}

func (s *streamWriter) endArray() { s.raw("]") }

// WriteExport streams user's openshelf.export.v1 document to w.
func WriteExport(ctx context.Context, q *db.Queries, user db.User, now time.Time, w io.Writer) error {
	s := &streamWriter{w: bufio.NewWriter(w)}
	s.raw(`{"format":`)
	s.value(FormatV1)
	s.field("exported_at", now.UTC())
	s.field("user", ExportUser{Username: user.Username, DisplayName: user.DisplayName})

	items, err := q.ExchangeExportItems(ctx, user.ID)
	if err != nil {
		return err
	}
	s.beginArray("items")
	for _, it := range items {
		x := Item{ID: it.PublicID, Kind: it.Kind, Title: it.Title, OriginalTitle: it.OriginalTitle,
			Year: it.ReleaseYear, ReleaseDate: dateString(it.ReleaseDate), ExternalIDs: []ExternalID{}}
		if err := json.Unmarshal(it.ExternalIds, &x.ExternalIDs); err != nil {
			return err
		}
		s.elem(x)
	}
	s.endArray()

	editions, err := q.ExchangeExportEditions(ctx, user.ID)
	if err != nil {
		return err
	}
	s.beginArray("editions")
	for _, e := range editions {
		s.elem(Edition{ID: e.PublicID, Item: e.ItemPublicID, Title: e.Title, Format: e.Format, Platform: e.PlatformSlug,
			Region: e.Region, Publisher: e.Publisher, ReleaseDate: dateString(e.ReleaseDate)})
	}
	s.endArray()

	entries, err := q.ExchangeExportEntries(ctx, user.ID)
	if err != nil {
		return err
	}
	s.beginArray("library_entries")
	for _, e := range entries {
		s.elem(Entry{ID: e.PublicID, Item: e.ItemPublicID, Edition: e.EditionPublicID, Ownership: e.OwnershipUser,
			Backlog: e.Backlog, Played: e.PlayedUser, AcquiredOn: dateString(e.AcquiredOn), Tags: e.Tags, Notes: e.Notes,
			Visibility: e.Visibility, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt})
	}
	s.endArray()

	events, err := q.ExchangeExportProgressEvents(ctx, user.ID)
	if err != nil {
		return err
	}
	byExp := map[uuid.UUID][]ProgressEvent{}
	for _, e := range events {
		byExp[e.ExperiencePublicID] = append(byExp[e.ExperiencePublicID], ProgressEvent{OccurredAt: e.OccurredAt, Value: e.Value, Note: e.Note})
	}
	exps, err := q.ExchangeExportExperiences(ctx, user.ID)
	if err != nil {
		return err
	}
	s.beginArray("experiences")
	for _, e := range exps {
		pe := byExp[e.PublicID]
		if pe == nil {
			pe = []ProgressEvent{}
		}
		s.elem(Experience{ID: e.PublicID, Item: e.ItemPublicID, Edition: e.EditionPublicID, State: e.State,
			StartedOn: dateString(e.StartedOn), FinishedOn: dateString(e.FinishedOn), ProgressUnit: e.ProgressUnit,
			ProgressCurrent: e.ProgressCurrent, ProgressTotal: e.ProgressTotal, Notes: e.Notes, Visibility: e.Visibility,
			CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt, ProgressEvents: pe})
	}
	s.endArray()

	reviews, err := q.ExchangeExportReviews(ctx, user.ID)
	if err != nil {
		return err
	}
	s.beginArray("reviews")
	for _, r := range reviews {
		s.elem(Review{ID: r.PublicID, Item: r.ItemPublicID, Experience: r.ExperiencePublicID, Rating: r.Rating, Body: r.Body,
			ContainsSpoilers: r.ContainsSpoilers, Visibility: r.Visibility, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt})
	}
	s.endArray()

	listItems, err := q.ExchangeExportListItems(ctx, user.ID)
	if err != nil {
		return err
	}
	byList := map[uuid.UUID][]ListItem{}
	for _, li := range listItems {
		byList[li.ListPublicID] = append(byList[li.ListPublicID], ListItem{Item: li.ItemPublicID, Position: li.Position, Note: li.Note})
	}
	lists, err := q.ExchangeExportLists(ctx, user.ID)
	if err != nil {
		return err
	}
	s.beginArray("lists")
	for _, l := range lists {
		li := byList[l.PublicID]
		if li == nil {
			li = []ListItem{}
		}
		s.elem(List{ID: l.PublicID, Name: l.Name, Description: l.Description, Ranked: l.Ranked, Visibility: l.Visibility,
			CreatedAt: l.CreatedAt, UpdatedAt: l.UpdatedAt, Items: li})
	}
	s.endArray()

	activity, err := q.ExchangeExportActivity(ctx, user.ID)
	if err != nil {
		return err
	}
	s.beginArray("activity")
	for _, a := range activity {
		s.elem(Activity{ID: a.PublicID, Verb: a.Verb, Item: a.ItemPublicID, LibraryEntry: a.EntryPublicID,
			Experience: a.ExperiencePublicID, Review: a.ReviewPublicID, List: a.ListPublicID,
			Visibility: a.Visibility, OccurredAt: a.OccurredAt})
	}
	s.endArray()
	s.raw("}\n")
	if s.err != nil {
		return s.err
	}
	return s.w.Flush()
}
