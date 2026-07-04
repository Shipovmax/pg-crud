# Task #9 — Redis Cache Layer (pg-crud)

## Цель

Добавить cache-aside слой на Redis поверх `pg-crud`, не трогая `pgUserRepository` и не протекая абстракцией в `handler`. Учебная цель — не "прикрутить редис", а разобраться с реальными проблемами кэширования: **cache stampede**, **инвалидация после записи**, **fail-open деградация** и **circuit breaker** на внешнюю зависимость, которая может лечь под нагрузкой. Задача блокируется незакрытым техдолгом — `List()` без пагинации не имеет права на кэш, пока не ограничена по объёму.

---

## Acceptance Criteria

- [ ] `List()` переведён на `LIMIT`/`OFFSET` (или keyset-пагинацию по `id`) — **прод-блокер**, без этого пункт про кэш List не имеет смысла
- [ ] `GetByID` при cache hit не делает ни одного запроса к Postgres — проверяется тестом с мок-репозиторием, считающим вызовы
- [ ] `GetByID` при cache miss читает из Postgres и прогревает кэш перед возвратом ответа клиенту
- [ ] N конкурентных `GetByID` с одинаковым `id` при холодном кэше → **ровно один** запрос в Postgres (тест на `singleflight`, `N >= 50` горутин)
- [ ] `Update`/`Delete` инвалидируют кэш **после** успешного коммита в Postgres, не раньше
- [ ] Обрыв соединения с Redis не роняет `GetByID` — запрос уходит в Postgres, статус ответа не меняется (тест с недоступным адресом Redis)
- [ ] Circuit Breaker на Redis-операциях: после N подряд ошибок переходит в `Open`, следующие вызовы **не ждут таймаута Redis**, идут прямиком в Postgres
- [ ] TTL кэша содержит джиттер ±10% — нет одинакового TTL у двух подряд записанных ключей (тест на разброс значений)
- [ ] Экспортированы метрики `cache_hits_total`, `cache_misses_total`, `cache_errors_total`, `cache_breaker_state` на `/metrics`
- [ ] `go vet ./...` и `go test -race ./...` проходят чисто
- [ ] Ни одной утечки горутин — фоновая запись в кэш ограничена собственным таймаутом, не привязана к отменяемому `request context`

---

## Технические требования

### Обязательно

| Требование | Детали |
|---|---|
| `go-redis/v9` | клиент с явными `DialTimeout`/`ReadTimeout`/`WriteTimeout`, пул через `PoolSize`/`MinIdleConns` |
| `golang.org/x/sync/singleflight` | схлопывание конкурентных промахов по одному ключу в один поход в Postgres |
| Circuit Breaker (`sony/gobreaker` или самописный) | оборачивает **все** Redis-операции; порог и cooldown задаются конфигом, не хардкодятся |
| Раздельные `context.WithTimeout` | Redis-таймаут (мс) строго меньше и независим от таймаута Postgres-запроса |
| Jittered TTL | `ttl ± 10%`, иначе массово прогретые ключи одновременно протухают и бьют по БД разом |
| Fail-open | ошибка/таймаут Redis — не ошибка запроса, только лог + метрика, деградация на Postgres |
| Prometheus-метрики | `cache_hits_total`, `cache_misses_total`, `cache_errors_total`, `cache_breaker_state` (0/1/2 = closed/half-open/open) |
| `LIMIT`/`OFFSET` в `List()` | без этого — Seq Scan на росте таблицы и невозможность нормально кэшировать список |

### Запрещено

- Кэшировать `List()` без ограничения выборки
- Блокирующая запись в Redis **на пути ответа** клиенту — `Set` только в фоне с собственным bounded-таймаутом
- Redis как источник истины — TTL обязателен и конечен, `0`/бессрочные ключи запрещены
- Молчаливое поглощение ошибок Redis без лога и без инкремента `cache_errors_total`
- Хардкод адреса Redis, пула, порога Circuit Breaker в коде — всё через `config.Load()`
- Инвалидация кэша **до** коммита в Postgres (порядок операций фиксирован: сначала БД, потом `DEL`)

---

## Темы, которые ты прокачиваешь

- **Cache-aside pattern** — приложение само решает, когда читать/писать кэш; в отличие от write-through, БД не знает о существовании Redis вообще. Проще в реализации, но окно рассинхрона между БД и кэшем — твоя ответственность.

- **Cache stampede (thundering herd)** — при протухании горячего ключа сотни конкурентных запросов промахиваются одновременно и долбят БД разом. `singleflight` схлопывает их в один поход, остальные ждут результат первого.

- **Circuit Breaker (Closed / Open / Half-Open)** — при деградации внешней зависимости важно не ждать таймаут на каждый запрос, а быстро фейлиться после порога ошибок (`Open`), периодически пробуя восстановление (`Half-Open`). Без этого зависший Redis добавляет фиксированную задержку **к каждому** запросу вместо того, чтобы отвалиться разом.

- **TTL jitter** — если 10 000 ключей прогреты в одну секунду с одинаковым TTL, они протухнут в одну секунду. Джиттер размазывает инвалидацию по времени.

- **Fail-open vs fail-closed** — для read-кэша фейл-открытая деградация (идём в БД) корректна. Для платежей или лимитов был бы нужен fail-closed (отказ безопаснее, чем неверный ответ). Обязан уметь объяснить, почему здесь выбор именно fail-open.

- **Порядок «запись → инвалидация»** — если инвалидировать кэш до коммита в БД и упасть между операциями, старое значение попадёт обратно в кэш при следующем чтении до того, как транзакция вообще завершилась. Порядок фиксированный, не перепутать.

---

## Структура файлов

```
pg-crud/
├── main.go
├── config/
│   └── config.go              # + RedisAddr, RedisPoolSize, CacheTTL, BreakerThreshold
├── handler/
│   ├── user.go                # без изменений
│   └── user_test.go
├── repository/
│   ├── user.go                 # без изменений, List() переведён на LIMIT/OFFSET
│   ├── redis_client.go         # NEW: конструктор *redis.Client
│   ├── user_cache.go           # NEW: cachedUserRepository — декоратор UserRepository
│   └── user_cache_test.go      # NEW: singleflight, fail-open, breaker, jitter
├── metrics/
│   └── cache.go                # NEW: Prometheus-коллекторы
├── migrations/
│   ├── 000001_create_users.up.sql
│   └── 000001_create_users.down.sql
├── docker-compose.yml          # + redis service
├── go.mod
├── README.md
└── task.md
```

---

## Разбивка по файлам

### `repository/user.go` (изменения)

**За что отвечает:** persistence-слой, как раньше, плюс пагинация.

**Изменения:**
- `List(ctx context.Context, limit, offset int) ([]*User, error)` — сигнатура меняется, `ORDER BY id LIMIT $1 OFFSET $2`
- Интерфейс `UserRepository.List` обновляется синхронно, `handler/user.go` парсит `?limit=&offset=` из query-параметров с дефолтами и верхним потолком (`limit > 100` → `400`)

**Связи:** `cachedUserRepository.List` проксирует напрямую, без кэша

---

### `repository/redis_client.go`

**За что отвечает:** конструктор клиента с явными таймаутами, чтобы зависший Redis не подвешивал горутины хендлеров.

**Функции:**
- `func NewRedisClient(ctx context.Context, cfg RedisConfig) (*redis.Client, error)` — `redis.NewClient` с `DialTimeout=2s`, `ReadTimeout=500ms`, `WriteTimeout=500ms`, `PoolSize`/`MinIdleConns` из конфига, `Ping` при старте с отдельным таймаутом

**Связи:** `main.go → repository.NewRedisClient(ctx, cfg)`

---

### `repository/user_cache.go`

**За что отвечает:** cache-aside декоратор над `UserRepository`, единственное место, знающее о Redis.

**Типы:**
- `type cachedUserRepository struct { next UserRepository; redis *redis.Client; breaker *gobreaker.CircuitBreaker; ttl time.Duration; sf singleflight.Group; metrics *metrics.CacheMetrics }`

**Функции:**
- `func NewCachedUserRepository(next UserRepository, rdb *redis.Client, ttl time.Duration, m *metrics.CacheMetrics) UserRepository`
- `func (r *cachedUserRepository) GetByID(ctx context.Context, id int64) (*User, error)` — Redis GET через breaker → hit/miss/error по метрикам → `sf.Do` на miss → фоновый `Set` с джиттером
- `func (r *cachedUserRepository) Create/Update/Delete(...)` — проксируют в `next`, `Update`/`Delete` вызывают `invalidate` после успешного ответа `next`
- `func (r *cachedUserRepository) invalidate(ctx context.Context, id int64)` — `DEL` через breaker, ошибка только логируется
- `func (r *cachedUserRepository) jitteredTTL() time.Duration` — `ttl ± 10%` через `math/rand`

**Связи:** `main.go` оборачивает `pgUserRepository` этим декоратором перед передачей в `handler.NewUserHandler`

---

### `metrics/cache.go`

**За что отвечает:** Prometheus-коллекторы для кэш-слоя, регистрируются один раз при старте.

**Типы:**
- `type CacheMetrics struct { Hits, Misses, Errors prometheus.Counter; BreakerState prometheus.Gauge }`

**Функции:**
- `func NewCacheMetrics(reg prometheus.Registerer) *CacheMetrics` — регистрирует все коллекторы, паникует при дублирующей регистрации (fail-fast на старте, не в рантайме)

**Связи:** передаётся в `repository.NewCachedUserRepository`, обновляется на каждый hit/miss/error и на смену состояния breaker

---

### `main.go` (изменения)

**Функции:**
- `func main()` — после `pgxpool.NewWithConfig`: `repository.NewRedisClient` → `metrics.NewCacheMetrics` → `gobreaker.NewCircuitBreaker` с порогом из конфига → `repository.NewCachedUserRepository(pgRepo, redisClient, cfg.CacheTTL, cacheMetrics)` → `handler.NewUserHandler(cachedRepo)` → добавить `mux.Handle("/metrics", promhttp.Handler())`

**Связи:** точка сборки всех зависимостей, порядок инициализации фиксирован — Redis поднимается после Postgres, до создания хендлеров

---

## Подсказки по архитектуре

**Circuit Breaker вокруг Redis-вызова:**
```go
func (r *cachedUserRepository) getFromCache(ctx context.Context, key string) ([]byte, error) {
    v, err := r.breaker.Execute(func() (interface{}, error) {
        cacheCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
        defer cancel()
        return r.redis.Get(cacheCtx, key).Bytes()
    })
    if err != nil {
        return nil, err // сюда прилетает и redis.Nil, и gobreaker.ErrOpenState — разбирать в вызывающем коде
    }
    return v.([]byte), nil
}
```
`gobreaker.ErrOpenState` — отдельная ветка от `redis.Nil`: **Open** значит "даже не пытались стучаться", это не промах кэша, это отказ от похода в Redis вообще.

**Инвалидация строго после коммита:**
```go
func (r *cachedUserRepository) Update(ctx context.Context, id int64, name, email string) (*User, error) {
    u, err := r.next.Update(ctx, id, name, email) // сначала источник истины
    if err != nil {
        return nil, err
    }
    r.invalidate(ctx, id) // потом кэш; если invalidate упадёт — залогируется, TTL сам почистит
    return u, nil
}
```

**Фоновая запись без утечки горутин:**
```go
func (r *cachedUserRepository) setAsync(u *User) {
    go func() {
        ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
        defer cancel()
        // ctx не привязан к отменяемому request context — иначе отмена
        // клиентом запроса оборвёт прогрев кэша на середине
        payload, _ := json.Marshal(u)
        _, _ = r.breaker.Execute(func() (interface{}, error) {
            return nil, r.redis.Set(ctx, userCacheKey(u.ID), payload, r.jitteredTTL()).Err()
        })
    }()
}
```
Горутина не утечёт: у неё собственный ограниченный по времени контекст (300 мс максимум), она не может зависнуть навечно.

---

## Definition of Done

1. Все Acceptance Criteria закрыты тестами, `go test -race ./...` зелёный
2. Нагрузочный прогон (`hey`/`wrk`, N=1000 конкурентных запросов на один и тот же `id` с холодным кэшем) показывает **один** запрос в Postgres в логах — доказывает работу `singleflight`
3. Искусственно уронить Redis (`docker stop redis`) во время нагрузки — сервис продолжает отвечать `200`, `cache_errors_total` растёт, breaker переходит в `Open`, задержка ответа не деградирует до таймаута Redis на каждый запрос
4. Можешь объяснить: почему инвалидация идёт после коммита, а не до; чем `singleflight` отличается от простого мьютекса на ключ; почему `Open` state брейкера — это не то же самое, что `redis.Nil`; зачем джиттер на TTL, если можно просто уменьшить TTL

---

## Следующий шаг после сдачи

Task #10 — Rate Limiting на уровне `handler` (token bucket поверх `net/http`, по IP или по API-ключу). Кэш защищает от повторных чтений существующих `id`, но не от потока запросов на **несуществующие** `id` — они всегда промахиваются мимо кэша и бьют напрямую в Postgres, кэш тут бессилен в принципе.