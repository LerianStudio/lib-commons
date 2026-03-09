//go:build unit

package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	errTestPanicRecover     = errors.New("test error")
	errOriginalPanicRecover = errors.New("original error")
)

// TestLogPanicWithStack_NilLogger tests that nil logger doesn't cause panic.
func TestLogPanicWithStack_NilLogger(t *testing.T) {
	t.Parallel()

	require.NotPanics(t, func() {
		logPanicWithStack(nil, "test", "panic value", []byte("stack trace"))
	})
}

// TestLogPanicWithStack_ValidLogger tests logging with a valid logger.
func TestLogPanicWithStack_ValidLogger(t *testing.T) {
	t.Parallel()

	logger := newTestLogger()
	stack := []byte("goroutine 1 [running]:\nmain.main()\n\t/path/to/file.go:10")

	logPanicWithStack(logger, "test-handler", "test panic", stack)

	assert.True(t, logger.wasPanicLogged())
	assert.NotEmpty(t, logger.errorCalls)
}

// TestLogPanicWithStack_DifferentPanicTypes tests various panic value types.
func TestLogPanicWithStack_DifferentPanicTypes(t *testing.T) {
	t.Parallel()

	type customStruct struct {
		Field string
		Code  int
	}

	tests := []struct {
		name       string
		panicValue any
	}{
		{
			name:       "string panic value",
			panicValue: "something went wrong",
		},
		{
			name:       "error panic value",
			panicValue: errTestPanicRecover,
		},
		{
			name:       "int panic value",
			panicValue: 42,
		},
		{
			name:       "struct panic value",
			panicValue: customStruct{Field: "test", Code: 500},
		},
		{
			name:       "nil panic value",
			panicValue: nil,
		},
		{
			name:       "bool panic value",
			panicValue: true,
		},
		{
			name:       "float panic value",
			panicValue: 3.14159,
		},
		{
			name:       "slice panic value",
			panicValue: []string{"a", "b", "c"},
		},
		{
			name:       "map panic value",
			panicValue: map[string]int{"key": 123},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logger := newTestLogger()
			stack := []byte("test stack")

			require.NotPanics(t, func() {
				logPanicWithStack(logger, "test", tt.panicValue, stack)
			})

			assert.True(t, logger.wasPanicLogged())
		})
	}
}

// TestRecoverAndLog_NilLogger tests RecoverAndLog with nil logger.
func TestRecoverAndLog_NilLogger(t *testing.T) {
	t.Parallel()

	require.NotPanics(t, func() {
		func() {
			defer RecoverAndLog(nil, "test-nil-logger")

			panic("test panic")
		}()
	})
}

// TestRecoverAndLogWithContext_NilLogger tests RecoverAndLogWithContext with nil logger.
func TestRecoverAndLogWithContext_NilLogger(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	require.NotPanics(t, func() {
		func() {
			defer RecoverAndLogWithContext(ctx, nil, "component", "test-nil-logger")

			panic("test panic")
		}()
	})
}

// TestRecoverAndCrash_NilLogger tests RecoverAndCrash with nil logger still re-panics.
func TestRecoverAndCrash_NilLogger(t *testing.T) {
	t.Parallel()

	defer func() {
		r := recover()
		require.NotNil(t, r, "Should re-panic even with nil logger")
		assert.Equal(t, "test panic", r)
	}()

	func() {
		defer RecoverAndCrash(nil, "test-nil-logger")

		panic("test panic")
	}()

	t.Fatal("Should not reach here")
}

// TestRecoverWithPolicy_NilLogger tests RecoverWithPolicy with nil logger.
func TestRecoverWithPolicy_NilLogger(t *testing.T) {
	t.Parallel()

	t.Run("KeepRunning with nil logger", func(t *testing.T) {
		t.Parallel()

		require.NotPanics(t, func() {
			func() {
				defer RecoverWithPolicy(nil, "test", KeepRunning)

				panic("test panic")
			}()
		})
	})

	t.Run("CrashProcess with nil logger still re-panics", func(t *testing.T) {
		t.Parallel()

		defer func() {
			r := recover()
			require.NotNil(t, r, "Should re-panic with CrashProcess")
		}()

		func() {
			defer RecoverWithPolicy(nil, "test", CrashProcess)

			panic("test panic")
		}()

		t.Fatal("Should not reach here")
	})
}

// TestRecoverWithPolicyAndContext_NilLogger tests context variant with nil logger.
func TestRecoverWithPolicyAndContext_NilLogger(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("KeepRunning with nil logger", func(t *testing.T) {
		t.Parallel()

		require.NotPanics(t, func() {
			func() {
				defer RecoverWithPolicyAndContext(ctx, nil, "component", "test", KeepRunning)

				panic("test panic")
			}()
		})
	})

	t.Run("CrashProcess with nil logger still re-panics", func(t *testing.T) {
		t.Parallel()

		defer func() {
			r := recover()
			require.NotNil(t, r, "Should re-panic with CrashProcess")
		}()

		func() {
			defer RecoverWithPolicyAndContext(ctx, nil, "component", "test", CrashProcess)

			panic("test panic")
		}()

		t.Fatal("Should not reach here")
	})
}

// TestLogPanic_CallsLogPanicWithStack tests that logPanic delegates correctly.
func TestLogPanic_CallsLogPanicWithStack(t *testing.T) {
	t.Parallel()

	logger := newTestLogger()

	logPanic(logger, "test-handler", "panic value")

	assert.True(t, logger.wasPanicLogged())
	assert.NotEmpty(t, logger.errorCalls)
}

// TestRecoverAndLog_PreservesPanicValue tests panic value is correctly captured.
func TestRecoverAndLog_PreservesPanicValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		panicValue any
	}{
		{
			name:       "string value",
			panicValue: "panic message",
		},
		{
			name:       "error value",
			panicValue: errOriginalPanicRecover,
		},

		{
			name:       "integer value",
			panicValue: 12345,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logger := newTestLogger()

			func() {
				defer RecoverAndLog(logger, "test")

				panic(tt.panicValue)
			}()

			assert.True(t, logger.wasPanicLogged())
		})
	}
}

// TestRecoverAndCrash_PreservesPanicValue tests re-panicked value is preserved.
func TestRecoverAndCrash_PreservesPanicValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		panicValue any
	}{
		{
			name:       "string value",
			panicValue: "original panic",
		},
		{
			name:       "error value",
			panicValue: errOriginalPanicRecover,
		},
		{
			name:       "integer value",
			panicValue: 99999,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logger := newTestLogger()

			defer func() {
				r := recover()
				require.NotNil(t, r)
				assert.Equal(t, tt.panicValue, r)
			}()

			func() {
				defer RecoverAndCrash(logger, "test")

				panic(tt.panicValue)
			}()

			t.Fatal("Should not reach here")
		})
	}
}

// TestRecoverAndCrashWithContext_PreservesPanicValue tests context variant preserves value.
func TestRecoverAndCrashWithContext_PreservesPanicValue(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	logger := newTestLogger()
	expectedValue := "context panic value"

	defer func() {
		r := recover()
		require.NotNil(t, r)
		assert.Equal(t, expectedValue, r)
	}()

	func() {
		defer RecoverAndCrashWithContext(ctx, logger, "component", "handler")

		panic(expectedValue)
	}()

	t.Fatal("Should not reach here")
}

// TestRecoverFunctions_NoPanic tests all recover functions when no panic occurs.
func TestRecoverFunctions_NoPanic(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	logger := newTestLogger()

	t.Run("RecoverAndLog no panic", func(t *testing.T) {
		t.Parallel()

		testLogger := newTestLogger()

		func() {
			defer RecoverAndLog(testLogger, "test")
		}()

		assert.False(t, testLogger.wasPanicLogged())
	})

	t.Run("RecoverAndLogWithContext no panic", func(t *testing.T) {
		t.Parallel()

		testLogger := newTestLogger()

		func() {
			defer RecoverAndLogWithContext(ctx, testLogger, "component", "test")
		}()

		assert.False(t, testLogger.wasPanicLogged())
	})

	t.Run("RecoverAndCrash no panic", func(t *testing.T) {
		t.Parallel()

		func() {
			defer RecoverAndCrash(logger, "test")
		}()

		assert.False(t, logger.wasPanicLogged())
	})

	t.Run("RecoverAndCrashWithContext no panic", func(t *testing.T) {
		t.Parallel()

		testLogger := newTestLogger()

		func() {
			defer RecoverAndCrashWithContext(ctx, testLogger, "component", "test")
		}()

		assert.False(t, testLogger.wasPanicLogged())
	})

	t.Run("RecoverWithPolicy no panic", func(t *testing.T) {
		t.Parallel()

		testLogger := newTestLogger()

		func() {
			defer RecoverWithPolicy(testLogger, "test", KeepRunning)
		}()

		assert.False(t, testLogger.wasPanicLogged())
	})

	t.Run("RecoverWithPolicyAndContext no panic", func(t *testing.T) {
		t.Parallel()

		testLogger := newTestLogger()

		func() {
			defer RecoverWithPolicyAndContext(ctx, testLogger, "component", "test", KeepRunning)
		}()

		assert.False(t, testLogger.wasPanicLogged())
	})
}

// TestHandlePanicValue tests the HandlePanicValue function for external recovery integration.
func TestHandlePanicValue(t *testing.T) {
	t.Parallel()

	t.Run("logs and records observability for panic value", func(t *testing.T) {
		t.Parallel()

		logger := newTestLogger()
		ctx := context.Background()

		HandlePanicValue(ctx, logger, "test panic", "matcher", "http_handler")

		assert.True(t, logger.wasPanicLogged())
		assert.NotEmpty(t, logger.errorCalls)
	})

	t.Run("handles nil panic value gracefully", func(t *testing.T) {
		t.Parallel()

		logger := newTestLogger()
		ctx := context.Background()

		require.NotPanics(t, func() {
			HandlePanicValue(ctx, logger, nil, "matcher", "http_handler")
		})

		assert.False(t, logger.wasPanicLogged())
	})

	t.Run("handles nil logger gracefully", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()

		require.NotPanics(t, func() {
			HandlePanicValue(ctx, nil, "test panic", "matcher", "http_handler")
		})
	})

	t.Run("handles various panic value types", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name       string
			panicValue any
		}{
			{"string", "panic message"},
			{"error", errTestPanicRecover},
			{"integer", 42},
			{"struct", struct{ Code int }{Code: 500}},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				logger := newTestLogger()
				ctx := context.Background()

				require.NotPanics(t, func() {
					HandlePanicValue(ctx, logger, tt.panicValue, "matcher", "handler")
				})

				assert.True(t, logger.wasPanicLogged())
			})
		}
	})
}
