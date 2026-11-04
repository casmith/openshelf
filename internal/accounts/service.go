package accounts

import (
	"context"
	"crypto/rand"
	"errors"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/casmith/openshelf/internal/auth"
	"github.com/casmith/openshelf/internal/db"
	"github.com/casmith/openshelf/internal/deps"
)

var (
	ErrSetupDone     = errors.New("accounts: setup already completed")
	ErrLastAdmin     = errors.New("accounts: the last admin can't be demoted or disabled")
	ErrSelf          = errors.New("accounts: you can't disable your own account")
	ErrUsernameTaken = errors.New("accounts: username taken")
	ErrEmailTaken    = errors.New("accounts: email taken")
)

var usernameRE = regexp.MustCompile(`^[a-z0-9_]{2,30}$`)

func ValidUsername(s string) bool { return usernameRE.MatchString(s) }

func ValidEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	return at > 0 && at < len(s)-1 && !strings.ContainsAny(s, " \t\r\n") && strings.Count(s, "@") == 1
}

func ValidTimezone(tz string) bool {
	if tz == "" || tz == "Local" {
		return false
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}

// Timezones is the curated picker list; any valid IANA zone is still accepted.
var Timezones = []string{
	"UTC",
	"Pacific/Honolulu", "America/Anchorage", "America/Los_Angeles", "America/Phoenix", "America/Denver",
	"America/Chicago", "America/New_York", "America/Halifax", "America/St_Johns", "America/Mexico_City",
	"America/Bogota", "America/Lima", "America/Santiago", "America/Sao_Paulo", "America/Argentina/Buenos_Aires",
	"Atlantic/Reykjavik", "Europe/London", "Europe/Dublin", "Europe/Lisbon", "Europe/Paris", "Europe/Berlin",
	"Europe/Madrid", "Europe/Rome", "Europe/Amsterdam", "Europe/Stockholm", "Europe/Warsaw", "Europe/Athens",
	"Europe/Helsinki", "Europe/Istanbul", "Europe/Kyiv", "Europe/Moscow", "Africa/Lagos", "Africa/Cairo",
	"Africa/Johannesburg", "Africa/Nairobi", "Asia/Dubai", "Asia/Tehran", "Asia/Karachi", "Asia/Kolkata",
	"Asia/Kathmandu", "Asia/Dhaka", "Asia/Bangkok", "Asia/Jakarta", "Asia/Singapore", "Asia/Shanghai",
	"Asia/Hong_Kong", "Asia/Taipei", "Asia/Manila", "Asia/Seoul", "Asia/Tokyo", "Australia/Perth",
	"Australia/Adelaide", "Australia/Brisbane", "Australia/Sydney", "Pacific/Auckland",
}

// TempPassword returns a random password for admin-created accounts and resets.
func TempPassword() string { return rand.Text()[:16] }

type NewUser struct {
	Username, DisplayName, Password string
	Email                           *string
	Role                            db.UserRole
}

func createUser(ctx context.Context, q *db.Queries, u NewUser) (db.User, error) {
	hash, err := auth.HashPassword(u.Password)
	if err != nil {
		return db.User{}, err
	}
	created, err := q.CreateUser(ctx, db.CreateUserParams{
		Username: u.Username, DisplayName: u.DisplayName, Email: u.Email, PasswordHash: hash, Role: u.Role,
	})
	return created, uniqueErr(err)
}

func uniqueErr(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		switch pe.ConstraintName {
		case "users_username_key":
			return ErrUsernameTaken
		case "users_email_key":
			return ErrEmailTaken
		}
	}
	return err
}

// Setup creates the first admin, failing with ErrSetupDone once any user exists.
func Setup(ctx context.Context, d *deps.Deps, u NewUser) (db.User, error) {
	var created db.User
	err := d.WithTx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		if err := q.AccountsLockSetup(ctx); err != nil {
			return err
		}
		n, err := q.CountUsers(ctx)
		if err != nil {
			return err
		}
		if n > 0 {
			return ErrSetupDone
		}
		u.Role = db.UserRoleAdmin
		created, err = createUser(ctx, q, u)
		return err
	})
	return created, err
}

func CreateUser(ctx context.Context, q *db.Queries, u NewUser) (db.User, error) {
	return createUser(ctx, q, u)
}

func UpdateProfile(ctx context.Context, q *db.Queries, userID int64, displayName string, email *string) (db.User, error) {
	u, err := q.AccountsUpdateProfile(ctx, db.AccountsUpdateProfileParams{ID: userID, DisplayName: displayName, Email: email})
	return u, uniqueErr(err)
}

func SetPassword(ctx context.Context, q *db.Queries, userID int64, password string) error {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	return q.AccountsSetPassword(ctx, db.AccountsSetPasswordParams{ID: userID, PasswordHash: hash})
}

// modifyUser locks active admins, then the target, so concurrent changes can't remove the last admin.
func modifyUser(ctx context.Context, d *deps.Deps, username string, fn func(q *db.Queries, target db.User, activeAdmins []int64) error) error {
	return d.WithTx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		admins, err := q.AccountsLockActiveAdmins(ctx)
		if err != nil {
			return err
		}
		target, err := q.AccountsGetUserForUpdate(ctx, username)
		if err != nil {
			return err
		}
		return fn(q, target, admins)
	})
}

func isLastActiveAdmin(u db.User, activeAdmins []int64) bool {
	return u.Role == db.UserRoleAdmin && u.DisabledAt == nil && len(activeAdmins) == 1 && activeAdmins[0] == u.ID
}

func SetRole(ctx context.Context, d *deps.Deps, username string, role db.UserRole) error {
	return modifyUser(ctx, d, username, func(q *db.Queries, target db.User, admins []int64) error {
		if role != db.UserRoleAdmin && isLastActiveAdmin(target, admins) {
			return ErrLastAdmin
		}
		return q.AccountsSetRole(ctx, db.AccountsSetRoleParams{ID: target.ID, Role: role})
	})
}

func SetDisabled(ctx context.Context, d *deps.Deps, actor db.User, username string, disabled bool) error {
	return modifyUser(ctx, d, username, func(q *db.Queries, target db.User, admins []int64) error {
		var at *time.Time
		if disabled {
			if target.ID == actor.ID {
				return ErrSelf
			}
			if isLastActiveAdmin(target, admins) {
				return ErrLastAdmin
			}
			if target.DisabledAt != nil {
				return nil
			}
			now := d.Now()
			at = &now
		}
		return q.AccountsSetDisabled(ctx, db.AccountsSetDisabledParams{ID: target.ID, DisabledAt: at})
	})
}

// ResetPassword sets and returns a new temporary password.
func ResetPassword(ctx context.Context, d *deps.Deps, username string) (db.User, string, error) {
	u, err := d.Q.GetUserByUsername(ctx, username)
	if err != nil {
		return u, "", err
	}
	pw := TempPassword()
	return u, pw, SetPassword(ctx, d.Q, u.ID, pw)
}
