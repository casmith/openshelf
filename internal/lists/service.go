package lists

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/activity"
	"github.com/casmith/openshelf/internal/db"
)

// Move directions accepted by MoveItem.
const (
	MoveUp     = "up"
	MoveDown   = "down"
	MoveTop    = "top"
	MoveBottom = "bottom"
)

var ErrBadMove = errors.New("lists: invalid move direction")

// AddItem appends item to list and records a "listed" event. It reports false
// when the item was already on the list. Call it with tx-bound queries after
// locking the list with ListsLockOwned.
func AddItem(ctx context.Context, q *db.Queries, list db.List, itemID int64) (bool, error) {
	_, err := q.ListsAddItem(ctx, db.ListsAddItemParams{ListID: list.ID, MediaItemID: itemID})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := activity.Record(ctx, q, activity.Event{
		UserID:      list.UserID,
		Verb:        db.ActivityVerbListed,
		MediaItemID: itemID,
		ListID:      &list.ID,
		Visibility:  list.Visibility,
	}); err != nil {
		return false, err
	}
	return true, q.ListsTouch(ctx, db.ListsTouchParams{ID: list.ID, UserID: list.UserID})
}

// RemoveItem deletes item from list and closes the gap in positions.
// It returns pgx.ErrNoRows when the item isn't on the list.
func RemoveItem(ctx context.Context, q *db.Queries, list db.List, itemID int64) error {
	n, err := q.ListsRemoveItem(ctx, db.ListsRemoveItemParams{ListID: list.ID, MediaItemID: itemID})
	if err != nil {
		return err
	}
	if n == 0 {
		return pgx.ErrNoRows
	}
	rows, err := q.ListsItemIDs(ctx, list.ID)
	if err != nil {
		return err
	}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	if err := q.ListsSetPositions(ctx, db.ListsSetPositionsParams{ListID: list.ID, Ids: ids}); err != nil {
		return err
	}
	return q.ListsTouch(ctx, db.ListsTouchParams{ID: list.ID, UserID: list.UserID})
}

// MoveItem moves item one step or to an end and renumbers positions 1..n.
// It returns pgx.ErrNoRows when the item isn't on the list.
func MoveItem(ctx context.Context, q *db.Queries, list db.List, itemID int64, dir string) error {
	rows, err := q.ListsItemIDs(ctx, list.ID)
	if err != nil {
		return err
	}
	ids := make([]int64, len(rows))
	from := -1
	for i, r := range rows {
		ids[i] = r.ID
		if r.MediaItemID == itemID {
			from = i
		}
	}
	if from < 0 {
		return pgx.ErrNoRows
	}
	var to int
	switch dir {
	case MoveUp:
		to = max(from-1, 0)
	case MoveDown:
		to = min(from+1, len(ids)-1)
	case MoveTop:
		to = 0
	case MoveBottom:
		to = len(ids) - 1
	default:
		return ErrBadMove
	}
	ids = reorder(ids, from, to)
	if err := q.ListsSetPositions(ctx, db.ListsSetPositionsParams{ListID: list.ID, Ids: ids}); err != nil {
		return err
	}
	return q.ListsTouch(ctx, db.ListsTouchParams{ID: list.ID, UserID: list.UserID})
}

func reorder(ids []int64, from, to int) []int64 {
	v := ids[from]
	out := make([]int64, 0, len(ids))
	out = append(out, ids[:from]...)
	out = append(out, ids[from+1:]...)
	out = append(out[:to], append([]int64{v}, out[to:]...)...)
	return out
}
