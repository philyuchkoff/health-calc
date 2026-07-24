# Fixes Summary

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
