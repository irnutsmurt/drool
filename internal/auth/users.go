package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"fleetmgr/internal/ids"
	"fleetmgr/internal/store"
	"fleetmgr/internal/store/db"
)

var (
	ErrUserNotFound    = errors.New("user not found")
	ErrUsernameTaken   = errors.New("username already exists")
	ErrInvalidUsername = errors.New("username must be 1-64 characters: letters, digits, '.', '_', '-' or '@'")
	ErrInvalidRole     = fmt.Errorf("role must be one of %s", strings.Join(Roles, ", "))
	ErrLastAdmin       = errors.New("operation would leave no enabled administrator")
	ErrSelfModify      = errors.New("you cannot delete, disable or demote your own account")
	ErrTokenNotFound   = errors.New("token not found")
)

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

// Mutations come in two forms: XxxTx(ctx, q, ...) runs inside a caller's
// transaction (so the API can commit the change and its audit record
// together), and Xxx(ctx, ...) wraps it in its own transaction. Expensive
// work such as password hashing happens before the transaction starts so it
// doesn't hold the database write lock.

// NewUser validates input and hashes the password. Nothing is written.
func (s *Service) NewUser(username, password, role string) (db.User, error) {
	username = strings.TrimSpace(username)
	if !usernameRE.MatchString(username) {
		return db.User{}, ErrInvalidUsername
	}
	if !ValidRole(role) {
		return db.User{}, ErrInvalidRole
	}
	if err := ValidatePassword(password); err != nil {
		return db.User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return db.User{}, err
	}
	now := s.now()
	return db.User{ID: ids.New(ids.User), Username: username, PasswordHash: hash, Role: role,
		CreatedAt: now, UpdatedAt: now, PasswordChangedAt: now}, nil
}

// InsertUserTx stores a user prepared by NewUser.
func (s *Service) InsertUserTx(ctx context.Context, q *db.Queries, u db.User) error {
	err := q.CreateUser(ctx, db.CreateUserParams{
		ID: u.ID, Username: u.Username, PasswordHash: u.PasswordHash, Role: u.Role,
		CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt, PasswordChangedAt: u.PasswordChangedAt,
	})
	if isUniqueViolation(err) {
		return ErrUsernameTaken
	}
	return err
}

// CreateUser validates, hashes and stores a local account.
func (s *Service) CreateUser(ctx context.Context, username, password, role string) (db.User, error) {
	u, err := s.NewUser(username, password, role)
	if err != nil {
		return db.User{}, err
	}
	return u, s.st.InTx(ctx, func(q *db.Queries) error { return s.InsertUserTx(ctx, q, u) })
}

// UserUpdate holds optional changes; nil fields are left unchanged.
type UserUpdate struct {
	Role     *string
	Disabled *bool
	Password *string

	passwordHash string // set by Prepare
}

// Prepare validates the update and hashes a new password. Call it before
// UpdateUserTx.
func (up *UserUpdate) Prepare() error {
	if up.Role != nil && !ValidRole(*up.Role) {
		return ErrInvalidRole
	}
	if up.Password != nil {
		if err := ValidatePassword(*up.Password); err != nil {
			return err
		}
		h, err := HashPassword(*up.Password)
		if err != nil {
			return err
		}
		up.passwordHash = h
	}
	return nil
}

// UpdateUser applies changes made by actor in its own transaction.
func (s *Service) UpdateUser(ctx context.Context, actor *Principal, id string, up UserUpdate) (db.User, error) {
	if err := up.Prepare(); err != nil {
		return db.User{}, err
	}
	var out db.User
	err := s.st.InTx(ctx, func(q *db.Queries) error {
		var err error
		out, err = s.UpdateUserTx(ctx, q, actor, id, up)
		return err
	})
	return out, err
}

// UpdateUserTx applies a prepared update. Changing a password or disabling a
// user revokes that user's sessions (except the actor's current session when
// changing their own password).
func (s *Service) UpdateUserTx(ctx context.Context, q *db.Queries, actor *Principal, id string, up UserUpdate) (db.User, error) {
	if up.Password != nil && up.passwordHash == "" {
		return db.User{}, errors.New("auth: UserUpdate.Prepare not called")
	}
	self := actor != nil && actor.UserID == id
	u, err := q.GetUser(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.User{}, ErrUserNotFound
	}
	if err != nil {
		return db.User{}, err
	}
	now := s.now()
	demoting := up.Role != nil && *up.Role != RoleAdmin && u.Role == RoleAdmin
	disabling := up.Disabled != nil && *up.Disabled && u.Disabled == 0
	if self && (demoting || disabling) {
		return db.User{}, ErrSelfModify
	}
	if (demoting || disabling) && u.Role == RoleAdmin && u.Disabled == 0 {
		n, err := q.CountActiveAdmins(ctx)
		if err != nil {
			return db.User{}, err
		}
		if n <= 1 {
			return db.User{}, ErrLastAdmin
		}
	}
	if up.Role != nil && *up.Role != u.Role {
		if err := q.UpdateUserRole(ctx, db.UpdateUserRoleParams{Role: *up.Role, UpdatedAt: now, ID: id}); err != nil {
			return db.User{}, err
		}
		u.Role = *up.Role
	}
	if up.Disabled != nil {
		v := boolToInt(*up.Disabled)
		if v != u.Disabled {
			if err := q.UpdateUserDisabled(ctx, db.UpdateUserDisabledParams{Disabled: v, UpdatedAt: now, ID: id}); err != nil {
				return db.User{}, err
			}
			u.Disabled = v
		}
		if *up.Disabled {
			if err := q.DeleteUserSessions(ctx, id); err != nil {
				return db.User{}, err
			}
		}
	}
	if up.passwordHash != "" {
		if err := q.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{
			PasswordHash: up.passwordHash, PasswordChangedAt: now, UpdatedAt: now, ID: id,
		}); err != nil {
			return db.User{}, err
		}
		u.PasswordHash = up.passwordHash
		if self && actor.Kind == KindSession {
			err = q.DeleteUserSessionsExcept(ctx, db.DeleteUserSessionsExceptParams{UserID: id, TokenHash: actor.SessionHash})
		} else {
			err = q.DeleteUserSessions(ctx, id)
		}
		if err != nil {
			return db.User{}, err
		}
	}
	u.UpdatedAt = now
	return u, nil
}

// DeleteUser removes an account in its own transaction.
func (s *Service) DeleteUser(ctx context.Context, actor *Principal, id string) error {
	return s.st.InTx(ctx, func(q *db.Queries) error {
		_, err := s.DeleteUserTx(ctx, q, actor, id)
		return err
	})
}

// DeleteUserTx removes an account and, via cascade, its sessions and tokens.
// It returns the deleted user.
func (s *Service) DeleteUserTx(ctx context.Context, q *db.Queries, actor *Principal, id string) (db.User, error) {
	if actor != nil && actor.UserID == id {
		return db.User{}, ErrSelfModify
	}
	u, err := q.GetUser(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.User{}, ErrUserNotFound
	}
	if err != nil {
		return db.User{}, err
	}
	if u.Role == RoleAdmin && u.Disabled == 0 {
		n, err := q.CountActiveAdmins(ctx)
		if err != nil {
			return db.User{}, err
		}
		if n <= 1 {
			return db.User{}, ErrLastAdmin
		}
	}
	return u, q.DeleteUser(ctx, id)
}

// DeleteAPITokenTx revokes one of userID's tokens and returns it.
func (s *Service) DeleteAPITokenTx(ctx context.Context, q *db.Queries, userID, id string) (db.ApiToken, error) {
	t, err := q.GetAPIToken(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && t.UserID != userID) {
		return db.ApiToken{}, ErrTokenNotFound
	}
	if err != nil {
		return db.ApiToken{}, err
	}
	return t, q.DeleteAPIToken(ctx, id)
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func isUniqueViolation(err error) bool { return store.IsUniqueViolation(err) }
