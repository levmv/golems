package engine

type logLevel int

const (
	debugLevel logLevel = iota
	infoLevel
	warnLevel
	errorLevel
)

// logf routes through the configured llm.Logger, tolerating a nil logger.
func (e *Engine) logf(level logLevel, format string, args ...any) {
	if e.log == nil {
		return
	}
	switch level {
	case errorLevel:
		e.log.Error(format, args...)
	case warnLevel:
		e.log.Warn(format, args...)
	case debugLevel:
		e.log.Debug(format, args...)
	default:
		e.log.Info(format, args...)
	}
}
