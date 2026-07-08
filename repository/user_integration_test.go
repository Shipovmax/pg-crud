//go:build integration

// These tests spin up a real PostgreSQL container via testcontainers-go and
// run the actual migrations against it, exercising SQL that the unit tests
// (which use fakes/miniredis) never touch: the unique email constraint, the
// optimistic-lock version predicate, and real pgx error mapping. They need
// a Docker daemon, so they're gated behind the "integration" build tag and
// run in a separate CI job, not on every `go test ./...`.
package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

// newIntegrationPool starts a Postgres container, applies migrations/ against
// it, and returns a pool pointed at it. The container and pool are torn down
// via t.Cleanup.
func newIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("pgcrud"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Logf("terminate postgres container: %v", err)
		}
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	m, err := migrate.New("file://../migrations", dsn)
	if err != nil {
		t.Fatalf("migrate.New: %v", err)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	if srcErr, dbErr := m.Close(); srcErr != nil || dbErr != nil {
		t.Fatalf("migrate close: source=%v db=%v", srcErr, dbErr)
	}

	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse pool config: %v", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

func TestPgUserRepository_CreateGetDelete(t *testing.T) {
	pool := newIntegrationPool(t)
	repo := NewUserRepository(pool)
	ctx := context.Background()

	created, err := repo.Create(ctx, "Alice", "alice@example.com")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Version != 1 {
		t.Fatalf("new row version = %d, want 1", created.Version)
	}

	got, err := repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Email != "alice@example.com" {
		t.Fatalf("got email %q, want alice@example.com", got.Email)
	}

	if err := repo.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.GetByID(ctx, created.ID); err != ErrNotFound {
		t.Fatalf("GetByID after delete: got %v, want ErrNotFound", err)
	}
}

func TestPgUserRepository_DuplicateEmailConstraint(t *testing.T) {
	pool := newIntegrationPool(t)
	repo := NewUserRepository(pool)
	ctx := context.Background()

	if _, err := repo.Create(ctx, "Alice", "dup@example.com"); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := repo.Create(ctx, "Bob", "dup@example.com"); err != ErrDuplicateEmail {
		t.Fatalf("second Create: got %v, want ErrDuplicateEmail", err)
	}
}

func TestPgUserRepository_UpdateOptimisticLock(t *testing.T) {
	pool := newIntegrationPool(t)
	repo := NewUserRepository(pool)
	ctx := context.Background()

	u, err := repo.Create(ctx, "Alice", "occ@example.com")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := repo.Update(ctx, u.ID, "Alice2", "occ2@example.com", u.Version)
	if err != nil {
		t.Fatalf("first Update: %v", err)
	}
	if updated.Version != u.Version+1 {
		t.Fatalf("version after update = %d, want %d", updated.Version, u.Version+1)
	}

	// u.Version is now stale — a real concurrent writer already won.
	if _, err := repo.Update(ctx, u.ID, "Alice3", "occ3@example.com", u.Version); err != ErrVersionConflict {
		t.Fatalf("stale Update: got %v, want ErrVersionConflict", err)
	}
}

func TestPgUserRepository_ListPagination(t *testing.T) {
	pool := newIntegrationPool(t)
	repo := NewUserRepository(pool)
	ctx := context.Background()

	for i := range 5 {
		email := fmt.Sprintf("list-user-%d@example.com", i)
		if _, err := repo.Create(ctx, "User", email); err != nil {
			t.Fatalf("seed user %d: %v", i, err)
		}
	}

	page, err := repo.List(ctx, 2, 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("got %d users, want 2", len(page))
	}
}
