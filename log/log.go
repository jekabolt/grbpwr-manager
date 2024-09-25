package log

import (
	"context"

	"log/slog"

	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
)

type Config struct {
	Level     int  `mapstructure:"level"`
	AddSource bool `mapstructure:"add_source"`
}

// InterceptorLogger adapts slog logger to interceptor logger.
func InterceptorLogger(l *slog.Logger) logging.Logger {
	return logging.LoggerFunc(func(ctx context.Context, lvl logging.Level, msg string, fields ...any) {
		l.Log(ctx, slog.Level(lvl), msg, fields...)
	})
}

type SlogStripe struct {
	logger *slog.Logger
}

// NewSlogLeveledLogger initializes a new SlogLeveledLogger
func NewSlogLeveledLogger() *SlogStripe {
	return &SlogStripe{
		logger: slog.Default(),
	}
}

// Debugf logs a debug message
func (l *SlogStripe) Debugf(format string, v ...interface{}) {
	// slog.Default().Debug(format, slog.Any("msg", v))
}

// Errorf logs an error message
func (l *SlogStripe) Errorf(format string, v ...interface{}) {
	slog.Default().Error(format, slog.Any("msg", v))
}

// Infof logs an info message
func (l *SlogStripe) Infof(format string, v ...interface{}) {
	// slog.Default().Info(format, slog.Any("msg", v))
}

// Warnf logs a warning message
func (l *SlogStripe) Warnf(format string, v ...interface{}) {
	slog.Default().Warn(format, slog.Any("msg", v))
}
