package log

import (
	"fmt"
)

// StructuredLogger provides structured logging with fields
type StructuredLogger struct {
	logger Logger
	fields map[string]any
}

// NewStructuredLogger creates a new structured logger
func NewStructuredLogger(logger Logger) *StructuredLogger {
	return &StructuredLogger{
		logger: logger,
		fields: make(map[string]any),
	}
}

// WithFields adds fields to the logger
func (sl *StructuredLogger) WithFields(fields map[string]any) *StructuredLogger {
	newLogger := &StructuredLogger{
		logger: sl.logger,
		fields: make(map[string]any),
	}

	// Copy existing fields
	for k, v := range sl.fields {
		newLogger.fields[k] = v
	}

	// Add new fields
	for k, v := range fields {
		newLogger.fields[k] = v
	}

	return newLogger
}

// WithField adds a single field to the logger
func (sl *StructuredLogger) WithField(key string, value any) *StructuredLogger {
	return sl.WithFields(map[string]any{key: value})
}

// WithService adds service context
func (sl *StructuredLogger) WithService(serviceName string) *StructuredLogger {
	return sl.WithField("service", serviceName)
}

// WithOperation adds operation context
func (sl *StructuredLogger) WithOperation(operationName string) *StructuredLogger {
	return sl.WithField("operation", operationName)
}

// WithBusinessContext adds business context
func (sl *StructuredLogger) WithBusinessContext(organizationID, ledgerID string) *StructuredLogger {
	fields := make(map[string]any)
	if organizationID != "" {
		fields["organization_id"] = organizationID
	}

	if ledgerID != "" {
		fields["ledger_id"] = ledgerID
	}

	return sl.WithFields(fields)
}

// WithError adds error context
func (sl *StructuredLogger) WithError(err error) *StructuredLogger {
	if err != nil {
		return sl.WithField("error", err.Error())
	}

	return sl
}

// formatMessage formats the message with fields
func (sl *StructuredLogger) formatMessage(msg string) string {
	if len(sl.fields) == 0 {
		return msg
	}

	fieldStr := ""
	for k, v := range sl.fields {
		if fieldStr != "" {
			fieldStr += " "
		}

		fieldStr += fmt.Sprintf("%s=%v", k, v)
	}

	return fmt.Sprintf("%s [%s]", msg, fieldStr)
}

// Info logs an info message
func (sl *StructuredLogger) Info(msg string) {
	sl.logger.Info(sl.formatMessage(msg))
}

// Infof logs a formatted info message
func (sl *StructuredLogger) Infof(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	sl.Info(msg)
}

// Error logs an error message
func (sl *StructuredLogger) Error(msg string) {
	sl.logger.Error(sl.formatMessage(msg))
}

// Errorf logs a formatted error message
func (sl *StructuredLogger) Errorf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	sl.Error(msg)
}

// Warn logs a warning message
func (sl *StructuredLogger) Warn(msg string) {
	sl.logger.Warn(sl.formatMessage(msg))
}

// Warnf logs a formatted warning message
func (sl *StructuredLogger) Warnf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	sl.Warn(msg)
}

// Debug logs a debug message
func (sl *StructuredLogger) Debug(msg string) {
	sl.logger.Debug(sl.formatMessage(msg))
}

// Debugf logs a formatted debug message
func (sl *StructuredLogger) Debugf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	sl.Debug(msg)
}

// Fatal logs a fatal message
func (sl *StructuredLogger) Fatal(msg string) {
	sl.logger.Fatal(sl.formatMessage(msg))
}

// Fatalf logs a formatted fatal message
func (sl *StructuredLogger) Fatalf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	sl.Fatal(msg)
}
