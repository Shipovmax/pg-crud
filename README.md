# pg-crud — PostgreSQL CRUD Service

> HTTP REST сервис с полноценной работой с PostgreSQL: пул соединений, миграции, транзакции. Project #7 в Go backend roadmap — первое знакомство с persistent storage.

---

## For Recruiter

### What this is and why

pg-crud — HTTP сервис для управления сущностями (users) с хранением данных в PostgreSQL. Проект #7 следует за in-memory kv-store (#6) и впервые вводит persistent storage с production-grade подходом: connection pool через `pgxpool`, SQL-миграции как код, явные транзакции там где нужна атомарность.

Это именно тот проект, где большинство junior-разработчиков делают критические ошибки: открывают новое соединение на каждый запрос, игнорируют rollback при ошибке, смешивают SQL с бизнес-логикой. Здесь всё разделено: repository слой изолирует SQL, handler слой — HTTP, main.go только wire-up.

### What this project demonstrates

| Skill | Implementation |
|-------|---------------|
| Connection pooling | `pgxpool.New` с настройкой `MaxConns`, `MinConns` |
| SQL migrations | `golang-migrate/migrate` — up/down файлы, версионирование схемы |
| Transactions | `pool.Begin()` → defer rollback → explicit commit |
| Repository pattern | SQL изолирован в `repository/`, handler не знает про SQL |
| Error handling | `pgx.ErrNoRows` → 404, constraint violations → 409 |
| Graceful shutdown | `http.Server.Shutdown` + закрытие пула соединений |
| Environment config | DSN через переменные окружения, не хардкод |

### Stack

- Language: Go 1.23
- Dependencies: `pgx/v5`, `golang-migrate/migrate/v4`
- Infrastructure: PostgreSQL 16, Docker (для локального запуска БД)
- Platform: Linux/macOS

---

## For Developer

### Architectural decisions

#### WHY pgxpool, не database/sql

`database/sql` — абстракция для любых драйверов. `pgxpool` — нативный PostgreSQL пул с поддержкой PG-специфичных типов (pgtype), LISTEN/NOTIFY, копирования. На production Go+Postgres всегда pgx. `database/sql` нужен когда требуется совместимость с несколькими СУБД.

#### WHY golang-migrate, не руками

SQL в `db.Exec` при старте приложения — антипаттерн: нет версионирования, нет rollback, нет воспроизводимости. `golang-migrate` даёт нумерованные `up`/`down` файлы, чистую историю изменений схемы, возможность откатиться. Это стандарт в продакшене.

#### WHY repository pattern

Handler не должен знать про SQL. Repository — единственное место где есть `pgxpool.Pool` и SQL-строки. Handler работает через интерфейс `UserRepository`. Это даёт: тестируемость (mock репозиторий), независимость слоёв, возможность поменять хранилище без переписывания HTTP слоя.

#### WHY defer tx.Rollback()

```go
tx, _ := pool.Begin(ctx)
defer tx.Rollback(ctx) // no-op если уже был Commit
// ... операции ...
tx.Commit(ctx)
```

`Rollback` после `Commit` — безопасный no-op. Паттерн гарантирует что при любой ошибке транзакция будет откачена, даже если забыть явный rollback.

### Structure

```
pg-crud/
├── main.go                    # wire-up: pool, migrations, server, shutdown
├── config/
│   └── config.go              # читает env vars, возвращает Config struct
├── repository/
│   └── user.go                # SQL: Create, GetByID, List, Update, Delete
├── handler/
│   └── user.go                # HTTP handlers, зависят от UserRepository interface
├── migrations/
│   ├── 000001_create_users.up.sql
│   └── 000001_create_users.down.sql
├── docker-compose.yml         # PostgreSQL 16 локально
├── README.md
└── task.md
```

### Setup and run

```bash
# Поднять PostgreSQL
docker compose up -d

# Запустить сервис
DATABASE_URL="postgres://postgres:postgres@localhost:5432/pgcrud?sslmode=disable" \
go run ./...
```

### Usage

```bash
# Создать пользователя
curl -X POST http://localhost:8080/users \
  -H "Content-Type: application/json" \
  -d '{"name":"Alice","email":"alice@example.com"}'

# Получить пользователя
curl http://localhost:8080/users/1

# Список всех
curl http://localhost:8080/users

# Обновить
curl -X PUT http://localhost:8080/users/1 \
  -d '{"name":"Alice Updated","email":"alice@example.com"}'

# Удалить
curl -X DELETE http://localhost:8080/users/1
```

### Examples

```bash
$ curl -X POST http://localhost:8080/users \
  -d '{"name":"Bob","email":"bob@example.com"}'
{"id":1,"name":"Bob","email":"bob@example.com","created_at":"2025-01-01T12:00:00Z"}

$ curl http://localhost:8080/users/999
{"error":"user not found"}
# HTTP 404

$ curl -X POST http://localhost:8080/users \
  -d '{"name":"Bob2","email":"bob@example.com"}'
{"error":"email already exists"}
# HTTP 409
```

### Error handling

```bash
# Not found
GET /users/999 → 404 {"error":"user not found"}

# Duplicate email (unique constraint)
POST /users (existing email) → 409 {"error":"email already exists"}

# Invalid JSON
POST /users (bad body) → 400 {"error":"invalid request body"}

# Missing required field
POST /users (no email) → 400 {"error":"email is required"}

# DB unavailable
any request → 503 {"error":"service unavailable"}
```

### Run without build

```bash
DATABASE_URL="postgres://postgres:postgres@localhost:5432/pgcrud?sslmode=disable" \
go run ./...
```
