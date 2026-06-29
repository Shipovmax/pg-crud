# Task #7 — PostgreSQL CRUD

## Цель

Написать HTTP REST сервис с полноценной работой с PostgreSQL: connection pool через pgxpool, SQL-миграции через golang-migrate, транзакции, repository pattern. Главная учебная цель — научиться правильно организовывать слой работы с БД: SQL изолирован в repository, handler не знает про pgx, миграции как версионированный код.

---

## Acceptance Criteria

- [ ] `POST /users` создаёт пользователя, возвращает `201` с созданным объектом (включая `id` и `created_at`)
- [ ] `GET /users/:id` возвращает `200` с пользователем или `404 {"error":"user not found"}`
- [ ] `GET /users` возвращает `200` со списком всех пользователей (пустой список = `[]`, не `null`)
- [ ] `PUT /users/:id` обновляет пользователя, возвращает `200` с обновлённым объектом
- [ ] `DELETE /users/:id` удаляет пользователя, возвращает `204 No Content`
- [ ] Дублирующий email → `409 {"error":"email already exists"}`
- [ ] Невалидный JSON → `400 {"error":"invalid request body"}`
- [ ] Отсутствующее обязательное поле → `400 {"error":"<field> is required"}`
- [ ] Миграции применяются автоматически при старте приложения
- [ ] DSN берётся из переменной окружения `DATABASE_URL`, не хардкодится
- [ ] При `Ctrl+C` — graceful shutdown: сначала `http.Server.Shutdown`, потом `pool.Close()`
- [ ] `go vet ./...` проходит без предупреждений
- [ ] Зависимостей только `pgx/v5` и `golang-migrate/migrate/v4`

---

## Технические требования

### Обязательно

| Требование | Детали |
|-----------|--------|
| `pgxpool.New` | пул соединений, не одиночное `pgx.Connect` |
| `golang-migrate` | `migrations/` директория с `up`/`down` SQL файлами |
| Repository interface | handlers зависят от интерфейса, не от `*UserRepository` |
| `pgx.ErrNoRows` → 404 | явная проверка, не generic 500 |
| unique constraint → 409 | детектить через pgconn.PgError, Code `"23505"` |
| `defer tx.Rollback(ctx)` | паттерн безопасного отката транзакции |
| `docker-compose.yml` | PostgreSQL 16, с healthcheck |
| Config из env | `os.Getenv("DATABASE_URL")`, с валидацией при старте |

### Запрещено

- `panic` для обработки ошибок
- SQL-строки в handler слое
- Хардкод DSN или credentials в коде
- Открывать новое соединение на каждый запрос (`pgx.Connect` вместо pool)
- Игнорировать ошибку rollback (`_ = tx.Rollback(ctx)` — допустимо, но только если уже есть явный Commit)

---

## Темы Go, которые ты прокачиваешь

Это не просто список — это checklist того, что обязан использовать в проекте.

- **`pgxpool`** — пул соединений к PostgreSQL. `pgxpool.New(ctx, dsn)` возвращает `*pgxpool.Pool`. Pool переиспользует соединения между запросами. Передаётся через dependency injection в repository.

- **`pgx.ErrNoRows`** — ошибка которую возвращает pgx когда `QueryRow` не нашёл строк. Нужно явно проверять: `if errors.Is(err, pgx.ErrNoRows)` → возвращать доменную ошибку `ErrNotFound`.

- **`pgconn.PgError`** — структура PostgreSQL-ошибки. Code `"23505"` = unique_violation. Детектить через `var pgErr *pgconn.PgError; errors.As(err, &pgErr)`.

- **Транзакции** — `pool.Begin(ctx)` возвращает `pgx.Tx`. Паттерн: `defer tx.Rollback(ctx)` сразу после Begin, в конце `tx.Commit(ctx)`. Использовать там где несколько операций должны быть атомарными.

- **`golang-migrate`** — запуск: `migrate.New("file://migrations", dsn)` → `m.Up()`. Вызывать в `main.go` до старта HTTP сервера. `migrate.ErrNoChange` — не ошибка, игнорировать.

- **Repository pattern** — интерфейс `UserRepository` с методами CRUD. `*pgUserRepository` реализует интерфейс. Handler принимает `UserRepository`, не конкретный тип.

- **`context.Context` в SQL** — все pgx-вызовы принимают ctx первым аргументом. Передавать `r.Context()` из HTTP handler через repository в SQL.

- **Sentinel errors** — определить в repository пакете: `var ErrNotFound = errors.New("not found")`. Handler проверяет через `errors.Is`.

---

## Структура файлов

```
pg-crud/
├── main.go                          # wire-up всего приложения
├── config/
│   └── config.go                    # Config struct + загрузка из env
├── repository/
│   └── user.go                      # интерфейс + реализация SQL операций
├── handler/
│   └── user.go                      # HTTP handlers
├── migrations/
│   ├── 000001_create_users.up.sql   # CREATE TABLE users
│   └── 000001_create_users.down.sql # DROP TABLE users
├── docker-compose.yml
├── go.mod
├── README.md
└── task.md
```

---

## Разбивка по файлам

### `config/config.go`

**За что отвечает:** читает переменные окружения, валидирует их, возвращает типизированный Config.

**Типы и структуры:**
- `Config` — хранит все настройки приложения: DatabaseURL, ServerAddr, PoolMaxConns

**Функции:**
- `func Load() (*Config, error)` — читает `DATABASE_URL` и опциональные параметры из env; если `DATABASE_URL` пустой — возвращает ошибку; возвращает заполненный `*Config`

**Связи:** `main.go → config/config.go`

---

### `repository/user.go`

**За что отвечает:** весь SQL для таблицы users — единственное место где есть pgx-вызовы.

**Типы и структуры:**
- `User` — доменная структура: `ID int64`, `Name string`, `Email string`, `CreatedAt time.Time`
- `UserRepository` — интерфейс с методами CRUD; handler зависит от него, не от конкретного типа
- `pgUserRepository` — приватная структура, реализует `UserRepository`, хранит `*pgxpool.Pool`
- `var ErrNotFound = errors.New("user not found")` — sentinel error для 404

**Функции:**
- `func NewUserRepository(pool *pgxpool.Pool) UserRepository` — конструктор; принимает пул, возвращает интерфейс
- `func (r *pgUserRepository) Create(ctx context.Context, name, email string) (*User, error)` — INSERT с RETURNING id, created_at; при unique violation возвращает `ErrDuplicateEmail`
- `func (r *pgUserRepository) GetByID(ctx context.Context, id int64) (*User, error)` — SELECT по id; при `pgx.ErrNoRows` возвращает `ErrNotFound`
- `func (r *pgUserRepository) List(ctx context.Context) ([]*User, error)` — SELECT всех; при пустой таблице возвращает пустой slice (не nil)
- `func (r *pgUserRepository) Update(ctx context.Context, id int64, name, email string) (*User, error)` — UPDATE с RETURNING; при `pgx.ErrNoRows` возвращает `ErrNotFound`
- `func (r *pgUserRepository) Delete(ctx context.Context, id int64) error` — DELETE; проверяет `RowsAffected() == 0` → возвращает `ErrNotFound`

**Связи:** `handler/user.go → repository/user.go (через интерфейс)`; `main.go → repository/user.go (конструктор)`

---

### `handler/user.go`

**За что отвечает:** HTTP — декодирование запроса, вызов repository, кодирование ответа, маппинг доменных ошибок в HTTP статусы.

**Типы и структуры:**
- `UserHandler` — хранит `repo repository.UserRepository`
- `createUserRequest` — JSON тело для POST: `Name string`, `Email string`
- `updateUserRequest` — JSON тело для PUT: `Name string`, `Email string`

**Функции:**
- `func NewUserHandler(repo repository.UserRepository) *UserHandler` — конструктор
- `func (h *UserHandler) Create(w http.ResponseWriter, r *http.Request)` — декодирует тело, вызывает `repo.Create`, при `ErrDuplicateEmail` → 409, при успехе → 201 + JSON
- `func (h *UserHandler) GetByID(w http.ResponseWriter, r *http.Request)` — берёт id из `r.PathValue("id")`, парсит в int64, вызывает `repo.GetByID`; при `ErrNotFound` → 404
- `func (h *UserHandler) List(w http.ResponseWriter, r *http.Request)` — вызывает `repo.List`, пишет JSON; nil slice кодирует как `[]`
- `func (h *UserHandler) Update(w http.ResponseWriter, r *http.Request)` — декодирует тело, берёт id из path, вызывает `repo.Update`
- `func (h *UserHandler) Delete(w http.ResponseWriter, r *http.Request)` — берёт id, вызывает `repo.Delete`, при успехе → 204 без тела
- `func writeJSON(w http.ResponseWriter, status int, v any)` — хелпер: устанавливает Content-Type, пишет статус, кодирует v в JSON
- `func writeError(w http.ResponseWriter, status int, msg string)` — хелпер: пишет `{"error":"msg"}` с нужным статусом

**Связи:** `main.go → handler/user.go`; `handler/user.go → repository (интерфейс)`

---

### `main.go`

**За что отвечает:** инициализация всех зависимостей в правильном порядке и graceful shutdown.

**Функции:**
- `func main()` — последовательно: `config.Load()` → `pgxpool.New()` → запуск миграций → `repository.NewUserRepository()` → `handler.NewUserHandler()` → регистрация маршрутов → `signal.NotifyContext` → `srv.ListenAndServe()` в горутине → ожидание сигнала → `srv.Shutdown()` → `pool.Close()`

**Связи:** импортирует все пакеты проекта; точка входа

---

### `migrations/000001_create_users.up.sql`

```sql
CREATE TABLE IF NOT EXISTS users (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    email      TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

### `migrations/000001_create_users.down.sql`

```sql
DROP TABLE IF EXISTS users;
```

---

## Подсказки по архитектуре

**Детектирование unique constraint violation:**
```go
var pgErr *pgconn.PgError
if errors.As(err, &pgErr) && pgErr.Code == "23505" {
    return ErrDuplicateEmail
}
```

**Пустой список вместо null в JSON:**
```go
users := make([]*User, 0) // не var users []*User
```

**Порядок shutdown в main.go важен:**
```
srv.Shutdown(ctx) → pool.Close()
```
Сначала HTTP, потом закрываем пул — иначе in-flight запросы получат ошибку соединения.

**Запуск миграций:**
```go
m, err := migrate.New("file://migrations", cfg.DatabaseURL)
if err != nil { log.Fatal(err) }
if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
    log.Fatal(err)
}
```

---

## Definition of Done

1. Все Acceptance Criteria проверены curl-командами из раздела выше
2. Код загружен на GitHub в репозиторий `pg-crud`
3. README.md в репозитории
4. Можешь объяснить: зачем pool вместо одного соединения, как работает `defer tx.Rollback`, почему `errors.Is(err, pgx.ErrNoRows)` а не сравнение строк, что такое Code `"23505"`

---

## Следующий шаг после сдачи

Task #8 — gRPC Service: protobuf, grpc-go, unary + server-streaming, interceptors.
