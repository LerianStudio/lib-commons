package contracts

import (
	"reflect"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/LerianStudio/lib-commons/commons/observability"
	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/trace"
)

// TestLoggerInterfaceContract validates the core Logger interface stability
func TestLoggerInterfaceContract(t *testing.T) {
	t.Run("core_logger_interface_signature", func(t *testing.T) {
		var logger log.Logger
		loggerType := reflect.TypeOf(&logger).Elem()

		// Verify interface exists
		assert.Equal(t, "Logger", loggerType.Name())
		assert.Equal(t, reflect.Interface, loggerType.Kind())

		// Define expected methods that must remain stable
		expectedMethods := map[string]loggerMethodSignature{
			"Info": {
				params:  []string{"...interface {}"},
				returns: []string{},
			},
			"Infof": {
				params:  []string{"string", "...interface {}"},
				returns: []string{},
			},
			"Infoln": {
				params:  []string{"...interface {}"},
				returns: []string{},
			},
			"Error": {
				params:  []string{"...interface {}"},
				returns: []string{},
			},
			"Errorf": {
				params:  []string{"string", "...interface {}"},
				returns: []string{},
			},
			"Errorln": {
				params:  []string{"...interface {}"},
				returns: []string{},
			},
			"Warn": {
				params:  []string{"...interface {}"},
				returns: []string{},
			},
			"Warnf": {
				params:  []string{"string", "...interface {}"},
				returns: []string{},
			},
			"Warnln": {
				params:  []string{"...interface {}"},
				returns: []string{},
			},
			"Debug": {
				params:  []string{"...interface {}"},
				returns: []string{},
			},
			"Debugf": {
				params:  []string{"string", "...interface {}"},
				returns: []string{},
			},
			"Debugln": {
				params:  []string{"...interface {}"},
				returns: []string{},
			},
			"Fatal": {
				params:  []string{"...interface {}"},
				returns: []string{},
			},
			"Fatalf": {
				params:  []string{"string", "...interface {}"},
				returns: []string{},
			},
			"Fatalln": {
				params:  []string{"...interface {}"},
				returns: []string{},
			},
			"WithFields": {
				params:  []string{"...interface {}"},
				returns: []string{"log.Logger"},
			},
			"WithDefaultMessageTemplate": {
				params:  []string{"string"},
				returns: []string{"log.Logger"},
			},
			"Sync": {
				params:  []string{},
				returns: []string{"error"},
			},
		}

		// Verify each expected method exists
		for i := 0; i < loggerType.NumMethod(); i++ {
			method := loggerType.Method(i)
			expectedSig, exists := expectedMethods[method.Name]

			if exists {
				validateLoggerMethodSignature(t, method, expectedSig, "log.Logger."+method.Name)
				delete(expectedMethods, method.Name) // Mark as found
			}
		}

		// Verify no expected methods are missing
		for missingMethod := range expectedMethods {
			t.Errorf("Expected method %s is missing from log.Logger interface", missingMethod)
		}
	})

	t.Run("logger_implementations_contract", func(t *testing.T) {
		// Test that built-in logger implementations satisfy the interface
		implementations := []log.Logger{
			&log.GoLogger{Level: log.InfoLevel},
			&log.NoneLogger{},
		}

		for i, impl := range implementations {
			assert.Implements(t, (*log.Logger)(nil), impl,
				"Logger implementation %d should implement log.Logger interface", i)
		}
	})
}

// TestObservabilityLoggerContract validates the observability Logger interface
func TestObservabilityLoggerContract(t *testing.T) {
	t.Run("observability_logger_interface_signature", func(t *testing.T) {
		var logger observability.Logger
		loggerType := reflect.TypeOf(&logger).Elem()

		// Verify interface exists
		assert.Equal(t, "Logger", loggerType.Name())
		assert.Equal(t, reflect.Interface, loggerType.Kind())

		// Define expected methods for observability logger
		expectedMethods := map[string]loggerMethodSignature{
			"Debug": {
				params:  []string{"...interface {}"},
				returns: []string{},
			},
			"Info": {
				params:  []string{"...interface {}"},
				returns: []string{},
			},
			"Warn": {
				params:  []string{"...interface {}"},
				returns: []string{},
			},
			"Error": {
				params:  []string{"...interface {}"},
				returns: []string{},
			},
			"With": {
				params:  []string{"map[string]interface {}"},
				returns: []string{"observability.Logger"},
			},
			"WithContext": {
				params:  []string{"trace.SpanContext"},
				returns: []string{"observability.Logger"},
			},
			"WithSpan": {
				params:  []string{"trace.Span"},
				returns: []string{"observability.Logger"},
			},
		}

		// Verify methods exist (basic check)
		for expectedMethod := range expectedMethods {
			found := false
			for i := 0; i < loggerType.NumMethod(); i++ {
				if loggerType.Method(i).Name == expectedMethod {
					found = true
					break
				}
			}
			assert.True(t, found, "Method %s should exist in observability.Logger", expectedMethod)
		}
	})
}

// TestLoggerBehaviorContract validates expected logger behaviors
func TestLoggerBehaviorContract(t *testing.T) {
	t.Run("go_logger_behavior_contract", func(t *testing.T) {
		logger := &log.GoLogger{Level: log.InfoLevel}

		// Test that logger methods don't panic with various inputs
		assert.NotPanics(t, func() {
			logger.Info("test message")
		})

		assert.NotPanics(t, func() {
			logger.Infof("test %s", "formatted")
		})

		assert.NotPanics(t, func() {
			logger.Error("error message")
		})

		assert.NotPanics(t, func() {
			logger.Debug("debug message")
		})

		// Test WithFields returns a new logger
		fieldsLogger := logger.WithFields("key", "value", "another", "field")
		assert.NotNil(t, fieldsLogger)
		assert.Implements(t, (*log.Logger)(nil), fieldsLogger)

		// WithFields should not modify original logger (immutability contract)
		assert.NotEqual(t, logger, fieldsLogger)

		// Test WithDefaultMessageTemplate
		templateLogger := logger.WithDefaultMessageTemplate("Default: %s")
		assert.NotNil(t, templateLogger)
		assert.Implements(t, (*log.Logger)(nil), templateLogger)

		// Test Sync doesn't panic
		assert.NotPanics(t, func() {
			err := logger.Sync()
			// Sync may or may not return an error depending on implementation
			_ = err
		})
	})

	t.Run("nil_logger_behavior_contract", func(t *testing.T) {
		logger := &log.NoneLogger{}

		// Nil logger should implement interface
		assert.Implements(t, (*log.Logger)(nil), logger)

		// All operations should be no-ops and not panic
		assert.NotPanics(t, func() {
			logger.Info("this should be ignored")
			logger.Error("this should also be ignored")
			logger.Debug("debug ignored")
			logger.Fatal("fatal ignored")
		})

		// WithFields should return a logger (possibly another nil logger)
		fieldsLogger := logger.WithFields("key", "value")
		assert.NotNil(t, fieldsLogger)
		assert.Implements(t, (*log.Logger)(nil), fieldsLogger)

		// Sync should not return error for nil logger
		err := logger.Sync()
		assert.NoError(t, err)
	})

	t.Run("logger_chaining_contract", func(t *testing.T) {
		logger := &log.GoLogger{Level: log.InfoLevel}

		// Test method chaining behavior
		chainedLogger := logger.
			WithFields("service", "test").
			WithFields("version", "1.0.0").
			WithDefaultMessageTemplate("Service %s:")

		assert.NotNil(t, chainedLogger)
		assert.Implements(t, (*log.Logger)(nil), chainedLogger)

		// Chained logger should work normally
		assert.NotPanics(t, func() {
			chainedLogger.Info("test message")
		})
	})
}

// TestLoggerObservabilityIntegration validates observability logger integration
func TestLoggerObservabilityIntegration(t *testing.T) {
	t.Run("observability_logger_with_span_contract", func(t *testing.T) {
		// This test validates the contract for observability logger integration
		// We test the interface requirements without needing actual telemetry setup

		// Create a mock span context for testing
		spanCtx := trace.SpanContext{}

		// Verify that observability logger interface can accept span context
		// This ensures the contract supports tracing integration
		var obsLogger observability.Logger
		obsLoggerType := reflect.TypeOf(&obsLogger).Elem()

		// Check for WithSpan method
		withSpanMethod, exists := obsLoggerType.MethodByName("WithSpan")
		if exists {
			// Verify method signature accepts trace.Span
			methodType := withSpanMethod.Type
			if methodType.NumIn() >= 2 { // receiver + span parameter
				spanParamType := methodType.In(1)
				spanInterfaceType := reflect.TypeOf((*trace.Span)(nil)).Elem()

				assert.True(t, spanParamType.Implements(spanInterfaceType) ||
					spanParamType == spanInterfaceType,
					"WithSpan should accept trace.Span parameter")
			}
		}

		// Check for WithContext method
		withContextMethod, exists := obsLoggerType.MethodByName("WithContext")
		if exists {
			methodType := withContextMethod.Type
			if methodType.NumIn() >= 2 {
				contextParamType := methodType.In(1)
				spanContextType := reflect.TypeOf(spanCtx)

				assert.Equal(t, spanContextType, contextParamType,
					"WithContext should accept trace.SpanContext parameter")
			}
		}
	})

	t.Run("structured_logging_contract", func(t *testing.T) {
		// Test that structured logging interface supports field addition
		var obsLogger observability.Logger
		obsLoggerType := reflect.TypeOf(&obsLogger).Elem()

		// Verify With method for structured fields
		withMethod, exists := obsLoggerType.MethodByName("With")
		if exists {
			methodType := withMethod.Type
			if methodType.NumIn() >= 2 {
				fieldsParamType := methodType.In(1)
				expectedType := reflect.TypeOf(map[string]interface{}{})

				assert.Equal(t, expectedType, fieldsParamType,
					"With method should accept map[string]interface{} for structured fields")
			}
		}
	})
}

// TestLoggerPerformanceContract validates performance characteristics
func TestLoggerPerformanceContract(t *testing.T) {
	t.Run("logger_non_blocking_contract", func(t *testing.T) {
		// Test that logger operations complete quickly (non-blocking contract)
		logger := &log.GoLogger{Level: log.InfoLevel}

		// Log operations should complete within reasonable time
		start := time.Now()
		for i := 0; i < 100; i++ {
			logger.Info("performance test message", i)
		}
		duration := time.Since(start)

		// 100 log messages should complete within 100ms under normal conditions
		// This validates the non-blocking performance contract
		assert.Less(t, duration, 100*time.Millisecond,
			"100 log operations should complete within 100ms")
	})

	t.Run("nil_logger_zero_overhead_contract", func(t *testing.T) {
		// Test that nil logger has minimal overhead
		nilLogger := &log.NoneLogger{}

		start := time.Now()
		for i := 0; i < 1000; i++ {
			nilLogger.Info("nil logger message", i)
		}
		duration := time.Since(start)

		// Nil logger should have very low overhead
		assert.Less(t, duration, 10*time.Millisecond,
			"1000 nil logger operations should complete within 10ms")
	})
}

// Helper types and functions

type loggerMethodSignature struct {
	params  []string
	returns []string
}

func validateLoggerMethodSignature(
	t *testing.T,
	method reflect.Method,
	expected loggerMethodSignature,
	methodName string,
) {
	methodType := method.Type

	// For interface methods from reflect.Type.Method(), NumIn() does NOT include receiver
	// This is different from method values where receiver is included
	expectedParamCount := len(expected.params)
	actualParamCount := methodType.NumIn()

	// Handle variadic parameters (represented as "...interface {}" in our notation)
	if len(expected.params) > 0 &&
		(expected.params[len(expected.params)-1] == "...interface {}" ||
			expected.params[len(expected.params)-1] == "...interface{}") {
		// For variadic methods, check if last parameter is variadic
		if methodType.IsVariadic() {
			// Accept variadic methods - the last parameter counts as 1 but can accept multiple values
			assert.GreaterOrEqual(t, actualParamCount, expectedParamCount-1,
				"Method %s should have at least %d parameters", methodName, expectedParamCount-1)
		} else {
			// Non-variadic method should match exactly
			assert.Equal(t, expectedParamCount, actualParamCount,
				"Method %s should have %d parameters", methodName, expectedParamCount)
		}
	} else {
		assert.Equal(t, expectedParamCount, actualParamCount,
			"Method %s should have %d parameters", methodName, expectedParamCount)
	}

	// Validate return count
	expectedReturnCount := len(expected.returns)
	actualReturnCount := methodType.NumOut()
	assert.Equal(t, expectedReturnCount, actualReturnCount,
		"Method %s should return %d values", methodName, expectedReturnCount)
}
