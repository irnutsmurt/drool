package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"fleetmgr/internal/ids"
	"fleetmgr/internal/store/db"
)

var (
	ErrUserNotFound    = errors.New("user not found")
	ErrUsernameTaken   = errors.New("username already exists")
	ErrInvalidUsername = errors.New("username must be 1-64 characters: letters, digits, '.', '_', '-' or '@'")
	ErrInvalidRole     = fmt.Errorf("role must be one of %s", strings.Join(Roles, ", "))
	ErrLastAdmin       = errors.New("operation would leave no enabled administrator")
	ErrSelfModify      = errors.New("you cannot delete, disable or demote your own account")
)

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

// CreateUser adds a local account.
func (s *Service) CreateUser(ctx context.Context, username, password, role string) (db.User, error) {
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
	u := db.User{ID: ids.New(ids.User), Username: username, PasswordHash: hash, Role: role,
		CreatedAt: now, UpdatedAt: now, PasswordChangedAt: now}
	err = s.st.W.CreateUser(ctx, db.CreateUserParams{
		ID: u.ID, Username: u.Username, PasswordHash: u.PasswordHash, Role: u.Role,
		CreatedAt: now, UpdatedAt: now, PasswordChangedAt: now,
	})
	if isUniqueViolation(err) {
		return db.User{}, ErrUsernameTaken
	}
	return u, err
}

// UserUpdate holds optional changes; nil fields are left unchanged.
type UserUpdate struct {
	Role     *string
	Disabled *bool
	Password *string
}

// UpdateUser applies changes made by actor. Changing a password or disabling
// a user revokes that user's sessions (except the actor's current session
// when changing their own password).
func (s *Service) UpdateUser(ctx context.Context, actor *Principal, id string, up UserUpdate) (db.User, error) {
	if up.Role != nil && !ValidRole(*up.Role) {
		return db.User{}, ErrInvalidRole
	}
	var newHash string
	if up.Password != nil {
		if err := ValidatePassword(*up.Password); err != nil {
			return db.User{}, err
		}
		h, err := HashPassword(*up.Password)
		if err != nil {
			return db.User{}, err
		}
		newHash = h
	}
	self := actor != nil && actor.UserID == id

	var out db.User
	err := s.st.InTx(ctx, func(q *db.Queries) error {
		u, err := q.GetUser(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUserNotFound
		}
		if err != nil {
			return err
		}
		now := s.now()
		demoting := up.Role != nil && *up.Role != RoleAdmin && u.Role == RoleAdmin
		disabling := up.Disabled != nil && *up.Disabled && u.Disabled == 0
		if self && (demoting || disabling) {
			return ErrSelfModify
		}
		if (demoting || disabling) && u.Role == RoleAdmin && u.Disabled == 0 {
			n, err := q.CountActiveAdmins(ctx)
			if err != nil {
				return err
			}
			if n <= 1 {
				return ErrLastAdmin
			}
		}
		if up.Role != nil && *up.Role != u.Role {
			if err := q.UpdateUserRole(ctx, db.UpdateUserRoleParams{Role: *up.Role, UpdatedAt: now, ID: id}); err != nil {
				return err
			}
			u.Role = *up.Role
		}
		if up.Disabled != nil {
			v := boolToInt(*up.Disabled)
			if v != u.Disabled {
				if err := q.UpdateUserDisabled(ctx, db.UpdateUserDisabledParams{Disabled: v, UpdatedAt: now, ID: id}); err != nil {
					return err
				}
				u.Disabled = v
			}
			if *up.Disabled {
				if err := q.DeleteUserSessions(ctx, id); err != nil {
					return err
				}
			}
		}
		if newHash != "" {
			if err := q.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{
				PasswordHash: newHash, PasswordChangedAt: now, UpdatedAt: now, ID: id,
			}); err != nil {
				return err
			}
			u.PasswordHash = newHash
			if self && actor.Kind == KindSession {
				err = q.DeleteUserSessionsExcept(ctx, db.DeleteUserSessionsExceptParams{UserID: id, TokenHash: actor.SessionHash})
			} else {
				err = q.DeleteUserSessions(ctx, id)
			}
			if err != nil {
				return err
			}
		}
		u.UpdatedAt = now
		out = u
		return nil
	})
	return out, err
}

// DeleteUser removes an account and, via cascade, its sessions and tokens.
func (s *Service) DeleteUser(ctx context.Context, actor *Principal, id string) error {
	if actor != nil && actor.UserID == id {
		return ErrSelfModify
	}
	return s.st.InTx(ctx, func(q *db.Queries) error {
		u, err := q.GetUser(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUserNotFound
		}
		if err != nil {
			return err
		}
		if u.Role == RoleAdmin && u.Disabled == 0 {
			n, err := q.CountActiveAdmins(ctx)
			if err != nil {
				return err
			}
			if n <= 1 {
				return ErrLastAdmin
			}
		}
		return q.DeleteUser(ctx, id)
	})
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func isUniqueViolation(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || se.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
	}
	return false
}
