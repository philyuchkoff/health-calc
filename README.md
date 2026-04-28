# Health score calculator

## Калькулятор "здоровья" сервиса или продукта

![](./img/health-calc.png)

## Сервис, который:

1. раз в 5 минут читает конфиг из Git (`health-config.yaml` с весами и правилами).

2. через Prometheus API запрашивает сырые метрики по каждому правилу.

3. Вычисляет общий Health Score как взвешенную сумму `(weight_synthetic * uptime_synthetic) + (weight_allure * test_pass_rate) + ....`

4. экспортирует результат как gauge `platform_health_score` в формате Prometheus.

5. использует in-memory кэш (значения метрик хранятся в metricValues map) на случай недоступности Prometheus.

6. в случае недоступности источника метрик повторяет запросы три раза (ретрай с экспоненциальным бэк-оффом) и на четвертый отправляет алерт в Telegram.

7. поддержка переменных окружения в конфиге

8. наблюдаемость: Метрика Prometheus + логирование

  

## Подробнее:

### 1. Структуры данных:

-  `Config` - загружает YAML конфиг с весами метрик и настройками

-  `PrometheusResponse` - для парсинга JSON ответов от Prometheus API

  

### 2. Prometheus метрики:

-  `platform_health_score` - основная метрика здоровья

-  `health_calculator_metrics_fetched_total` - счетчик успешных запросов

-  `health_calculator_metrics_failed_total` - счетчик неудачных запросов

-  `health_calculator_calculation_duration_seconds` - гистограмма времени расчетов

  

### 3. Основные алгоритмы:

- ретраи: 3 попытки с exponential backoff (1s, 2s, 3s)

- нормализация: Приведение всех метрик к диапазону 0-1

- взвешенная сумма: `totalScore += normalizedValue * metric.Weight`

- пропорциональная корректировка: если часть метрик недоступна

- валидация: проверка суммы весов = 1.0

  

### 4. Graceful shutdown:

- обработка `SIGINT`/`SIGTERM` сигналов

- отмена контекста для остановки горутин

- 10-секундный timeout для завершения HTTP соединений

  

### 5. Circuit Breaker:

Защищает сервис от каскадных сбоев при недоступности Prometheus:

- **Состояния:** Closed (норма), Open (блокирует запросы), Half-Open (проверяет восстановление)
- **Настройки:** max_failures=3, reset_timeout=30s (настраивается в config)
- **Поведение при открытии:** возвращает fallback значение 0.5 для метрик
- **Мониторинг:** метрика `health_calculator_circuit_breaker_tripped_total`
- **Endpoint для мониторинга:** `/circuit-breaker` - показывает текущее состояние

### 6. Health checks:

- проверяет что расчеты выполняются регулярно (<10 минут)

- возвращает JSON с деталями статуса

  

### 7. Безопасность:

- таймауты на HTTP запросы

- ReadOnly монтирование конфига

- обработка ошибок на всех уровнях

  
 #### Сервис будет доступен на порту 8080 с endpoints:

-   `/metrics` - Prometheus метрики
    
-   `/health` - health check для Kubernetes
    
-   `/circuit-breaker` - статус circuit breaker
    
-   `/` - простой status page

## надо добавить:
- [x] graceful shutdown
- [x] метрики для мониторинга самого сервиса
- [x] хелсчек с бизнес-логикой
- [x] circuit breaker
- [ ] structured logging
- [ ] graceful degradation
- [ ] rate limiting
- [ ] улучшить health checks
- [ ] деплоймент
- [ ] алертинг рулз