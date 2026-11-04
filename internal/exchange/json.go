package exchange

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/activity"
	"github.com/casmith/openshelf/internal/db"
)

func parseOptDate(field string, s *string) (*time.Time, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.DateOnly, *s)
	if err != nil {
		return nil, badRow("%s must be a date (YYYY-MM-DD).", field)
	}
	return &t, nil
}

type jsonImport struct {
	*importer
	doc      Export
	items    map[uuid.UUID]Item
	editions map[uuid.UUID]Edition
	entryIDs map[uuid.UUID]int64
	expIDs   map[uuid.UUID]int64
	revIDs   map[uuid.UUID]int64
	listIDs  map[uuid.UUID]int64
}

func (im *importer) importJSON(tx pgx.Tx, content []byte) error {
	var doc Export
	if err := json.Unmarshal(content, &doc); err != nil {
		return &fileError{"The file isn't valid JSON: " + err.Error()}
	}
	if doc.Format != FormatV1 {
		return &fileError{fmt.Sprintf("Unsupported export format %q (expected %s).", doc.Format, FormatV1)}
	}
	j := &jsonImport{importer: im, doc: doc,
		items: map[uuid.UUID]Item{}, editions: map[uuid.UUID]Edition{},
		entryIDs: map[uuid.UUID]int64{}, expIDs: map[uuid.UUID]int64{},
		revIDs: map[uuid.UUID]int64{}, listIDs: map[uuid.UUID]int64{}}
	for _, it := range doc.Items {
		j.items[it.ID] = it
	}
	for _, e := range doc.Editions {
		j.editions[e.ID] = e
	}
	for i, e := range doc.LibraryEntries {
		if err := im.row(tx, 0, fmt.Sprintf("library_entries[%d]", i), true, func() error { return j.entry(e) }); err != nil {
			return err
		}
	}
	for i, e := range doc.Experiences {
		if err := im.row(tx, 0, fmt.Sprintf("experiences[%d]", i), true, func() error { return j.experience(e) }); err != nil {
			return err
		}
	}
	for i, r := range doc.Reviews {
		if err := im.row(tx, 0, fmt.Sprintf("reviews[%d]", i), true, func() error { return j.review(r) }); err != nil {
			return err
		}
	}
	for i, l := range doc.Lists {
		if err := im.row(tx, 0, fmt.Sprintf("lists[%d]", i), true, func() error { return j.list(l) }); err != nil {
			return err
		}
	}
	for i, a := range doc.Activity {
		if err := im.row(tx, 0, fmt.Sprintf("activity[%d]", i), false, func() error { return j.activity(a) }); err != nil {
			return err
		}
	}
	return nil
}

func (j *jsonImport) item(ref uuid.UUID) (int64, error) {
	it, ok := j.items[ref]
	if !ok {
		return 0, badRow("references item %s, which isn't in the items section.", ref)
	}
	if !it.Kind.Valid() {
		return 0, badRow("item %s has an invalid kind.", ref)
	}
	if strings.TrimSpace(it.Title) == "" {
		return 0, badRow("item %s has no title.", ref)
	}
	rd, err := parseOptDate("release_date", it.ReleaseDate)
	if err != nil {
		return 0, err
	}
	return j.resolveItem(itemSpec{PublicID: &it.ID, Kind: it.Kind, Title: it.Title, OriginalTitle: it.OriginalTitle,
		Year: it.Year, ReleaseDate: rd, IDs: it.ExternalIDs})
}

func (j *jsonImport) edition(itemID int64, ref *uuid.UUID) (*int64, error) {
	if ref == nil {
		return nil, nil
	}
	if id, err := j.q.ExchangeGetEditionByPublicID(j.ctx, db.ExchangeGetEditionByPublicIDParams{PublicID: *ref, MediaItemID: itemID}); err == nil {
		return &id, nil
	} else if !noRows(err) {
		return nil, err
	}
	e, ok := j.editions[*ref]
	if !ok {
		return nil, badRow("references edition %s, which isn't in the editions section.", *ref)
	}
	if e.Format != nil && !e.Format.Valid() {
		return nil, badRow("edition %s has an invalid format.", *ref)
	}
	var platformID *int64
	if e.Platform != nil {
		p, err := j.q.GetPlatformBySlug(j.ctx, *e.Platform)
		if noRows(err) {
			return nil, badRow("unknown platform %q.", *e.Platform)
		} else if err != nil {
			return nil, err
		}
		platformID = &p.ID
	}
	id, err := j.q.ExchangeFindEdition(j.ctx, db.ExchangeFindEditionParams{MediaItemID: itemID, PlatformID: platformID, Format: e.Format, Title: e.Title})
	if noRows(err) {
		rd, derr := parseOptDate("release_date", e.ReleaseDate)
		if derr != nil {
			return nil, derr
		}
		id, err = j.q.ExchangeCreateEdition(j.ctx, db.ExchangeCreateEditionParams{MediaItemID: itemID, PlatformID: platformID,
			Format: e.Format, Title: e.Title, Region: e.Region, Publisher: e.Publisher, ReleaseDate: rd})
		j.wrote = true
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func (j *jsonImport) entry(e Entry) error {
	itemID, err := j.item(e.Item)
	if err != nil {
		return err
	}
	editionID, err := j.edition(itemID, e.Edition)
	if err != nil {
		return err
	}
	if e.Ownership != nil && !e.Ownership.Valid() {
		return badRow("invalid ownership %q.", *e.Ownership)
	}
	acquired, err := parseOptDate("acquired_on", e.AcquiredOn)
	if err != nil {
		return err
	}
	backlog := e.Backlog
	id, _, err := j.ensureEntry(itemID, editionID, entryFields{Ownership: e.Ownership, Backlog: &backlog, Played: e.Played,
		AcquiredOn: acquired, Tags: e.Tags, Notes: e.Notes})
	if err != nil {
		return err
	}
	j.entryIDs[e.ID] = id
	return nil
}

func (j *jsonImport) experience(e Experience) error {
	itemID, err := j.item(e.Item)
	if err != nil {
		return err
	}
	editionID, err := j.edition(itemID, e.Edition)
	if err != nil {
		return err
	}
	if !e.State.Valid() {
		return badRow("invalid state %q.", e.State)
	}
	if e.ProgressUnit != nil && !e.ProgressUnit.Valid() {
		return badRow("invalid progress_unit %q.", *e.ProgressUnit)
	}
	started, err := parseOptDate("started_on", e.StartedOn)
	if err != nil {
		return err
	}
	finished, err := parseOptDate("finished_on", e.FinishedOn)
	if err != nil {
		return err
	}
	id, _, err := j.ensureExperience(db.ExchangeInsertExperienceParams{MediaItemID: itemID, EditionID: editionID, State: e.State,
		StartedOn: started, FinishedOn: finished, ProgressUnit: e.ProgressUnit, ProgressCurrent: e.ProgressCurrent,
		ProgressTotal: e.ProgressTotal, Notes: e.Notes})
	if err != nil {
		return err
	}
	for _, pe := range e.ProgressEvents {
		n, err := j.q.ExchangeAddProgressEvent(j.ctx, db.ExchangeAddProgressEventParams{ExperienceID: id, UserID: j.userID,
			OccurredAt: pe.OccurredAt, Value: pe.Value, Note: pe.Note})
		if err != nil {
			return err
		}
		if n > 0 {
			j.wrote = true
		}
	}
	j.expIDs[e.ID] = id
	return nil
}

func (j *jsonImport) review(r Review) error {
	itemID, err := j.item(r.Item)
	if err != nil {
		return err
	}
	if r.Rating == nil && (r.Body == nil || *r.Body == "") {
		return badRow("a review needs a rating or a body.")
	}
	if r.Rating != nil && (*r.Rating < 0 || *r.Rating > 100) {
		return badRow("rating must be from 0 to 100.")
	}
	var expID *int64
	if r.Experience != nil {
		if id, ok := j.expIDs[*r.Experience]; ok {
			expID = &id
		}
	}
	spoilers := r.ContainsSpoilers
	id, _, err := j.ensureReview(itemID, expID, r.Rating, r.Body, &spoilers)
	if err != nil {
		return err
	}
	j.revIDs[r.ID] = id
	return nil
}

func (j *jsonImport) list(l List) error {
	name := strings.TrimSpace(l.Name)
	if name == "" {
		return badRow("a list needs a name.")
	}
	id, err := j.q.ExchangeFindList(j.ctx, db.ExchangeFindListParams{UserID: j.userID, Name: name})
	if noRows(err) {
		id, err = j.q.ExchangeInsertList(j.ctx, db.ExchangeInsertListParams{UserID: j.userID, Name: name, Description: l.Description, Ranked: l.Ranked})
		j.wrote = true
	}
	if err != nil {
		return err
	}
	items := slices.Clone(l.Items)
	slices.SortStableFunc(items, func(a, b ListItem) int { return cmp.Compare(a.Position, b.Position) })
	for _, li := range items {
		itemID, err := j.item(li.Item)
		if err != nil {
			return err
		}
		n, err := j.q.ExchangeAddListItem(j.ctx, db.ExchangeAddListItemParams{ListID: id, UserID: j.userID, MediaItemID: itemID, Note: li.Note})
		if err != nil {
			return err
		}
		if n > 0 {
			j.wrote = true
		}
	}
	j.listIDs[l.ID] = id
	return nil
}

func (j *jsonImport) activity(a Activity) error {
	if !a.Verb.Valid() {
		return badRow("invalid verb %q.", a.Verb)
	}
	if a.OccurredAt.IsZero() {
		return badRow("occurred_at is required.")
	}
	itemID, err := j.item(a.Item)
	if err != nil {
		return err
	}
	ev := activity.Event{Verb: a.Verb, MediaItemID: itemID, OccurredAt: a.OccurredAt}
	link := func(ref *uuid.UUID, ids map[uuid.UUID]int64, dst **int64) error {
		if ref == nil {
			return nil
		}
		id, ok := ids[*ref]
		if !ok {
			return badRow("references %s, which wasn't imported.", *ref)
		}
		*dst = &id
		return nil
	}
	if err := link(a.LibraryEntry, j.entryIDs, &ev.LibraryEntryID); err != nil {
		return err
	}
	if err := link(a.Experience, j.expIDs, &ev.ExperienceID); err != nil {
		return err
	}
	if err := link(a.Review, j.revIDs, &ev.ReviewID); err != nil {
		return err
	}
	if err := link(a.List, j.listIDs, &ev.ListID); err != nil {
		return err
	}
	return j.record(ev)
}
