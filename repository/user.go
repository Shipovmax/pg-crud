package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// User is the domain representation of a row in the users table.
type User struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

var (
	ErrNotFound       = errors.New("user not found")
	ErrDuplicateEmail = errors.New("email already exists")
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
	Update(ctx context.Context, id int64, name, email string) (*User, error)
	Delete(ctx context.Context, id int64) error
}

type pgUserRepository struct {
	pool *pgxpool.Pool
}

// NewUserRepository constructs a UserRepository backed by the given pool.
func NewUserRepository(pool *pgxpool.Pool) UserRepository {
	return &pgUserRepository{pool: pool}
}

func (r *pgUserRepository) Create(ctx context.Context, name, email string) (*User, error) {
	const q = `INSERT INTO users (name, email) VALUES ($1, $2) RETURNING id, name, email, created_at`

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	u := &User{}
	err := r.pool.QueryRow(ctx, q, name, email).Scan(&u.ID, &u.Name, &u.Email, &u.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrDuplicateEmail
		}
		return nil, err
	}
	return u, nil
}

func (r *pgUserRepository) GetByID(ctx context.Context, id int64) (*User, error) {
	const q = `SELECT id, name, email, created_at FROM users WHERE id = $1`

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	u := &User{}
	err := r.pool.QueryRow(ctx, q, id).Scan(&u.ID, &u.Name, &u.Email, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return u, nil
}

func (r *pgUserRepository) List(ctx context.Context, limit, offset int) ([]*User, error) {
	const q = `SELECT id, name, email, created_at FROM users ORDER BY id LIMIT $1 OFFSET $2`

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	rows, err := r.pool.Query(ctx, q, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := make([]*User, 0)
	for rows.Next() {
		u := &User{}
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return users, nil
}

func (r *pgUserRepository) Update(ctx context.Context, id int64, name, email string) (*User, error) {
	const q = `UPDATE users SET name = $1, email = $2 WHERE id = $3 RETURNING id, name, email, created_at`

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	u := &User{}
	err := r.pool.QueryRow(ctx, q, name, email, id).Scan(&u.ID, &u.Name, &u.Email, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrDuplicateEmail
		}
		return nil, err
	}
	return u, nil
}

func (r *pgUserRepository) Delete(ctx context.Context, id int64) error {
	const q = `DELETE FROM users WHERE id = $1`

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	tag, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
