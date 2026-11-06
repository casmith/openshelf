// Package runner applies sync adapter observations to the database and owns the
// River jobs that schedule account syncs.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/catalog"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
	"github.com/casmith/openshelf/internal/discover"
	"github.com/casmith/openshelf/internal/images"
	"github.com/casmith/openshelf/internal/providers"
	"github.com/casmith/openshelf/internal/sync"
)

const (
	DefaultInterval = 12 * time.Hour
	RetryInterval   = time.Hour
	// DeferredInterval brings the next sync forward while a resolver has deferred items.
	DeferredInterval = 15 * time.Minute
	maxWarnings      = 50
	maxErrorLen      = 1000
)

var ErrAdapterUnavailable = errors.New("sync: adapter is not available or not configured")

// Counts summarizes a run; stored as sync_runs.counts.
type Counts struct {
	Games           int `json:"games"`
	WithProgress    int `json:"with_progress"`
	Deferred        int `json:"deferred"`
	ItemsResolved   int `json:"items_resolved"`
	ItemsCreated    int `json:"items_created"`
	EntriesCreated  int `json:"entries_created"`
	EntriesUpdated  int `json:"entries_updated"`
	Excluded        int `json:"excluded"`
	Skipped         int `json:"skipped"`
	AchievementSets int `json:"achievement_sets"`
	Unlocks         int `json:"unlocks"`
}

type Result struct {
	RunID    int64
	Status   db.SyncRunStatus
	Counts   Counts
	Warnings []string
	Err      error // the adapter's error, already recorded on the run
}

// Run syncs one account now. Adapter errors are recorded on the run and account
// and returned in Result.Err; the returned error is only for bookkeeping failures.
// A missing account returns pgx.ErrNoRows; a disabled one is skipped.
func Run(ctx context.Context, d *deps.Deps, accountID int64) (Result, error) {
	acct, err := d.Q.SyncGetAccount(ctx, accountID)
	if err != nil {
		return Result{}, err
	}
	if acct.Status == db.ConnectionStatusDisabled {
		return Result{}, nil
	}
	run, err := d.Q.SyncStartRun(ctx, acct.ID)
	if err != nil {
		return Result{}, err
	}
	a, ok := d.SyncAdapters.Get(acct.Provider)
	em := &emitter{d: d, acct: acct, runID: run.ID, items: map[string]int64{}}
	if r, isResolver := a.(sync.Resolver); ok && isResolver {
		em.resolver = r
	}

	var cursor []byte
	var syncErr error
	if !ok || !a.Configured() {
		syncErr = ErrAdapterUnavailable
	} else {
		cursor, syncErr = a.Sync(ctx, acct, acct.Cursor, em)
	}

	if em.counts.Deferred > 0 {
		em.Warn(fmt.Sprintf("%d %s queued for matching next sync", em.counts.Deferred, kindNoun(em.deferredKind, em.counts.Deferred)))
	}
	res := Result{RunID: run.ID, Counts: em.counts, Warnings: em.warnings, Err: syncErr}
	switch {
	case syncErr == nil && len(em.warnings) == 0:
		res.Status = db.SyncRunStatusSucceeded
	case syncErr == nil || em.applied > 0:
		res.Status = db.SyncRunStatusPartial
	default:
		res.Status = db.SyncRunStatusFailed
	}
	return res, finish(context.WithoutCancel(ctx), d, acct, res, cursor)
}

func finish(ctx context.Context, d *deps.Deps, acct db.ConnectedAccount, res Result, cursor []byte) error {
	counts, err := json.Marshal(res.Counts)
	if err != nil {
		return err
	}
	now := d.Now()
	var errText *string
	if res.Err != nil {
		s := res.Err.Error()
		if len(s) > maxErrorLen {
			s = s[:maxErrorLen]
		}
		errText = &s
	}
	warnings := res.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	acctParams := db.SyncFinishAccountParams{ID: acct.ID, LastError: errText}
	if res.Err == nil {
		acctParams.Status = db.ConnectionStatusActive
		acctParams.Cursor = cursor
		acctParams.NextSyncAt = NextSync(now, DefaultInterval)
		if res.Counts.Deferred > 0 {
			acctParams.NextSyncAt = NextSync(now, DeferredInterval)
		}
	} else {
		acctParams.Status = db.ConnectionStatusError
		acctParams.NextSyncAt = NextSync(now, RetryInterval)
	}
	if res.Status != db.SyncRunStatusFailed {
		acctParams.LastSyncedAt = &now
	}
	return d.WithTx(ctx, func(q *db.Queries, tx pgx.Tx) error {
		if err := q.SyncFinishRun(ctx, db.SyncFinishRunParams{
			ID: res.RunID, Status: res.Status, Counts: counts, Warnings: warnings, Error: errText,
		}); err != nil {
			return err
		}
		return q.SyncFinishAccount(ctx, acctParams)
	})
}

// NextSync returns now+interval with ±10% jitter so linked accounts spread out.
func NextSync(now time.Time, interval time.Duration) time.Time {
	j := time.Duration(rand.Int64N(int64(interval)/5+1)) - interval/10
	return now.Add(interval + j)
}

type emitter struct {
	d        *deps.Deps
	acct     db.ConnectedAccount
	runID    int64
	resolver sync.Resolver
	items    map[string]int64 // provider item key -> media item, for this run
	counts   Counts
	warnings []string
	applied  int

	deferredKind db.MediaKind
}

func kindNoun(k db.MediaKind, n int) string {
	noun := "item"
	switch k {
	case db.MediaKindBook:
		noun = "book"
	case db.MediaKindGame:
		noun = "game"
	}
	if n != 1 {
		noun += "s"
	}
	return noun
}

func (e *emitter) deferred(g sync.OwnedItem) {
	if e.counts.Deferred == 0 {
		e.deferredKind = g.ItemKind()
	} else if e.deferredKind != g.ItemKind() {
		e.deferredKind = ""
	}
	e.counts.Deferred++
}

func (e *emitter) Warn(msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" || len(e.warnings) >= maxWarnings {
		return
	}
	for _, w := range e.warnings {
		if w == msg {
			return
		}
	}
	e.warnings = append(e.warnings, msg)
}

func (e *emitter) event(ctx context.Context, q *db.Queries, ev db.SyncInsertEventParams) error {
	ev.RunID, ev.AccountID, ev.UserID = e.runID, e.acct.ID, e.acct.UserID
	return q.SyncInsertEvent(ctx, ev)
}

func (e *emitter) excluded(ctx context.Context, key string) (bool, error) {
	return e.d.Q.SyncIsExcluded(ctx, db.SyncIsExcludedParams{AccountID: e.acct.ID, Key: key})
}

// findItem matches an observation to an item of its kind, first by item-level
// external IDs, then by edition IDs such as ISBNs.
func findItem(ctx context.Context, q *db.Queries, g sync.OwnedItem) (int64, bool, error) {
	kind := g.ItemKind()
	for _, x := range g.ExternalIDs {
		if x.Provider == "" || x.Value == "" {
			continue
		}
		id, err := q.SyncFindItemByExternalID(ctx, db.SyncFindItemByExternalIDParams{Provider: x.Provider, Value: x.Value, Kind: kind})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		return id, err == nil, err
	}
	if g.Edition == nil {
		return 0, false, nil
	}
	for _, x := range g.Edition.ExternalIDs {
		if x.Provider == "" || x.Value == "" {
			continue
		}
		id, err := q.SyncFindItemByEditionExternalID(ctx, db.SyncFindItemByEditionExternalIDParams{Provider: x.Provider, Value: x.Value, Kind: kind})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		return id, err == nil, err
	}
	return 0, false, nil
}

func (e *emitter) OwnedItem(ctx context.Context, g sync.OwnedItem) error {
	if g.Key == "" {
		e.Warn("skipped an item without an ID")
		return nil
	}
	e.counts.Games++
	if g.Progress.Started() {
		e.counts.WithProgress++
	}
	if ex, err := e.excluded(ctx, g.Key); err != nil {
		return err
	} else if ex {
		e.counts.Excluded++
		key := g.Key
		return e.event(ctx, e.d.Q, db.SyncInsertEventParams{Kind: sync.EventExcludedSkipped, ProviderItemKey: &key})
	}

	itemID, found, err := findItem(ctx, e.d.Q, g)
	if err != nil {
		return err
	}
	var fix *manualFix
	if found {
		if fix, err = e.planManualFix(ctx, g, itemID); err != nil {
			return err
		}
	} else if e.resolver != nil {
		res, err := e.resolver.Resolve(ctx, g)
		if errors.Is(err, sync.ErrDeferred) {
			e.deferred(g)
			return nil
		}
		if err == nil && res != nil && res.Item != nil {
			var item db.MediaItem
			item, err = discover.Import(ctx, e.d, res.Provider, res.Item, nil)
			if err == nil && item.Kind == g.ItemKind() {
				itemID, found = item.ID, true
				e.counts.ItemsResolved++
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			e.counts.Skipped++
			e.Warn(fmt.Sprintf("Couldn't match %q (%s); will retry next sync: %v", g.Title, g.Key, err))
			return nil
		}
	}

	err = e.d.WithTx(ctx, func(q *db.Queries, tx pgx.Tx) error {
		if fix != nil {
			if err := e.applyManualFix(ctx, q, tx, g, itemID, *fix); err != nil {
				return err
			}
		}
		var err error
		itemID, err = e.applyOwned(ctx, q, tx, g, itemID, found)
		return err
	})
	if err != nil {
		return err
	}
	e.items[g.Key] = itemID
	e.applied++
	return nil
}

// manualFix updates an item sync created without a match whose observed title
// changed (e.g. an adapter now strips edition suffixes), matching it to a provider
// item when the resolver now finds one.
type manualFix struct {
	oldTitle, title string
	res             *sync.Resolution
}

func (e *emitter) planManualFix(ctx context.Context, g sync.OwnedItem, itemID int64) (*manualFix, error) {
	title := strings.TrimSpace(g.Title)
	if title == "" {
		return nil, nil
	}
	m, err := e.d.Q.SyncGetManualItem(ctx, itemID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil || m.Title == title || slices.Contains(m.LockedFields, discover.FieldTitle) {
		return nil, err
	}
	fix := &manualFix{oldTitle: m.Title, title: title}
	if e.resolver == nil {
		return fix, nil
	}
	res, err := e.resolver.Resolve(ctx, g)
	switch {
	case errors.Is(err, sync.ErrDeferred):
		e.deferred(g)
		return nil, nil
	case err != nil:
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		e.Warn(fmt.Sprintf("Couldn't match %q (%s); will retry next sync: %v", g.Title, g.Key, err))
		return nil, nil
	case res != nil && res.Item != nil && res.Item.Kind == g.ItemKind():
		fix.res = res
	}
	return fix, nil
}

func (e *emitter) applyManualFix(ctx context.Context, q *db.Queries, tx pgx.Tx, g sync.OwnedItem, itemID int64, fix manualFix) error {
	key := g.Key
	ev := db.SyncInsertEventParams{MediaItemID: &itemID, ProviderItemKey: &key, Field: ptr(sync.FieldTitle), Before: &fix.oldTitle}
	if fix.res != nil {
		item, ok, err := e.matchManual(ctx, q, tx, itemID, fix.res)
		if err != nil {
			return err
		}
		if ok {
			e.counts.ItemsResolved++
			ev.Kind, ev.After = sync.EventItemMatched, &item.Title
			return e.event(ctx, q, ev)
		}
	}
	if err := q.SyncSetManualTitle(ctx, db.SyncSetManualTitleParams{ID: itemID, Title: fix.title, SortTitle: catalog.SortTitle(fix.title)}); err != nil {
		return err
	}
	ev.Kind, ev.After = sync.EventTitleChanged, &fix.title
	return e.event(ctx, q, ev)
}

// matchManual attaches the resolution's IDs to the existing item and refreshes it
// from the provider. It declines when the provider item is already another item.
func (e *emitter) matchManual(ctx context.Context, q *db.Queries, tx pgx.Tx, itemID int64, res *sync.Resolution) (db.MediaItem, bool, error) {
	for _, x := range res.Item.ExternalIDs {
		owner, err := q.SyncExternalIDOwner(ctx, db.SyncExternalIDOwnerParams{Provider: x.Provider, Value: x.Value})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil || owner != itemID {
			return db.MediaItem{}, false, err
		}
	}
	for _, x := range res.Item.ExternalIDs {
		if x.Provider == "" || x.Value == "" {
			continue
		}
		if err := q.SyncAttachExternalID(ctx, db.SyncAttachExternalIDParams{
			Provider: x.Provider, Value: x.Value, Url: optString(x.URL), MediaItemID: &itemID,
		}); err != nil {
			return db.MediaItem{}, false, err
		}
	}
	item, err := discover.ImportTx(ctx, e.d, q, tx, res.Provider, res.Item, nil)
	if err != nil {
		return db.MediaItem{}, false, err
	}
	return item, item.ID == itemID, nil
}

func (e *emitter) applyOwned(ctx context.Context, q *db.Queries, tx pgx.Tx, g sync.OwnedItem, itemID int64, found bool) (int64, error) {
	key := g.Key
	needsReview := false
	if !found {
		if err := q.SyncLockKey(ctx, e.acct.Provider+":"+key); err != nil {
			return 0, err
		}
		var err error
		if itemID, found, err = findItem(ctx, q, g); err != nil {
			return 0, err
		}
		if !found {
			if itemID, err = e.createItem(ctx, q, tx, g); err != nil {
				return 0, err
			}
			needsReview = true
		}
	}
	for _, x := range g.ExternalIDs {
		if x.Provider == "" || x.Value == "" {
			continue
		}
		if err := q.SyncAttachExternalID(ctx, db.SyncAttachExternalIDParams{
			Provider: x.Provider, Value: x.Value, Url: optString(x.URL), MediaItemID: &itemID,
		}); err != nil {
			return 0, err
		}
	}

	base := db.SyncInsertEventParams{MediaItemID: &itemID, ProviderItemKey: &key}
	owned := db.OwnershipOwned
	var observedPlayed *bool
	if (g.PlaytimeMinutes != nil && *g.PlaytimeMinutes > 0) || g.Progress.Started() {
		t := true
		observedPlayed = &t
	}

	entry, err := q.SyncFindEntry(ctx, db.SyncFindEntryParams{UserID: e.acct.UserID, MediaItemID: itemID})
	isNew := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !isNew {
		return 0, err
	}
	var ownChanged, playedChanged bool
	newPlayed := observedPlayed
	if isNew {
		entry.ID, err = q.SyncInsertEntry(ctx, db.SyncInsertEntryParams{
			UserID: e.acct.UserID, MediaItemID: itemID, OwnershipSource: &owned, PlayedSource: observedPlayed,
		})
		if err != nil {
			return 0, err
		}
		e.counts.EntriesCreated++
		ev := base
		ev.LibraryEntryID, ev.After = &entry.ID, optString(string(owned))
		ev.Kind, ev.Field = sync.EventEntryCreated, ptr(sync.FieldOwnership)
		if err := e.event(ctx, q, ev); err != nil {
			return 0, err
		}
		if needsReview {
			ev := base
			ev.LibraryEntryID, ev.Kind, ev.After = &entry.ID, sync.EventNeedsReview, optString(g.Title)
			if err := e.event(ctx, q, ev); err != nil {
				return 0, err
			}
		}
	} else {
		// played_source only ever moves to true: zero playtime isn't evidence of not playing
		// elsewhere, and a false source would hide experience-derived "played".
		if newPlayed == nil {
			newPlayed = entry.PlayedSource
		}
		ownChanged = entry.OwnershipSource == nil || *entry.OwnershipSource != owned
		playedChanged = !eqPtr(entry.PlayedSource, newPlayed)
		if ownChanged || playedChanged {
			if err := q.SyncSetEntrySource(ctx, db.SyncSetEntrySourceParams{
				ID: entry.ID, OwnershipSource: &owned, PlayedSource: newPlayed,
			}); err != nil {
				return 0, err
			}
			e.counts.EntriesUpdated++
		}
		if ownChanged {
			ev := base
			ev.LibraryEntryID, ev.Kind, ev.Field = &entry.ID, sync.EventOwnershipSourceChanged, ptr(sync.FieldOwnership)
			ev.Before, ev.After = ownStr(entry.OwnershipSource), optString(string(owned))
			ev.RespectedOverride = entry.OwnershipUser != nil && *entry.OwnershipUser != owned
			if err := e.event(ctx, q, ev); err != nil {
				return 0, err
			}
		}
		if playedChanged {
			ev := base
			ev.LibraryEntryID, ev.Kind, ev.Field = &entry.ID, sync.EventPlayedSourceChanged, ptr(sync.FieldPlayed)
			ev.Before, ev.After = boolStr(entry.PlayedSource), boolStr(newPlayed)
			ev.RespectedOverride = entry.PlayedUser != nil && *entry.PlayedUser != *newPlayed
			if err := e.event(ctx, q, ev); err != nil {
				return 0, err
			}
		}
	}

	playtimeChanged, err := e.applyPlaytime(ctx, q, g, itemID, entry.ID)
	if err != nil {
		return 0, err
	}
	progressChanged, err := e.applyProgress(ctx, q, g, itemID, entry.ID)
	if err != nil {
		return 0, err
	}

	// When something new arrived for the item but the effective value is the user's, say so.
	if !isNew && (playtimeChanged || progressChanged) {
		if !playedChanged && entry.PlayedUser != nil && newPlayed != nil && *entry.PlayedUser != *newPlayed {
			ev := base
			ev.LibraryEntryID, ev.Kind, ev.Field = &entry.ID, sync.EventOverrideRespected, ptr(sync.FieldPlayed)
			ev.Before, ev.After, ev.RespectedOverride = boolStr(newPlayed), boolStr(entry.PlayedUser), true
			if err := e.event(ctx, q, ev); err != nil {
				return 0, err
			}
		}
		if !ownChanged && entry.OwnershipUser != nil && *entry.OwnershipUser != owned {
			ev := base
			ev.LibraryEntryID, ev.Kind, ev.Field = &entry.ID, sync.EventOverrideRespected, ptr(sync.FieldOwnership)
			ev.Before, ev.After, ev.RespectedOverride = optString(string(owned)), ownStr(entry.OwnershipUser), true
			if err := e.event(ctx, q, ev); err != nil {
				return 0, err
			}
		}
	}
	return itemID, nil
}

func (e *emitter) createItem(ctx context.Context, q *db.Queries, tx pgx.Tx, g sync.OwnedItem) (int64, error) {
	kind := g.ItemKind()
	title := strings.TrimSpace(g.Title)
	if title == "" {
		title = "Unknown " + string(kind) + " " + g.Key
	}
	p := db.SyncInsertManualItemParams{Kind: kind, Title: title, SortTitle: catalog.SortTitle(title)}
	if g.ReleaseYear > 0 && g.ReleaseYear < 1<<15 {
		y := int16(g.ReleaseYear)
		p.ReleaseYear = &y
	}
	id, err := q.SyncInsertManualItem(ctx, p)
	if err != nil {
		return 0, err
	}
	switch kind {
	case db.MediaKindGame:
		err = q.SyncInsertGameDetails(ctx, id)
	case db.MediaKindBook:
		bp := db.SyncInsertBookDetailsParams{MediaItemID: id}
		if g.Book != nil {
			bp.SeriesName, bp.SeriesPosition = optString(g.Book.SeriesName), g.Book.SeriesPosition
		}
		err = q.SyncInsertBookDetails(ctx, bp)
	}
	if err != nil {
		return 0, err
	}
	if err := insertCredits(ctx, q, id, g.Credits); err != nil {
		return 0, err
	}
	if g.Edition != nil {
		if err := insertEdition(ctx, q, id, *g.Edition); err != nil {
			return 0, err
		}
	}
	if g.CoverURL != "" {
		imageID, err := images.Enqueue(ctx, e.d, tx, g.CoverURL)
		if err != nil {
			return 0, err
		}
		if err := q.SyncSetItemCover(ctx, db.SyncSetItemCoverParams{ID: id, ImageID: &imageID}); err != nil {
			return 0, err
		}
	}
	e.counts.ItemsCreated++
	return id, nil
}

func insertCredits(ctx context.Context, q *db.Queries, itemID int64, credits []providers.Credit) error {
	pos := map[db.CreditRole]int16{}
	for _, c := range credits {
		name := strings.TrimSpace(c.Name)
		if name == "" || !c.Role.Valid() {
			continue
		}
		creatorID, err := q.SyncFindCreatorByName(ctx, name)
		if errors.Is(err, pgx.ErrNoRows) {
			creatorID, err = q.SyncInsertCreator(ctx, db.SyncInsertCreatorParams{Name: name, SortName: name})
		}
		if err != nil {
			return err
		}
		if err := q.SyncInsertCredit(ctx, db.SyncInsertCreditParams{
			MediaItemID: itemID, CreatorID: creatorID, Role: c.Role, Position: pos[c.Role],
		}); err != nil {
			return err
		}
		pos[c.Role]++
	}
	return nil
}

func insertEdition(ctx context.Context, q *db.Queries, itemID int64, ed providers.Edition) error {
	var format *db.EditionFormat
	if ed.Format != "" && ed.Format.Valid() {
		format = &ed.Format
	}
	edID, err := q.SyncInsertEdition(ctx, db.SyncInsertEditionParams{
		MediaItemID: itemID, Format: format, Publisher: optString(ed.Publisher), DurationMinutes: ed.DurationMinutes,
	})
	if err != nil {
		return err
	}
	for _, x := range ed.ExternalIDs {
		if x.Provider == "" || x.Value == "" {
			continue
		}
		if err := q.SyncAttachEditionExternalID(ctx, db.SyncAttachEditionExternalIDParams{
			Provider: x.Provider, Value: x.Value, Url: optString(x.URL), EditionID: &edID,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (e *emitter) applyPlaytime(ctx context.Context, q *db.Queries, g sync.OwnedItem, itemID, entryID int64) (bool, error) {
	if g.PlaytimeMinutes == nil {
		return false, nil
	}
	minutes := clampInt32(*g.PlaytimeMinutes)
	var twoWeeks *int32
	if g.Playtime2Weeks != nil {
		v := clampInt32(*g.Playtime2Weeks)
		twoWeeks = &v
	}
	prev, err := q.SyncGetPlaytime(ctx, db.SyncGetPlaytimeParams{AccountID: e.acct.ID, Key: g.Key})
	isNew := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !isNew {
		return false, err
	}
	lastPlayed := g.LastPlayedAt
	if lastPlayed != nil {
		lastPlayed = ptr(lastPlayed.Truncate(time.Microsecond))
	}
	if lastPlayed == nil && !isNew {
		lastPlayed = prev.LastPlayedAt
	}
	if !isNew && prev.MediaItemID == itemID && prev.MinutesTotal == minutes &&
		eqPtr(prev.Minutes2weeks, twoWeeks) && eqTime(prev.LastPlayedAt, lastPlayed) {
		return false, nil
	}
	if err := q.SyncUpsertPlaytime(ctx, db.SyncUpsertPlaytimeParams{
		AccountID: e.acct.ID, MediaItemID: itemID, Key: g.Key,
		MinutesTotal: minutes, Minutes2weeks: twoWeeks, LastPlayedAt: lastPlayed,
	}); err != nil {
		return false, err
	}
	changed := (isNew && minutes > 0) || (!isNew && prev.MinutesTotal != minutes)
	if !changed {
		return false, nil
	}
	key := g.Key
	ev := db.SyncInsertEventParams{
		MediaItemID: &itemID, LibraryEntryID: &entryID, ProviderItemKey: &key,
		Kind: sync.EventPlaytimeChanged, Field: ptr(sync.FieldPlaytime), After: ptr(strconv.Itoa(int(minutes))),
	}
	if !isNew {
		ev.Before = ptr(strconv.Itoa(int(prev.MinutesTotal)))
	}
	return true, e.event(ctx, q, ev)
}

// progressEpsilon ignores float noise when comparing fractions; events store 4 decimals.
const progressEpsilon = 0.00005

func (e *emitter) applyProgress(ctx context.Context, q *db.Queries, g sync.OwnedItem, itemID, entryID int64) (bool, error) {
	p := g.Progress
	if p == nil {
		return false, nil
	}
	fraction := min(max(p.Fraction, 0), 1)
	if p.Finished {
		fraction = 1
	}
	prev, err := q.SyncGetProgress(ctx, db.SyncGetProgressParams{AccountID: e.acct.ID, Key: g.Key})
	isNew := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !isNew {
		return false, err
	}
	next := db.ExternalProgress{
		MediaItemID: itemID, Fraction: fraction, CurrentSeconds: p.CurrentSeconds, DurationSeconds: p.DurationSeconds,
		Finished: p.Finished, StartedAt: truncTime(p.StartedAt), FinishedAt: truncTime(p.FinishedAt),
		LastActivityAt: truncTime(p.LastActivityAt),
	}
	if !isNew {
		next.CurrentSeconds = coalesce(next.CurrentSeconds, prev.CurrentSeconds)
		next.DurationSeconds = coalesce(next.DurationSeconds, prev.DurationSeconds)
		next.StartedAt = coalesce(prev.StartedAt, next.StartedAt)
		next.FinishedAt = coalesce(next.FinishedAt, prev.FinishedAt)
		next.LastActivityAt = coalesce(next.LastActivityAt, prev.LastActivityAt)
		if prev.MediaItemID == itemID && math.Abs(prev.Fraction-fraction) < progressEpsilon && prev.Finished == next.Finished &&
			eqPtr(prev.CurrentSeconds, next.CurrentSeconds) && eqPtr(prev.DurationSeconds, next.DurationSeconds) &&
			eqTime(prev.StartedAt, next.StartedAt) && eqTime(prev.FinishedAt, next.FinishedAt) &&
			eqTime(prev.LastActivityAt, next.LastActivityAt) {
			return false, nil
		}
	}
	if err := q.SyncUpsertProgress(ctx, db.SyncUpsertProgressParams{
		AccountID: e.acct.ID, MediaItemID: itemID, Key: g.Key, Fraction: next.Fraction,
		CurrentSeconds: next.CurrentSeconds, DurationSeconds: next.DurationSeconds, Finished: next.Finished,
		StartedAt: next.StartedAt, FinishedAt: next.FinishedAt, LastActivityAt: next.LastActivityAt,
	}); err != nil {
		return false, err
	}

	key := g.Key
	base := db.SyncInsertEventParams{MediaItemID: &itemID, LibraryEntryID: &entryID, ProviderItemKey: &key}
	changed := false
	if (isNew && fraction > 0) || (!isNew && math.Abs(prev.Fraction-fraction) >= progressEpsilon) {
		ev := base
		ev.Kind, ev.Field, ev.After = sync.EventProgressChanged, ptr(sync.FieldProgress), ptr(fractionStr(fraction))
		if !isNew {
			ev.Before = ptr(fractionStr(prev.Fraction))
		}
		if err := e.event(ctx, q, ev); err != nil {
			return false, err
		}
		changed = true
	}
	if next.Finished && (isNew || !prev.Finished) {
		ev := base
		ev.Kind, ev.Field, ev.After = sync.EventSourceFinished, ptr(sync.FieldFinished), ptr("true")
		if next.FinishedAt != nil {
			ev.After = ptr(next.FinishedAt.UTC().Format(time.DateOnly))
		}
		if !isNew {
			ev.Before = ptr("false")
		}
		if err := e.event(ctx, q, ev); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

func fractionStr(f float64) string { return strconv.FormatFloat(f, 'f', 4, 64) }

func truncTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	return ptr(t.Truncate(time.Microsecond))
}

func coalesce[T any](a, b *T) *T {
	if a != nil {
		return a
	}
	return b
}

func (e *emitter) Achievements(ctx context.Context, s sync.AchievementSet) error {
	if s.Key == "" {
		e.Warn("skipped achievements for a game without an ID")
		return nil
	}
	if ex, err := e.excluded(ctx, s.Key); err != nil || ex {
		return err
	}
	itemID, ok := e.items[s.Key]
	if !ok {
		id, err := e.d.Q.SyncPlaytimeItem(ctx, db.SyncPlaytimeItemParams{AccountID: e.acct.ID, Key: s.Key})
		switch {
		case err == nil:
			itemID, ok = id, true
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		default:
			if itemID, ok, err = findItem(ctx, e.d.Q, sync.OwnedItem{ExternalIDs: s.ExternalIDs}); err != nil {
				return err
			}
		}
	}
	if !ok {
		e.Warn(fmt.Sprintf("Skipped achievements for %s: the game isn't in the library yet", s.Key))
		return nil
	}
	e.counts.AchievementSets++
	err := e.d.WithTx(ctx, func(q *db.Queries, tx pgx.Tx) error {
		return e.applyAchievements(ctx, q, s, itemID)
	})
	if err == nil {
		e.applied++
	}
	return err
}

func (e *emitter) applyAchievements(ctx context.Context, q *db.Queries, s sync.AchievementSet, itemID int64) error {
	names := map[string]string{}
	for _, a := range s.Achievements {
		if a.Key == "" {
			continue
		}
		name := strings.TrimSpace(a.Name)
		if name == "" {
			name = a.Key
		}
		names[a.Key] = name
		if err := q.SyncUpsertAchievement(ctx, db.SyncUpsertAchievementParams{
			MediaItemID: &itemID, Provider: e.acct.Provider, GameKey: s.Key, AchievementKey: a.Key,
			Name: name, Description: optString(a.Description), IconUrl: optString(a.IconURL),
			Hidden: a.Hidden, DisplayOrder: clampInt32(a.Order),
		}); err != nil {
			return err
		}
	}
	if len(s.Unlocks) == 0 {
		return nil
	}
	rows, err := q.SyncAchievementIDs(ctx, db.SyncAchievementIDsParams{Provider: e.acct.Provider, GameKey: s.Key})
	if err != nil {
		return err
	}
	ids := make(map[string]int64, len(rows))
	for _, r := range rows {
		ids[r.ProviderAchievementKey] = r.ID
	}
	var entryID *int64
	if entry, err := q.SyncFindEntry(ctx, db.SyncFindEntryParams{UserID: e.acct.UserID, MediaItemID: itemID}); err == nil {
		entryID = &entry.ID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	key := s.Key
	unknown := 0
	for _, u := range s.Unlocks {
		achID, ok := ids[u.Key]
		if !ok {
			unknown++
			continue
		}
		mode := u.Mode
		if mode == "" {
			mode = sync.ModeStandard
		}
		inserted, err := q.SyncUpsertUnlock(ctx, db.SyncUpsertUnlockParams{
			AccountID: e.acct.ID, AchievementID: achID, UnlockedAt: u.UnlockedAt, Mode: mode,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if !inserted {
			continue
		}
		e.counts.Unlocks++
		name := names[u.Key]
		if name == "" {
			name = u.Key
		}
		if err := e.event(ctx, q, db.SyncInsertEventParams{
			MediaItemID: &itemID, LibraryEntryID: entryID, ProviderItemKey: &key,
			Kind: sync.EventAchievementUnlocked, Field: ptr(u.Key), After: &name,
		}); err != nil {
			return err
		}
	}
	if unknown > 0 {
		e.Warn(fmt.Sprintf("Skipped %d unlocks for %s with no matching achievement definition", unknown, s.Key))
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

func optString(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

func eqPtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func eqTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func boolStr(b *bool) *string {
	if b == nil {
		return nil
	}
	return ptr(strconv.FormatBool(*b))
}

func ownStr(o *db.Ownership) *string {
	if o == nil {
		return nil
	}
	return ptr(string(*o))
}

func clampInt32(v int) int32 {
	switch {
	case v < 0:
		return 0
	case v > 1<<31-1:
		return 1<<31 - 1
	}
	return int32(v)
}
