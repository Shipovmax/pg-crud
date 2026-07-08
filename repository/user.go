package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("pg-crud/repository")

// endSpan records err on span (if any) and closes it. Kept tiny and
// called from a defer with a named return, so every repository method
// reports its outcome without repeating the same four lines five times.
func endSpan(span trace.Span, err error) {
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// User is the domain representation of a row in the users table.
type User struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
}

var (
	ErrNotFound       = errors.New("user not found")
	ErrDuplicateEmail = errors.New("email already exists")
	// ErrVersionConflict signals that Update was called with a stale
	// version: another writer committed since the caller read the row.
	ErrVersionConflict = errors.New("user version conflict")
)

// queryTimeout bounds each database round-trip independently of the
// caller's context, so a stuck lock or slow plan can't hang a request
// past this deadline even if the client keeps the connection open.
const queryTimeout = 5 * time.Second

// UserRepository defines the persistence operations available for users.
type UserRepository interface {
	Create(ctx context.Context, name, email string) (*User, error)
	GetByID(ctx context.Context, id int64) (*User, error)
	List(ctx context.Context, limit, offset int) ([]*User, error)
	// Update applies optimistic concurrency control: the write succeeds
	// only if the stored version matches the one the caller read.
	// A stale version yields ErrVersionConflict.
	Update(ctx context.Context, id int64, name, email string, version int64) (*User, error)
	Delete(ctx context.Context, id int64) error
}

type pgUserRepository struct {
	pool *pgxpool.Pool
}

// NewUserRepository constructs a UserRepository backed by the given pool.
func NewUserRepository(pool *pgxpool.Pool) UserRepository {
	return &pgUserRepository{pool: pool}
}

func (r *pgUserRepository) Create(ctx context.Context, name, email string) (u *User, err error) {
	ctx, span := tracer.Start(ctx, "pgUserRepository.Create")
	defer func() { endSpan(span, err) }()

	const q = `INSERT INTO users (name, email) VALUES ($1, $2) RETURNING id, name, email, version, created_at`

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	u = &User{}
	err = r.pool.QueryRow(ctx, q, name, email).Scan(&u.ID, &u.Name, &u.Email, &u.Version, &u.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			err = ErrDuplicateEmail
			return nil, err
		}
		return nil, err
	}
	return u, nil
}

func (r *pgUserRepository) GetByID(ctx context.Context, id int64) (u *User, err error) {
	ctx, span := tracer.Start(ctx, "pgUserRepository.GetByID")
	defer func() { endSpan(span, err) }()

	const q = `SELECT id, name, email, version, created_at FROM users WHERE id = $1`

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	u = &User{}
	err = r.pool.QueryRow(ctx, q, id).Scan(&u.ID, &u.Name, &u.Email, &u.Version, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = ErrNotFound
			return nil, err
		}
		return nil, err
	}
	return u, nil
}

func (r *pgUserRepository) List(ctx context.Context, limit, offset int) (users []*User, err error) {
	ctx, span := tracer.Start(ctx, "pgUserRepository.List")
	defer func() { endSpan(span, err) }()

	const q = `SELECT id, name, email, version, created_at FROM users ORDER BY id LIMIT $1 OFFSET $2`

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	rows, err := r.pool.Query(ctx, q, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users = make([]*User, 0)
	for rows.Next() {
		u := &User{}
		if err = rows.Scan(&u.ID, &u.Name, &u.Email, &u.Version, &u.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return users, nil
}

func (r *pgUserRepository) Update(ctx context.Context, id int64, name, email string, version int64) (u *User, err error) {
	ctx, span := tracer.Start(ctx, "pgUserRepository.Update")
	defer func() { endSpan(span, err) }()

	// The version predicate makes the write conditional: a concurrent
	// committed Update bumps version, so a caller holding the old value
	// matches zero rows instead of silently overwriting (lost update).
	const q = `UPDATE users SET name = $1, email = $2, version = version + 1
	           WHERE id = $3 AND version = $4
	           RETURNING id, name, email, version, created_at`

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	u = &User{}
	err = r.pool.QueryRow(ctx, q, name, email, id, version).Scan(&u.ID, &u.Name, &u.Email, &u.Version, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Zero rows means either the row is gone or the version is
			// stale — distinguish so the API can return 404 vs 409.
			var exists bool
			if exErr := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, id).Scan(&exists); exErr != nil {
				err = fmt.Errorf("check user existence: %w", exErr)
				return nil, err
			}
			if exists {
				err = ErrVersionConflict
				return nil, err
			}
			err = ErrNotFound
			return nil, err
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			err = ErrDuplicateEmail
			return nil, err
		}
		return nil, err
	}
	return u, nil
}

func (r *pgUserRepository) Delete(ctx context.Context, id int64) (err error) {
	ctx, span := tracer.Start(ctx, "pgUserRepository.Delete")
	defer func() { endSpan(span, err) }()

	const q = `DELETE FROM users WHERE id = $1`

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	tag, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		err = ErrNotFound
		return err
	}
	return nil
}
