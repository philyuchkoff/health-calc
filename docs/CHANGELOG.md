> [← Back to README](https://github.com/philyuchkoff/health-calc) · [Documentation](https://philyuchkoff.github.io/health-calc/) · [Русская версия](https://github.com/philyuchkoff/health-calc/blob/main/README-ru.md)

# Changelog

All notable changes to this project are documented here. The format is loosely based on [Keep a Changelog](https://keepachangelog.com/).

## Critical

### 1. Deadlock in `calculateHealthScore`

**Проблема:** `calculateHealthScore()` захватывает `hc.mutex.Lock()`, затем вызывает `cleanupExpiredCache()`, `getCachedValue()`, `cacheValue()`, `getFallbackValue()` — каждая повторно пытается захватить тот же mutex. Go `sync.Mutex` не-reentrant → сервис зависает на первом же тике.

**Почему:** Рефакторинг добавил вызовы helper-функций внутрь locked-секции, не учтя, что они сами захватывают mutex.

**Фикс:** Разделил функции на public (с Lock/Unlock) и `*Locked` variants (без захвата, caller обязан держать lock). `calculateHealthScore()` вызывает `*Locked` variants напрямую.

### 2. Rate limiter refill rate обнуляется

**Проблема:** `refillRate = int(float64(requests) / period.Seconds())`. Для `"10/m"`: `int(10/60) = 0`. После расхода всех токенов bucket никогда не пополняется → rate limit навсегда заблокирован.

**Почему:** Целочисленное деление отбрасывает дробную часть.

**Фикс:** `tokens` и `refillRate` — `float64`. Пополнение с миллисекундной точностью: `float64(elapsed.Milliseconds()) * refillRate / 1000`.

### 3. Race conditions при чтении `hc.config`

**Проблема:** `queryPrometheus()` читает `hc.config.Prometheus.URL/Timeout`, `sendAlert()` читает `hc.config.Alerting.Telegram.*` без мьютекса. `config` может быть перезаписан из `watchConfig` в параллельной горутине.

**Почему:** Функции использовали прямой доступ к полям структуры вместо передачи параметров.

**Фикс:** Все необходимые значения конфига (promURL, timeout, alertThreshold, botToken, chatID) извлекаются под блокировкой и передаются как параметры. `queryPrometheus()` и `sendAlert()` теперь не зависят от `hc.config`.

### 4. `normalizeValue` возвращает сырое значение

**Проблема:** При `value > metric.MaxValue` функция возвращает `metric.MaxValue` (сырое, например 0.95), а не нормализованное `1.0`.

**Почему:** Ошибка в логике — возвращалось граничное значение, а не результат нормализации.

**Фикс:** Возвращаем `1.0`.

## High

### 5. HTTP-сервер без `ReadHeaderTimeout`

**Проблема:** Нет таймаута на чтение заголовков — уязвимость к slow-loris атаке.

**Почему:** Параметр не был задан при создании `http.Server`.

**Фикс:** Добавлен `ReadHeaderTimeout: 10 * time.Second`.

### 6. CI без race detection и линтера

**Проблема:** `go test` без `-race` не обнаруживает data races. `golangci-lint` не запускается, хотя `.golangci.yml` существует. Docker-образ не собирается в CI.

**Почему:** Workflow не включал эти шаги.

**Фикс:** Добавлены `-race`, `golangci/golangci-lint-action@v6`, `docker build`.

### 7. Нет HTTP RED метрик

**Проблема:** Невозможно построить RED-дашборды (Rate/Errors/Duration) для HTTP.

**Почему:** Метрики не регистрировались и не записывались.

**Фикс:** Добавлены `http_requests_total` (counter по method, path, status) и `http_request_duration_seconds` (histogram по method, path). Middleware `httpMetricsMiddleware` оборачивает все routes.

## Medium

### 8. YAML code fence сломан в `rate-limiting.md`

**Проблема:** ` ```yamlper_ip_rate:` — fence и содержимое слиты, Markdown-рендерер не распознаёт блок.

**Почему:** Пропущен перенос строки после ` ```yaml`.

**Фикс:** Разделил на две строки. Исправлено в EN и RU версиях.

### 9. `cp health-config.yaml health-config.yaml` — no-op

**Проблема:** Команда копирует файл сам в себя, бесполезна.

**Почему:** Опечатка в документации.

**Фикс:** Заменено на `cp ... .bak` с инструкцией по редактированию.

### 10. Устаревшие SRE аудиты

**Проблема:** SRE-2.md (27.04) и SRE-3.md (29.04) жалуются на отсутствие circuit breaker, Dockerfile, CI, `/ready` — всё уже реализовано. Вводят в заблуждение.

**Фикс:** Файлы удалены. Уже были в `.gitignore`.

### 11. Hardcoded путь к конфигу

**Проблема:** Путь `"health-config.yaml"` жёстко задан в коде.

**Фикс:** Добавлена функция `configPath()`, читающая `CONFIG_PATH` env var (fallback `"health-config.yaml"`). Документация обновлена.

### 12. Нет `docker-compose.yml`

**Проблема:** docker-compose фрагмент был только в документации, без реального файла.

**Фикс:** Создан `docker-compose.yml` с VictoriaMetrics, Grafana, health-calculator. Документация ссылается на файл.

### 13. Нет Kubernetes манифестов

**Проблема:** Deployment/Service описаны inline в документации, нет готовых `.yaml` файлов.

**Фикс:** Созданы `k8s/deployment.yaml`, `k8s/service.yaml`, `k8s/configmap.yaml`. Документация ссылается на них.

### 14. Нет Makefile

**Проблема:** Часто используемые команды (build, test, lint, docker) не автоматизированы.

**Фикс:** Добавлен `Makefile` с 9 target'ами.

### 15. Нет `.env.example`

**Проблема:** Нет шаблона переменных окружения.

**Фикс:** Создан `.env.example` с TELEGRAM_BOT_TOKEN, TELEGRAM_CHAT_ID, CONFIG_PATH.

### 16. `.DS_Store` отслеживается в git

**Проблема:** Файл был закоммичен до добавления в `.gitignore`, остался в индексе.

**Фикс:** `git rm --cached .DS_Store`.

### 17. Бинарник называется по-разному

**Проблема:** README и examples используют `health-calc`, Dockerfile/docs/Makefile — `health-calculator`.

**Фикс:** Везде `health-calculator`.

### 18. Нет LICENSE

**Проблема:** Репозиторий без лицензии.

**Фикс:** Добавлен MIT `LICENSE`, бейджи в README.

### 19. `health-config.yaml` не совпадает с документацией

**Проблема:** Секции в другом порядке, нет `logging`. Документация упоминает `output`/`output_file`, которых нет в реальном файле.

**Фикс:** Переупорядочены секции, добавлен `logging`. Убран `toolchain` из `go.mod`.

### 20. ConfigReloadFailing алерт сломан

**Проблема:** `rate(health_calculator_config_reload_total[5m]) < 1` срабатывает даже при 0 reloads (0 rate).

**Фикс:** Исправлен в документации для сравнения с ошибками, а не с общим числом перезагрузок.

---

## SRE Audit 2026-07-24

### S1. SRE-*.md и SRE.md не игнорировались по единой маске

**Проблема:** `.gitignore` содержал две отдельные строки `SRE-*.md` и `SRE.md`. Новые аудиты с другими именами (например `SRE-foo.md`) попадали бы в индекс.

**Фикс:** Заменено на единую маску `SRE*.md`.

### 21. `/metrics` под rate limit — Prometheus self-throttled

**Проблема:** Конфигурация `global_rate: { "/metrics": "100/m" }` ограничивала собственные scrape-запросы Prometheus. При стандартном scrape_interval=15s получалось 4 запроса/мин — укладывается, но при scrape_interval=5s (12/мин) уже на грани. Любое сокращение интервала или дополнительный scrape job ломал сбор метрик.

**Почему:** `IsAllowed()` применяет rate limit ко всем endpoints одинаково, не различая операторский трафик (Prometheus) и клиентский.

**Фикс:** В `ratelimit.go` добавлен ранний return для `endpoint == "/metrics"` до проверки per-IP/global лимитов. Удалена запись `/metrics` из `health-config.yaml` и примеров в `docs/03-configuration.md`, `docs/rate-limiting.md` (EN+RU).

### 22. Docker-контейнер без healthcheck

**Проблема:** `Dockerfile` не содержал `HEALTHCHECK`. `docker-compose.yml` тоже не имел `healthcheck` для сервиса. Оркестратор не мог отличить живой контейнер от зависшего, Grafana стартовала раньше health-calculator.

**Фикс:** Добавлен `HEALTHCHECK` в Dockerfile с wget на `/ready`. Добавлен `healthcheck` в docker-compose.yml с `start_period: 30s` (учёт первого scrape Prometheus).

### 23. Контейнер работает под root

**Проблема:** Dockerfile не содержал `USER` директивы. Компрометация приложения = компрометация ноды.

**Фикс:** Создан system user `app` (UID/GID 10001). `WORKDIR /root/` chown'нут на app. `USER app` перед `CMD`.

### 24. Плавающий тег `alpine:latest`

**Проблема:** `FROM alpine:latest` в Dockerfile не воспроизводим. Любой минорный релиз alpine может сломать сборку или runtime.

**Фикс:** `alpine:3.21` (LTS). `golang:1.23-alpine3.21` для builder'а.

### 25. K8s без securityContext

**Проблема:** `k8s/deployment.yaml` не имел `securityContext`. Контейнер работал под root, без ограничений capabilities.

**Фикс:** Добавлен pod-level `securityContext` (runAsNonRoot: true, runAsUser/Group: 10001, fsGroup: 10001) и container-level (readOnlyRootFilesystem, allowPrivilegeEscalation: false, drop ALL capabilities). Добавлен `emptyDir /tmp` для Go runtime GC.

### 26. K8s без PodDisruptionBudget

**Проблема:** `replicas: 2` без PDB. При drain ноды оба пода могли упасть одновременно.

**Фикс:** Создан `k8s/pdb.yaml` с `minAvailable: 2`. Создан `k8s/networkpolicy.yaml` с default-deny ingress/egress и whitelist для Prometheus, Telegram HTTPS, DNS.

### 27. K8s без topology spread

**Проблема:** Оба реплики могли попасть на одну ноду. При падении ноды весь сервис падает.

**Фикс:** Добавлен `podAntiAffinity` (preferred) и `topologySpreadConstraints` (maxSkew: 1). `replicas: 2` → `replicas: 3` для кворума с PDB.

### 28. Image с тегом `:latest`

**Проблема:** `image: your-registry/health-calculator:latest` не позволяет откатиться, аудит слепнет.

**Фикс:** `ghcr.io/philyuchkoff/health-calculator:v0.1.0` + `imagePullPolicy: IfNotPresent`.

### 29. `X-RateLimit-Reset` всегда +1 минута

**Проблема:** Header всегда `time.Now()+1min`, лгал клиентам о реальном времени восстановления.

**Фикс:** Введён `RateLimitDecision` struct с полем `ResetAfter`. `Bucket.AllowNext` рассчитывает реальное время до следующего токена: `(1 - tokens) / refillRate`. `IsAllowed` прокидывает решение через middleware. API breaking change — тесты обновлены.

### 30. HTTP label cardinality explosion в метриках

**Проблема:** `path := r.URL.Path` как label. Любой уникальный URL создаёт новую time-series. 1000 уникальных 404 = 1000 series только для одного метода.

**Фикс:** Whitelist известных endpoints (`/metrics`, `/health`, `/ready`, `/circuit-breaker`, `/`). Неизвестные пути bucketed в `/other`.

### 31. golangci-lint: errcheck

**Проблема:** `json.NewEncoder(w).Encode(...)` и `w.Write([]byte(...))` не проверяют возвращаемые ошибки. errcheck падает в CI.

**Фикс:** `_ = json.NewEncoder(...)` и `_, _ = w.Write(...)` для HTTP responses (после `WriteHeader` уже поздно сообщать клиенту). `_ = cb.Execute(...)` в тестах.

### 32. golangci-lint: gofmt

**Проблема:** Struct alignment drift в нескольких файлах после рефакторингов. gofmt ругается на `calc.go:117`, `circuitbreaker.go:19`, `logging.go:235`, `ratelimit.go:24`, `circuitbreaker_test.go:114`, `graceful_degradation_test.go:53`.

**Фикс:** `gofmt -w *.go` — канонический формат.

### 33. golangci-lint: SA1029

**Проблема:** `context.WithValue(ctx, "request_id", ...)` использует `string` как ключ — может конфликтовать с ключами из других библиотек. staticcheck SA1029.

**Фикс:** Определён unexported type `contextKey string` и константы `requestIDKey`, `traceIDKey`, `spanIDKey`. Используется типизированный ключ.

### 34. `prometheus.MustRegister` паникует при дубликатах

**Проблема:** Второй вызов `NewHealthCalculator` (в тестах, fuzzer, hot-reload) → `panic: duplicate metrics collector registration`.

**Фикс:** Helper `registerOrLog(cs ...prometheus.Collector)` использует `prometheus.Register` с обработкой ошибки. `AlreadyRegisteredError` игнорируется, остальные логируются, startup не падает.

### 35. Nested mutex в `CleanupExpiredBuckets` (potential deadlock)

**Проблема:** `CleanupExpiredBuckets` берёт `rl.mutex.Lock()`, затем `bucket.mutex.Lock()` — nested lock. Сейчас безопасно (нет inverse order), но любое расширение (например, логирование из bucket operation) создаст deadlock.

**Фикс:** `lastRefill` (time.Time) заменён на `lastRefillUnix` (atomic.Int64). `CleanupExpiredBuckets` читает timestamp атомарно без bucket.mutex. Устранён nested lock полностью.

### 36. Data race в `setState` callback

**Проблема:** `setState` запускал callback через `go cb.onStateChange(...)`. Тест `TestCircuitBreakerStateChangeCallback` читал переменные после `time.Sleep(10ms)` — race detected.

**Фикс:** Убран `go` — callback синхронный. Гарантирует ordering: после возврата `setState` callback уже выполнен. Блокировка допустима, потому что state changes редки.

### 37. Нет лимита на размер ответа от Prometheus

**Проблема:** `io.ReadAll(resp.Body)` без лимита. Compromised/misconfigured Prometheus может прислать гигабайты → memory exhaustion DoS.

**Фикс:** `io.LimitReader(resp.Body, 10*1024*1024)` — 10 MB максимум. Prometheus API обычно отвечает десятками KB.

### 38. Telegram и Prometheus делили один http.Client

**Проблема:** Один клиент с timeout 30s для всех исходящих. Медленный Telegram API мог исчерпать connection pool, и алерт с задержкой 30s неприемлем во время инцидента.

**Фикс:** Добавлен `httpClientTelegram` с timeout 5s. Prometheus queries сохраняют 30s для медленных range queries.

### 39. Hardcoded `unhealthy_threshold`

**Проблема:** `/health` и `/ready` handlers содержали `lastUpdate > 10*time.Minute` хардкод. Оператор не мог настроить staleness detection под специфику деплоя.

**Фикс:** Добавлено поле `unhealthy_threshold` (yaml string, default 10m) в Config. В handlers снимок под RLock для защиты от race.

### 40. [C1] `calculateHealthScore` держит write-lock всё время HTTP-запросов

**Проблема:** `hc.mutex.Lock()` держался на всю длительность опроса Prometheus (до 30s × 4 метрики = 120s). HTTP handlers (`/health`, `/ready`, `/metrics`, `/circuit-breaker`) зависали на RLock. K8s liveness probe (periodSeconds=10, timeout=1s) таймаутил → pod restart → следующий расчёт опять под lock → CrashLoopBackOff каскад.

**Почему:** Изначальный дизайн защищал consistency под единым lock, не учёл что HTTP handlers используют тот же mutex. Это был Critical риск: при первом же медленном Prometheus (что нормально для больших queries) сервис уходил в boot loop.

**Фикс:** Рефакторинг в 3 фазы:
- **Phase 1 (write-lock, микросекунды):** cleanupExpiredCacheLocked + snapshot config + cached values в неизменяемый `calcSnapshot` struct
- **Phase 2 (lock-free):** collectMetricValues — HTTP запросы к Prometheus, может занимать секунды, handlers работают без блокировки
- **Phase 3 (write-lock, микросекунды):** applyResultsLocked пишет новые cache values, gauge, lastSuccessfulCalculation

Lock window сократился с минут до <1ms на фазу. Старый `getFallbackValueLocked` заменён на `fallbackValueFor(snapshot, metric)` — работает с snapshot, без lock.

## Refactoring

### 41. `calc.go` разбит на модули по ответственности

**Проблема:** `calc.go` вырос до 1252 строк и совмещал конфигурацию, HTTP-клиента, Prometheus-запросы, алерты, handlers, middleware и основной цикл — god object, который тяжело читать и тестировать.

**Фикс:** Код разнесён по файлам (общий объём ~1324 строк):
- `main.go` — `configPath()`, `main()`, `buildMux()`, `rootHandler()`
- `config.go` — структуры `Config`/`GracefulDegConfig`, константы `FallbackStrategy`, `loadConfig()`, `parseGracefulDegConfig()`
- `prometheus.go` — `PrometheusResponse`, `queryPrometheus()`, `queryPrometheusWithRetry()`, `sendAlert()`, `normalizeValue()`, `knownEndpoints`/`normalizePath()`
- `handlers.go` — `circuitBreakerHandler`, `healthHandler`, `readyHandler`
- `middleware.go` — `statusRecorder`, `recoveryMiddleware`, `wrapWithRateLimit`, `httpMetricsMiddleware`
- `calc.go` — уменьшен до 624 строк: `HealthCalculator`, цикл расчёта, кэш и graceful degradation

### 42. `HealthCalculator` начал терять статус god object: поля graceful degradation вынесены в `gracefulDegState`

**Проблема:** `HealthCalculator` держит 30 полей пяти разных ответственностей (кэш, метрики, circuit breaker, rate limit, конфиг).

**Фикс (шаг 1):** Поля graceful degradation (`cachedValues`, `degradedMode`, `fallbackUsed`, `maxAgeDuration`, `isDegraded`) сгруппированы в подструктуру `gracefulDegState`. Чтение/запись по-прежнему под `hc.mutex`, поведение не изменилось.

**Фикс (шаг 2):** Все 30 полей сгруппированы в 4 подструктуры по ответственности — главный struct сокращён до 11 полей:
- `calcState` — состояние последнего расчёта (`metricValues`, `lastSuccessfulCalculation`, `prometheusDownCount`)
- `metricsState` — все 12 Prometheus-коллекторов
- `httpClients` — исходящие HTTP-клиенты (`prometheus` 30s, `telegram` 5s)
- `degradation` — graceful degradation (кэш, fallback, флаг degraded)

### 44. Независимые модули вынесены в `internal/` пакеты

**Проблема:** 15 .go файлов в корне main-пакета — ответственности (логирование, circuit breaker, rate limiter) смешаны в одном пакете.

**Фикс:** Код разделён на пакеты:
- `internal/logging` — Logger, LogEntry, Source* константы
- `internal/circuitbreaker` — CircuitBreaker, State*, ErrCircuitBreakerOpen; добавлен метод `Name()`
- `internal/ratelimit` — RateLimiter, RateLimitMiddleware; поля `RateLimitMetrics` экспортированы

В корне осталось 7 .go файлов main-пакета (calc, config, handlers, middleware, prometheus, main + тесты). Зависимости: `ratelimit → logging`. Ни один internal-пакет не зависит от main.

### 43. Документация приведена к структуре репозитория

**Фикс:**
- `PLAN.md` удалён (дублировал CHANGELOG)
- `FIXES.md` переименован в `docs/CHANGELOG.md` (git mv), добавлены ссылки на README и GitHub Pages
- `README-RU.md` → `README-ru.md`, синхронизированы бейджи (Docker, Go Report Card), убран `/metrics` из примеров rate_limit (теперь он явно исключён в коде)
- Все документы (кроме README и LICENSE) перенесены в `docs/`
