
Объяснение ключевых компонентов:

### 1. Структуры данных:
- `Config` - загружает YAML конфиг с весами метрик и настройками
- `PrometheusResponse` - для парсинга JSON ответов от Prometheus API

### 2. Prometheus метрики:
 - `platform_health_score` - основная метрика здоровья
 - `health_calculator_metrics_fetched_total` - счетчик успешных запросов
 - `health_calculator_metrics_failed_total` - счетчик неудачных запросов
 - `health_calculator_calculation_duration_seconds` - гистограмма времени расчетов

### 3. Основные алгоритмы:
- ретраи: 3 попытки с exponential backoff (1s, 2s, 3s)
- нормализация: Приведение всех метрик к диапазону 0-1
- взвешенная сумма: `totalScore += normalizedValue * metric.Weight`
- пропорциональная корректировка: если часть метрик недоступна

### 4. Graceful shutdown:
- обработка `SIGINT`/`SIGTERM` сигналов
- отмена контекста для остановки горутин
- 10-секундный timeout для завершения HTTP соединений

### 5. Health checks:
 - проверяет что расчеты выполняются регулярно (<10 минут)
 - возвращает JSON с деталями статуса

### 6. Безопасность:
 - таймауты на HTTP запросы 
 - ReadOnly монтирование конфига 
 - обработка ошибок на всех уровнях
