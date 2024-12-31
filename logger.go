package forwarder

import (
	"fmt"
	"log/slog"
)

// Logger 日志接口
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}

// logger 默认日志记录器
type logger struct {
	logger *slog.Logger
}

// GetOrSetLogger 设置或者获取默认日志记录器
//
// @param customLogger ...Logger 自定义的日志记录器
//
// @return Logger 返回设置的日志记录器
func (gm *GlobalManager) GetOrSetLogger(customLogger ...Logger) Logger {
	if len(customLogger) > 0 {
		gm.logger = customLogger[0]
	}
	if gm.logger == nil {
		gm.logger = &logger{
			logger: slog.Default(),
		}
	}
	return gm.logger
}

func (l *logger) Debugf(format string, args ...any) {
	l.logger.Debug(sprintf(format, args...))
}

func (l *logger) Infof(format string, args ...any) {
	l.logger.Info(sprintf(format, args...))
}

func (l *logger) Warnf(format string, args ...any) {
	l.logger.Warn(sprintf(format, args...))
}

func (l *logger) Errorf(format string, args ...any) {
	l.logger.Error(sprintf(format, args...))
}

// sprintf 格式化字符串
func sprintf(format string, args ...any) string {
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}
