package main

import (
	"context"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// Logger структура для структурированного логирования
type Logger struct {
	*logrus.Logger
	serviceName string
}

// LogEntry богатая запись с дополнительными полями
type LogEntry struct {
	*logrus.Entry
}

// LoggingConfig конфигурация логгера
type LoggingConfig struct {
	Level      string `yaml:"level"`       // debug, info, warn, error
	Format     string `yaml:"format"`      // json, text
	Service    string `yaml:"service"`     // имя сервиса
	Async      bool   `yaml:"async"`       // асинхронное логирование
	Output     string `yaml:"output"`      // stdout, stderr, file
	OutputFile string `yaml:"output_file"` // путь к файлу если output=file
}

// ContextFields стандартные поля контекста
type ContextFields struct {
	RequestID string `json:"request_id"`
	Source    string `json:"source"`
	Module    string `json:"module"`
	TraceID   string `json:"trace_id"`
	SpanID    string `json:"span_id"`
}

const (
	SourcePrometheus = "prometheus"
	SourceCalculator = "calculator"
	SourceAlerting   = "alerting"
	SourceHTTP       = "http"
	SourceConfig     = "config"
	SourceRateLimit  = "rate_limit"
)

// NewLogger создает новый структурированный логгер
func NewLogger(config LoggingConfig) *Logger {
	logger := logrus.New()

	// Установка уровня логирования
	level, err := logrus.ParseLevel(config.Level)
	if err != nil {
		level = logrus.InfoLevel
		logger.WithError(err).Warn("Invalid log level, using info")
	}
	logger.SetLevel(level)

	// Установка формата
	if config.Format == "json" {
		logger.SetFormatter(&logrus.JSONFormatter{
			TimestampFormat: time.RFC3339,
			FieldMap: logrus.FieldMap{
				logrus.FieldKeyTime:  "timestamp",
				logrus.FieldKeyLevel: "level",
				logrus.FieldKeyMsg:   "message",
			},
		})
	} else {
		logger.SetFormatter(&logrus.TextFormatter{
			FullTimestamp:   true,
			TimestampFormat: time.RFC3339,
		})
	}

	// Установка output
	if config.Output == "file" && config.OutputFile != "" {
		file, err := os.OpenFile(config.OutputFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			logger.WithError(err).Error("Failed to open log file, using stdout")
			logger.SetOutput(os.Stdout)
		} else {
			logger.SetOutput(file)
		}
	} else if config.Output == "stderr" {
		logger.SetOutput(os.Stderr)
	} else {
		logger.SetOutput(os.Stdout)
	}

	// Если асинхронное логирование не требуется
	if !config.Async {
		logger.SetNoLock()
	}

	return &Logger{
		Logger:      logger,
		serviceName: config.Service,
	}
}

// WithFields добавляет поля к логгеру
func (l *Logger) WithFields(fields logrus.Fields) *LogEntry {
	return &LogEntry{Entry: l.Logger.WithFields(fields)}
}

// WithContextFields добавляет стандартные поля контекста
func (l *Logger) WithContextFields(ctx context.Context, source string) *LogEntry {
	fields := logrus.Fields{
		"service": l.serviceName,
		"source":  source,
	}

	// Извлекаем request_id из контекста
	if reqID, ok := ctx.Value("request_id").(string); ok {
		fields["request_id"] = reqID
	}

	// Извлекаем trace_id из контекста
	if traceID, ok := ctx.Value("trace_id").(string); ok {
		fields["trace_id"] = traceID
	}

	// Извлекаем span_id из контекста
	if spanID, ok := ctx.Value("span_id").(string); ok {
		fields["span_id"] = spanID
	}

	return &LogEntry{Entry: l.Logger.WithFields(fields)}
}

// WithError добавляет поле error
func (l *Logger) WithError(err error, source string) *LogEntry {
	entry := l.WithContextFields(context.Background(), source)
	return entry.WithError(err)
}

// WithModule добавляет поле module
func (l *Logger) WithModule(ctx context.Context, source, module string) *LogEntry {
	entry := l.WithContextFields(ctx, source)
	entry.Entry = entry.Logger.WithField("module", module)
	return entry
}

// Debug логирует на уровне debug
func (l *Logger) Debug(args ...interface{}) {
	l.Logger.Debug(args...)
}

// Info логирует на уровне info
func (l *Logger) Info(args ...interface{}) {
	l.Logger.Info(args...)
}

// Warn логирует на уровне warn
func (l *Logger) Warn(args ...interface{}) {
	l.Logger.Warn(args...)
}

// Error логирует на уровне error
func (l *Logger) Error(args ...interface{}) {
	l.Logger.Error(args...)
}

// Fatal логирует на уровне fatal и вызывает os.Exit(1)
func (l *Logger) Fatal(args ...interface{}) {
	l.Logger.Fatal(args...)
}

// Методы LogEntry для chain-инга

// WithError добавляет поле error к LogEntry
func (e *LogEntry) WithError(err error) *LogEntry {
	return &LogEntry{Entry: e.Entry.WithError(err)}
}

// Debugf форматирует debug сообщение
func (e *LogEntry) Debugf(format string, args ...interface{}) {
	e.Entry.Debugf(format, args...)
}

// Infof форматирует info сообщение
func (e *LogEntry) Infof(format string, args ...interface{}) {
	e.Entry.Infof(format, args...)
}

// Warnf форматирует warn сообщение
func (e *LogEntry) Warnf(format string, args ...interface{}) {
	e.Entry.Warnf(format, args...)
}

// Errorf форматирует error сообщение
func (e *LogEntry) Errorf(format string, args ...interface{}) {
	e.Entry.Errorf(format, args...)
}

// DataSource логирует информацию о запросе к источнику данных
func (e *LogEntry) DataSource(source, query string, duration time.Duration, err error) {
	fields := logrus.Fields{
		"query":    query,
		"duration": duration.Milliseconds(),
	}

	if err != nil {
		fields["error"] = err.Error()
		e.WithFields(fields).Error("Data source request failed")
	} else {
		e.WithFields(fields).Info("Data source request completed")
	}
}

// MetricValue логирует значение метрики
func (e *LogEntry) MetricValue(name string, value float64, normalized float64, fallback bool) {
	fields := logrus.Fields{
		"metric_name":      name,
		"value":            value,
		"normalized_value": normalized,
		"fallback_used":    fallback,
	}

	if fallback {
		e.WithFields(fields).Warn("Using fallback value for metric")
	} else {
		e.WithFields(fields).Debug("Metric value computed")
	}
}

// HealthScore логирует расчитанный health score
func (e *LogEntry) HealthScore(score float64, totalMetrics, degradedMetrics int, duration time.Duration) {
	fields := logrus.Fields{
		"health_score":    score,
		"total_metrics":   totalMetrics,
		"degraded_metrics": degradedMetrics,
		"calc_duration":  duration.Milliseconds(),
	}

	if degradedMetrics > 0 {
		e.WithFields(fields).Warn("Health score computed in degraded mode")
	} else {
		e.WithFields(fields).Info("Health score computed successfully")
	}
}

// CircuitBreakerChange логирует изменение состояния circuit breaker
func (e *LogEntry) CircuitBreakerChange(name string, from, to string) {
	e.WithFields(logrus.Fields{
		"circuit_breaker": name,
		"state_from":      from,
		"state_to":        to,
	}).Info("Circuit breaker state changed")
}

// RateLimitViolation логирует превышение rate limit
func (e *LogEntry) RateLimitViolation(ip, endpoint string) {
	e.WithFields(logrus.Fields{
		"client_ip":  ip,
		"endpoint":   endpoint,
	}).Warn("Rate limit exceeded")
}

// ConfigChange логирует изменение конфигурации
func (e *LogEntry) ConfigChange(oldVersion, newVersion int) {
	e.WithFields(logrus.Fields{
		"old_version": oldVersion,
		"new_version": newVersion,
	}).Info("Configuration reloaded")
}

// GenerateRequestID генерирует уникальный ID запроса
func GenerateRequestID() string {
	return uuid.New().String()
}

// ContextWithRequestID добавляет request_id в контекст
func ContextWithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, "request_id", requestID)
}

// ContextWithTrace добавляет trace_id и span_id в контекст
func ContextWithTrace(ctx context.Context, traceID, spanID string) context.Context {
	ctx = context.WithValue(ctx, "trace_id", traceID)
	ctx = context.WithValue(ctx, "span_id", spanID)
	return ctx
}