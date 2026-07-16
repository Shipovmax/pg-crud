# pg-crud — PostgreSQL CRUD Service

> HTTP REST service featuring comprehensive PostgreSQL integration: connection pooling, migrations, and optimistic concurrency control. Project #7 in the Go backend roadmap — first hands-on experience with persistent storage.

---

## For Recruiter

### What this is and why

pg-crud is an HTTP service for managing entity records (users) backed by PostgreSQL. Following the in-memory kv-store (#6), Project #7 introduces persistent storage with a production-grade setup: connection pooling via `pgxpool`, raw SQL migrations, and optimistic concurrency control where safe concurrent writes are required.

This is exactly the type of project where most junior developers make critical mistakes: opening a new connection per request, ignoring rollback logic on errors, or bleeding SQL queries into business logic. Here, layers are strictly isolated: the repository layer encapsulates raw SQL, handlers deal exclusively with HTTP, and `main.go` only acts as wire-up.

### What this project demonstrates

| Skill | Implementation |
|-------|---------------|
| Connection pooling | `pgxpool.New` with tuned `MaxConns` and `MinConns` configurations |
| SQL migrations | `golang-migrate/migrate` — up/down SQL files with schema versioning |
| Optimistic concurrency | Version column + conditional `UPDATE ... WHERE version = $n`, no lost updates |
| Repository pattern | SQL execution isolated in `repository/`; handlers remain agnostic to database queries |
| Error handling | Proper mapping: `pgx.ErrNoRows` → 404, constraint violations → 409 Conflict |
| Graceful shutdown | `http.Server.Shutdown` paired with clean connection pool termination |
| Environment config | DSN management using environment variables instead of hardcoded strings |

### Stack

- Language: Go 1.25
- Dependencies: `pgx/v5`, `golang-migrate/migrate/v4`, `redis/go-redis/v9`, `sony/gobreaker`, `golang.org/x/sync/singleflight`, `prometheus/client_golang`, `go.opentelemetry.io/otel`, `testcontainers-go` (integration tests only)
- Infrastructure: PostgreSQL 16, Redis 7, Docker
- Platform: Linux/macOS

---

## For Developer

### Architectural decisions

#### WHY pgxpool over database/sql

`database/sql` is a generalized abstraction designed to work with any database driver. In contrast, `pgxpool` is a native PostgreSQL pool that offers out-of-the-box support for PostgreSQL-specific data types (`pgtype`), LISTEN/NOTIFY commands, and binary data copying. For production-grade Go+Postgres services, `pgx` is the industry standard. `database/sql` is only necessary when seamless compatibility with multiple relational database engines is required.

#### WHY golang-migrate over inline SQL execution

Executing raw SQL strings directly within `db.Exec` upon application startup is a major anti-pattern: it lacks schema versioning, automated rollback capability, and environment reproducibility. `golang-migrate` addresses this by providing sequentially numbered `up` and `down` migration files, ensuring a transparent schema history and the ability to roll back changes cleanly. This is standard practice in production environments.

#### WHY the repository pattern

HTTP handlers should have no awareness of raw SQL queries. The repository layer is designed as the sole domain containing the `pgxpool.Pool` and raw SQL statement strings. Handlers interact with this layer strictly through the `UserRepository` interface. This architecture provides excellent testability (via mock repositories), absolute decoupling between business logic and storage, and the flexibility to switch database backends without modifying the HTTP transport layer.

#### WHY optimistic concurrency control instead of explicit transactions

```go
// version predicate makes the write conditional: a concurrent committed
// Update bumps version, so a caller holding the old value matches zero
// rows instead of silently overwriting (lost update)
const q = `UPDATE users SET name = $1, email = $2, version = version + 1
           WHERE id = $3 AND version = $4
           RETURNING id, name, email, version, created_at`
```

Every write in this service is a single statement, and PostgreSQL guarantees atomicity per statement — there is no multi-statement unit of work here that would require `pool.Begin()`/`Commit()`/`Rollback()`. An earlier revision wrapped `Update` in an explicit transaction purely to re-check row existence after a failed conditional update; that transaction bought nothing (no second write to keep atomic with the first) and was dropped in favor of the `version` column driving the whole conflict-detection logic. Optimistic concurrency was chosen over row-level locking (`SELECT ... FOR UPDATE`) because writes here are infrequent relative to reads and contention is expected to be low — a stale `version` simply yields `409 Conflict` for the client to retry, instead of holding a lock for the duration of the request.

### Structure

```
pg-crud/
├── main.go            # wire-up: pool initialization, migrations, server startup, and graceful shutdown
├── config/
│   └── config.go      # parses environment variables into a structured Config object
├── repository/
│   └── user.go        # SQL operations: Create, GetByID, List, Update, Delete
├── handler/
│   └── user.go        # HTTP handlers decoupled via the UserRepository interface
├── migrations/
│   ├── 000001_create_users.up.sql
│   └── 000001_create_users.down.sql
├── docker-compose.yml # localized PostgreSQL 16 infrastructure setup
├── README.md
└── task.md

```

### Setup and run

```bash
# Spin up PostgreSQL + Redis
docker compose up -d

# Spin up the service
DATABASE_URL="postgres://postgres:postgres@localhost:5432/pgcrud?sslmode=disable" \
go run ./...

```

Optional environment variables (defaults in parentheses): `SERVER_ADDR` (`:8080`), `POOL_MAX_CONNS` (`10`), `REDIS_ADDR` (`localhost:6379`), `REDIS_POOL_SIZE` (`10`), `REDIS_MIN_IDLE_CONNS` (`2`), `CACHE_TTL_SECONDS` (`60`), `BREAKER_THRESHOLD` (`5`), `BREAKER_COOLDOWN_SECONDS` (`30`), `API_KEYS` (comma-separated; unset disables auth — logged loudly at startup, not a silent default), `OTEL_EXPORTER_ENDPOINT` (unset exports traces to stdout instead of an OTLP collector).

Operational endpoints: `/metrics` (Prometheus), `/healthz` (liveness), `/readyz` (readiness — requires Postgres; a degraded Redis is reported but does not fail readiness, since the cache layer fails open).

`/users/*` requires an `X-API-Key` header matching one of `API_KEYS` (constant-time comparison); `/healthz`, `/readyz` and `/metrics` are unauthenticated.

### Testing

```bash
# Unit tests (fakes + miniredis, no external services)
go test -race ./...

# Integration tests against a real Postgres container (needs Docker)
go test -tags=integration -race ./repository/...

# Static analysis
golangci-lint run ./...

```

### Usage

If `API_KEYS` is set, every `/users/*` call below needs `-H "X-API-Key: <one of API_KEYS>"`.

```bash
# Create a user
curl -X POST http://localhost:8080/users \
  -H "Content-Type: application/json" \
  -H "X-API-Key: dev-key" \
  -d '{"name":"Alice","email":"alice@example.com"}'

# Get a user by ID
curl -H "X-API-Key: dev-key" http://localhost:8080/users/1

# List users (paginated, limit <= 100)
curl -H "X-API-Key: dev-key" "http://localhost:8080/users?limit=20&offset=0"

# Update a user — version implements optimistic locking: send the version
# you read; a stale version yields 409 instead of a silent lost update
curl -X PUT http://localhost:8080/users/1 \
  -H "Content-Type: application/json" \
  -H "X-API-Key: dev-key" \
  -d '{"name":"Alice Updated","email":"alice@example.com","version":1}'

# Delete a user
curl -X DELETE -H "X-API-Key: dev-key" http://localhost:8080/users/1

```

### Examples

```bash
$ curl -X POST http://localhost:8080/users \
  -H "Content-Type: application/json" \
  -d '{"name":"Bob","email":"bob@example.com"}'
{"id":1,"name":"Bob","email":"bob@example.com","created_at":"2026-01-01T12:00:00Z"}

$ curl http://localhost:8080/users/999
{"error":"user not found"}
# HTTP 404 Not Found

$ curl -X POST http://localhost:8080/users \
  -H "Content-Type: application/json" \
  -d '{"name":"Bob2","email":"bob@example.com"}'
{"error":"email already exists"}
# HTTP 409 Conflict

```

### Error handling

```bash
# Not found
GET /users/999 → 404 {"error":"user not found"}

# Duplicate email (unique constraint violation)
POST /users (existing email) → 409 {"error":"email already exists"}

# Invalid JSON payload
POST /users (malformed body) → 400 {"error":"invalid request body"}

# Missing required validation fields
POST /users (empty email) → 400 {"error":"email is required"}

# Stale optimistic-lock version (concurrent update won)
PUT /users/1 (old version) → 409 {"error":"version conflict"}

# Missing or wrong API key (only when API_KEYS is set)
any /users/* request → 401 {"error":"invalid or missing API key"}

# Database connectivity failure
any request → 503 {"error":"service unavailable"}

```

### Run without build

```bash
DATABASE_URL="postgres://postgres:postgres@localhost:5432/pgcrud?sslmode=disable" \
go run ./...

```

