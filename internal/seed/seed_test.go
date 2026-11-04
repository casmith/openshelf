package seed_test

import (
	"context"
	"testing"
	"time"

	"github.com/casmith/openshelf/internal/seed"
	"github.com/casmith/openshelf/internal/testutil"
)

func TestSeedIsIdempotent(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	if ran, err := seed.Run(ctx, pool, time.Now()); err != nil || !ran {
		t.Fatalf("first run: ran=%v err=%v", ran, err)
	}
	if ran, err := seed.Run(ctx, pool, time.Now()); err != nil || ran {
		t.Fatalf("second run: ran=%v err=%v", ran, err)
	}
	var items, dreamcastPS2 int
	_ = pool.QueryRow(ctx, "select count(*) from media_items").Scan(&items)
	_ = pool.QueryRow(ctx, `select count(*) from media_items m where
		exists (select 1 from editions e join platforms p on p.id = e.platform_id where e.media_item_id = m.id and p.slug = 'dreamcast') and
		exists (select 1 from editions e join platforms p on p.id = e.platform_id where e.media_item_id = m.id and p.slug = 'ps2')`).Scan(&dreamcastPS2)
	if items < 40 || dreamcastPS2 < 1 {
		t.Fatalf("items=%d dreamcast+ps2=%d", items, dreamcastPS2)
	}
}
