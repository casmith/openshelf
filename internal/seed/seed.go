// Package seed loads deterministic demo data for development.
package seed

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/catalog"
)

const Password = "password"

// Run inserts demo users and data. It is a no-op if user "alice" already exists.
func Run(ctx context.Context, pool *pgxpool.Pool, now time.Time) (bool, error) {
	var exists bool
	if err := pool.QueryRow(ctx, "select exists(select 1 from users where username = 'alice')").Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	hash, err := auth.HashPassword(Password)
	if err != nil {
		return false, err
	}
	return true, pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		s := &seeder{ctx: ctx, tx: tx, now: now, items: map[string]int64{}, creators: map[string]int64{}}
		alice, err := s.user("alice", "Alice", "admin", hash)
		if err != nil {
			return err
		}
		bob, err := s.user("bob", "Bob", "member", hash)
		if err != nil {
			return err
		}
		for _, it := range items {
			if err := s.item(it); err != nil {
				return fmt.Errorf("item %q: %w", it.title, err)
			}
		}
		if err := s.library(alice, aliceLibrary); err != nil {
			return err
		}
		if err := s.library(bob, bobLibrary); err != nil {
			return err
		}
		if err := s.list(alice, "Favorite science fiction", "Across every shelf.", false, "instance",
			"Dune", "Interstellar", "The Expanse", "Pandora's Star", "Outer Wilds"); err != nil {
			return err
		}
		if err := s.list(alice, "Dreamcast essentials", "", true, "public",
			"Shenmue", "Jet Set Radio", "Rez", "Skies of Arcadia"); err != nil {
			return err
		}
		return s.list(bob, "Movie night", "", false, "instance", "Alien", "The Thing", "Arrival")
	})
}

type seeder struct {
	ctx      context.Context
	tx       pgx.Tx
	now      time.Time
	items    map[string]int64
	creators map[string]int64
	step     int
}

// at returns a deterministic timestamp that moves forward with each call, ending near now.
func (s *seeder) at(daysAgo int) time.Time {
	s.step++
	return s.now.Add(-time.Duration(daysAgo)*24*time.Hour + time.Duration(s.step)*time.Minute)
}

func (s *seeder) user(username, name, role, hash string) (int64, error) {
	var id int64
	err := s.tx.QueryRow(s.ctx,
		`insert into users (username, display_name, password_hash, role, timezone) values ($1, $2, $3, $4, 'America/New_York') returning id`,
		username, name, hash, role).Scan(&id)
	return id, err
}

func (s *seeder) item(it item) error {
	var id int64
	if err := s.tx.QueryRow(s.ctx,
		`insert into media_items (kind, title, sort_title, release_year, synopsis, genres) values ($1, $2, $3, $4, $5, $6) returning id`,
		it.kind, it.title, catalog.SortTitle(it.title), it.year, it.synopsis, it.genres).Scan(&id); err != nil {
		return err
	}
	s.items[it.title] = id
	details := map[string]string{
		"game": "insert into game_details (media_item_id) values ($1)",
		"book": "insert into book_details (media_item_id) values ($1)",
		"film": "insert into film_details (media_item_id) values ($1)",
		"show": "insert into show_details (media_item_id) values ($1)",
	}
	if _, err := s.tx.Exec(s.ctx, details[it.kind], id); err != nil {
		return err
	}
	for i, c := range it.credits {
		cid, ok := s.creators[c.name]
		if !ok {
			if err := s.tx.QueryRow(s.ctx, `insert into creators (name, sort_name) values ($1, $1) returning id`, c.name).Scan(&cid); err != nil {
				return err
			}
			s.creators[c.name] = cid
		}
		if _, err := s.tx.Exec(s.ctx, `insert into credits (media_item_id, creator_id, role, position) values ($1, $2, $3, $4)`,
			id, cid, c.role, i); err != nil {
			return err
		}
	}
	for _, e := range it.editions {
		var pages *int
		if e.pages > 0 {
			pages = &e.pages
		}
		if _, err := s.tx.Exec(s.ctx,
			`insert into editions (media_item_id, format, platform_id, page_count)
			 values ($1, $2, (select id from platforms where slug = nullif($3, '')), $4)`,
			id, e.format, e.platform, pages); err != nil {
			return err
		}
	}
	return nil
}

func (s *seeder) activity(userID int64, verb string, itemID int64, col string, refID int64, vis string, at time.Time) error {
	sql := fmt.Sprintf(`insert into activity_events (user_id, verb, media_item_id, %s, visibility, occurred_at, created_at) values ($1, $2, $3, $4, $5, $6, $6)`, col)
	_, err := s.tx.Exec(s.ctx, sql, userID, verb, itemID, refID, vis, at)
	return err
}

func (s *seeder) library(userID int64, lib map[string]state) error {
	titles := make([]string, 0, len(lib))
	for t := range lib {
		titles = append(titles, t)
	}
	sort.Strings(titles)
	for i, title := range titles {
		st := lib[title]
		itemID, ok := s.items[title]
		if !ok {
			return fmt.Errorf("seed library: unknown item %q", title)
		}
		vis := st.visibility
		if vis == "" {
			vis = "private"
		}
		added := s.at(540 - i*15)
		var ownership *string
		if st.ownership != "" {
			ownership = &st.ownership
		}
		var entryID int64
		if err := s.tx.QueryRow(s.ctx,
			`insert into library_entries (user_id, media_item_id, ownership_user, backlog, created_at, updated_at)
			 values ($1, $2, $3, $4, $5, $5) returning id`, userID, itemID, ownership, st.backlog, added).Scan(&entryID); err != nil {
			return err
		}
		if err := s.activity(userID, "added", itemID, "library_entry_id", entryID, "private", added); err != nil {
			return err
		}

		runs := 0
		switch st.experience {
		case "active", "paused", "finished", "abandoned":
			runs = 1
		case "rewatched":
			runs = 2
		}
		var lastExp int64
		for r := 0; r < runs; r++ {
			expState := st.experience
			if expState == "rewatched" {
				expState = "finished"
			}
			started := added.Add(time.Duration(3+r*120) * 24 * time.Hour)
			if started.After(s.now) {
				started = s.now.Add(-48 * time.Hour)
			}
			var finished *time.Time
			if expState == "finished" || expState == "abandoned" {
				f := started.Add(time.Duration(5+i%20) * 24 * time.Hour)
				if f.After(s.now) {
					f = s.now.Add(-time.Hour)
				}
				finished = &f
			}
			var unit *string
			var cur, total *float64
			if st.unit != "" {
				unit, cur = &st.unit, &st.current
				if st.total > 0 {
					total = &st.total
				}
			}
			if err := s.tx.QueryRow(s.ctx,
				`insert into experiences (user_id, media_item_id, state, started_on, finished_on, progress_unit, progress_current, progress_total, visibility, created_at, updated_at)
				 values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10) returning id`,
				userID, itemID, expState, started, finished, unit, cur, total, vis, started).Scan(&lastExp); err != nil {
				return err
			}
			if err := s.activity(userID, "started", itemID, "experience_id", lastExp, vis, started); err != nil {
				return err
			}
			if cur != nil {
				if _, err := s.tx.Exec(s.ctx, `insert into progress_events (experience_id, occurred_at, value) values ($1, $2, $3)`,
					lastExp, started.Add(48*time.Hour), *cur); err != nil {
					return err
				}
			}
			if finished != nil {
				verb := "finished"
				if expState == "abandoned" {
					verb = "abandoned"
				}
				if err := s.activity(userID, verb, itemID, "experience_id", lastExp, vis, *finished); err != nil {
					return err
				}
			}
		}

		if st.rating > 0 || st.review != "" {
			var rating *int
			if st.rating > 0 {
				rating = &st.rating
			}
			var body *string
			if st.review != "" {
				body = &st.review
			}
			var expRef *int64
			if lastExp != 0 {
				expRef = &lastExp
			}
			at := s.at(10 + i)
			var reviewID int64
			if err := s.tx.QueryRow(s.ctx,
				`insert into reviews (user_id, media_item_id, experience_id, rating, body, contains_spoilers, visibility, created_at, updated_at)
				 values ($1, $2, $3, $4, $5, $6, $7, $8, $8) returning id`,
				userID, itemID, expRef, rating, body, st.spoilers, vis, at).Scan(&reviewID); err != nil {
				return err
			}
			verb := "rated"
			if body != nil {
				verb = "reviewed"
			}
			if err := s.activity(userID, verb, itemID, "review_id", reviewID, vis, at); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *seeder) list(userID int64, name, desc string, ranked bool, vis string, titles ...string) error {
	var listID int64
	var descPtr *string
	if desc != "" {
		descPtr = &desc
	}
	if err := s.tx.QueryRow(s.ctx,
		`insert into lists (user_id, name, description, ranked, visibility) values ($1, $2, $3, $4, $5) returning id`,
		userID, name, descPtr, ranked, vis).Scan(&listID); err != nil {
		return err
	}
	for i, t := range titles {
		itemID, ok := s.items[t]
		if !ok {
			return errors.New("seed list: unknown item " + t)
		}
		if _, err := s.tx.Exec(s.ctx, `insert into list_items (list_id, media_item_id, position) values ($1, $2, $3)`, listID, itemID, i+1); err != nil {
			return err
		}
		if err := s.activity(userID, "listed", itemID, "list_id", listID, vis, s.at(5)); err != nil {
			return err
		}
	}
	return nil
}
