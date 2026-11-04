package testutil

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var tracers sync.Map // *pgxpool.Pool -> *queryTracer

type queryTracer struct{ n atomic.Int64 }

func (q *queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	q.n.Add(1)
	return ctx
}

func (q *queryTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// CountQueries returns how many queries pool executed while fn ran. HTTP requests
// include the session load/save queries, so budget for those.
func CountQueries(t testing.TB, pool *pgxpool.Pool, fn func()) int {
	t.Helper()
	v, ok := tracers.Load(pool)
	if !ok {
		t.Fatal("CountQueries: pool was not created by testutil.NewDB")
	}
	tr := v.(*queryTracer)
	before := tr.n.Load()
	fn()
	return int(tr.n.Load() - before)
}
