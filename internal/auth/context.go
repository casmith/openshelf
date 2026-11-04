package auth

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
)

const sessionUserKey = "user_id"

type ctxKey struct{}

func WithUser(ctx context.Context, u db.User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

func CurrentUser(ctx context.Context) (db.User, bool) {
	u, ok := ctx.Value(ctxKey{}).(db.User)
	return u, ok
}

// LoadUser resolves the session's user and stores it in the request context.
// Disabled or deleted users have their session destroyed.
func LoadUser(d *deps.Deps) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := d.Sessions.GetInt64(r.Context(), sessionUserKey)
			if id == 0 {
				next.ServeHTTP(w, r)
				return
			}
			u, err := d.Q.GetUserByID(r.Context(), id)
			switch {
			case errors.Is(err, pgx.ErrNoRows) || (err == nil && u.DisabledAt != nil):
				_ = d.Sessions.Destroy(r.Context())
			case err != nil:
				d.Log.Error("load session user", "err", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			default:
				r = r.WithContext(WithUser(r.Context(), u))
			}
			next.ServeHTTP(w, r)
		})
	}
}

func LogIn(ctx context.Context, d *deps.Deps, userID int64) error {
	if err := d.Sessions.RenewToken(ctx); err != nil {
		return err
	}
	d.Sessions.Put(ctx, sessionUserKey, userID)
	return nil
}

func LogOut(ctx context.Context, d *deps.Deps) error {
	return d.Sessions.Destroy(ctx)
}

func RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := CurrentUser(r.Context()); !ok {
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login?next="+SafeNext(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func RequireAdmin(next http.Handler) http.Handler {
	return RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, _ := CurrentUser(r.Context()); u.Role != db.UserRoleAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// SafeNext only allows local absolute paths as redirect targets.
func SafeNext(next string) string {
	if len(next) == 0 || next[0] != '/' || (len(next) > 1 && (next[1] == '/' || next[1] == '\\')) {
		return "/"
	}
	return next
}
