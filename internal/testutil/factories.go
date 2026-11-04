package testutil

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/casmith/openshelf/internal/db"
)

// Factories insert minimal valid rows. Adjust extra columns with SQL updates in the test.

func one[T any](t testing.TB, pool *pgxpool.Pool, sql string, args ...any) T {
	t.Helper()
	rows, err := pool.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("factory query: %v", err)
	}
	v, err := pgx.CollectOneRow(rows, pgx.RowToStructByPos[T])
	if err != nil {
		t.Fatalf("factory scan: %v", err)
	}
	return v
}

func CreateItem(t testing.TB, pool *pgxpool.Pool, kind db.MediaKind, title string) db.MediaItem {
	t.Helper()
	return one[db.MediaItem](t, pool,
		`insert into media_items (kind, title, sort_title) values ($1, $2, $2) returning *`, kind, title)
}

// CreateEdition adds an edition; platformSlug may be "" for non-games.
func CreateEdition(t testing.TB, pool *pgxpool.Pool, itemID int64, platformSlug string) db.Edition {
	t.Helper()
	return one[db.Edition](t, pool,
		`insert into editions (media_item_id, platform_id)
		 values ($1, (select id from platforms where slug = nullif($2, ''))) returning *`, itemID, platformSlug)
}

func CreateEntry(t testing.TB, pool *pgxpool.Pool, userID, itemID int64) db.LibraryEntry {
	t.Helper()
	return one[db.LibraryEntry](t, pool,
		`insert into library_entries (user_id, media_item_id) values ($1, $2) returning *`, userID, itemID)
}

func CreateExperience(t testing.TB, pool *pgxpool.Pool, userID, itemID int64, state db.ExperienceState) db.Experience {
	t.Helper()
	return one[db.Experience](t, pool,
		`insert into experiences (user_id, media_item_id, state, started_on, finished_on)
		 values ($1, $2, $3::text::experience_state, current_date, case when $3::text in ('finished', 'abandoned') then current_date end)
		 returning *`, userID, itemID, state)
}

func CreateReview(t testing.TB, pool *pgxpool.Pool, userID, itemID int64, rating int, vis db.Visibility) db.Review {
	t.Helper()
	return one[db.Review](t, pool,
		`insert into reviews (user_id, media_item_id, rating, visibility) values ($1, $2, $3, $4) returning *`,
		userID, itemID, rating, vis)
}

func CreateList(t testing.TB, pool *pgxpool.Pool, userID int64, name string, vis db.Visibility, itemIDs ...int64) db.List {
	t.Helper()
	l := one[db.List](t, pool,
		`insert into lists (user_id, name, visibility) values ($1, $2, $3) returning *`, userID, name, vis)
	for i, id := range itemIDs {
		if _, err := pool.Exec(context.Background(),
			`insert into list_items (list_id, media_item_id, position) values ($1, $2, $3)`, l.ID, id, i+1); err != nil {
			t.Fatalf("add list item: %v", err)
		}
	}
	return l
}
