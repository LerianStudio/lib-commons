# Retry Logic

The retry package provides configurable retry mechanisms with exponential backoff, jitter, and custom retry conditions for handling transient failures.

## Overview

Features:
- Configurable retry attempts
- Exponential backoff with jitter
- Custom retry conditions
- Context support for cancellation
- Generic type support

## Basic Usage

```go
import (
    "github.com/LerianStudio/lib-commons/commons/retry"
    "time"
)

// Simple retry with default settings
result, err := retry.Do(func() (string, error) {
    return callExternalService()
})

// With custom configuration
result, err := retry.Do(
    func() (*Response, error) {
        return apiClient.Get("/endpoint")
    },
    retry.WithMaxRetries(5),
    retry.WithDelay(100*time.Millisecond),
)
```

## Configuration Options

### Maximum Retries

```go
// Retry up to 3 times (4 total attempts)
result, err := retry.Do(
    operation,
    retry.WithMaxRetries(3),
)
```

### Fixed Delay

```go
// Wait 1 second between retries
result, err := retry.Do(
    operation,
    retry.WithDelay(1*time.Second),
)
```

### Exponential Backoff

```go
// Start with 100ms, double each time
result, err := retry.Do(
    operation,
    retry.WithExponentialBackoff(100*time.Millisecond, 2.0),
)
// Delays: 100ms, 200ms, 400ms, 800ms...
```

### Maximum Delay

```go
// Cap the delay at 30 seconds
result, err := retry.Do(
    operation,
    retry.WithExponentialBackoff(1*time.Second, 2.0),
    retry.WithMaxDelay(30*time.Second),
)
```

### Jitter

```go
// Add randomness to prevent thundering herd
result, err := retry.Do(
    operation,
    retry.WithExponentialBackoff(1*time.Second, 2.0),
    retry.WithJitter(0.1), // ±10% randomness
)
```

### Custom Retry Conditions

```go
// Only retry on specific errors
result, err := retry.Do(
    operation,
    retry.WithRetryIf(func(err error) bool {
        // Retry on timeout or temporary errors
        if errors.Is(err, context.DeadlineExceeded) {
            return true
        }
        if tempErr, ok := err.(interface{ Temporary() bool }); ok {
            return tempErr.Temporary()
        }
        return false
    }),
)
```

## Advanced Usage

### With Context

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

result, err := retry.DoWithContext(ctx,
    func(ctx context.Context) (*Data, error) {
        return fetchDataWithContext(ctx)
    },
    retry.WithMaxRetries(10),
    retry.WithExponentialBackoff(100*time.Millisecond, 2.0),
)
```

### HTTP Client with Retry

```go
type RetryableHTTPClient struct {
    client *http.Client
}

func (c *RetryableHTTPClient) Get(url string) (*http.Response, error) {
    return retry.Do(
        func() (*http.Response, error) {
            resp, err := c.client.Get(url)
            if err != nil {
                return nil, err
            }
            
            // Retry on 5xx errors
            if resp.StatusCode >= 500 {
                resp.Body.Close()
                return nil, fmt.Errorf("server error: %d", resp.StatusCode)
            }
            
            return resp, nil
        },
        retry.WithMaxRetries(3),
        retry.WithExponentialBackoff(500*time.Millisecond, 2.0),
        retry.WithRetryIf(func(err error) bool {
            // Retry on network errors or 5xx
            return err != nil
        }),
    )
}
```

### Database Operations

```go
func (r *Repository) SaveWithRetry(ctx context.Context, data *Model) error {
    _, err := retry.DoWithContext(ctx,
        func(ctx context.Context) (struct{}, error) {
            return struct{}{}, r.db.WithContext(ctx).Create(data).Error
        },
        retry.WithMaxRetries(3),
        retry.WithDelay(100*time.Millisecond),
        retry.WithRetryIf(func(err error) bool {
            // Retry on deadlock or connection errors
            return isDeadlock(err) || isConnectionError(err)
        }),
    )
    return err
}
```

### Async Operations with Retry

```go
func ProcessBatchWithRetry(items []Item) error {
    return retry.DoAsync(
        func() error {
            return processBatch(items)
        },
        retry.WithMaxRetries(5),
        retry.WithExponentialBackoff(1*time.Second, 1.5),
        retry.WithOnRetry(func(attempt int, err error) {
            logger.Warn("Batch processing failed, retrying",
                "attempt", attempt,
                "error", err,
            )
        }),
    )
}
```

## Retry Strategies

### Linear Backoff

```go
// Fixed delay between retries
result, err := retry.Do(
    operation,
    retry.WithDelay(1*time.Second),
    retry.WithMaxRetries(5),
)
```

### Exponential Backoff

```go
// Exponentially increasing delays
result, err := retry.Do(
    operation,
    retry.WithExponentialBackoff(100*time.Millisecond, 2.0),
    retry.WithMaxRetries(10),
)
```

### Fibonacci Backoff

```go
// Custom backoff strategy
func fibonacciBackoff(baseDelay time.Duration) retry.Option {
    fib := []int{1, 1, 2, 3, 5, 8, 13, 21}
    return retry.WithBackoffFunc(func(attempt int) time.Duration {
        if attempt >= len(fib) {
            attempt = len(fib) - 1
        }
        return baseDelay * time.Duration(fib[attempt])
    })
}

result, err := retry.Do(
    operation,
    fibonacciBackoff(100*time.Millisecond),
)
```

## Error Handling

### Permanent vs Transient Errors

```go
type PermanentError struct {
    Err error
}

func (e PermanentError) Error() string {
    return e.Err.Error()
}

// Don't retry permanent errors
result, err := retry.Do(
    func() (string, error) {
        data, err := fetchData()
        if err != nil {
            if isNotFound(err) {
                // Don't retry 404 errors
                return "", PermanentError{err}
            }
            // Retry other errors
            return "", err
        }
        return data, nil
    },
    retry.WithRetryIf(func(err error) bool {
        _, isPermanent := err.(PermanentError)
        return !isPermanent
    }),
)
```

### Collecting All Errors

```go
var allErrors []error

result, err := retry.Do(
    operation,
    retry.WithMaxRetries(3),
    retry.WithOnRetry(func(attempt int, err error) {
        allErrors = append(allErrors, fmt.Errorf("attempt %d: %w", attempt, err))
    }),
)

if err != nil {
    // Log all attempts
    for _, e := range allErrors {
        logger.Error("Retry attempt failed", "error", e)
    }
}
```

## Monitoring and Observability

```go
type RetryMetrics struct {
    mu              sync.Mutex
    totalRetries    int64
    successfulRetries int64
    failedRetries   int64
}

func (m *RetryMetrics) Record(attempts int, err error) {
    m.mu.Lock()
    defer m.mu.Unlock()
    
    if attempts > 1 {
        m.totalRetries += int64(attempts - 1)
        if err == nil {
            m.successfulRetries++
        } else {
            m.failedRetries++
        }
    }
}

// Use with retry
metrics := &RetryMetrics{}

result, err := retry.Do(
    operation,
    retry.WithMaxRetries(3),
    retry.WithOnComplete(func(attempts int, err error) {
        metrics.Record(attempts, err)
    }),
)
```

## Best Practices

1. **Set reasonable limits**: Don't retry indefinitely
2. **Use exponential backoff**: Prevents overwhelming failing services
3. **Add jitter**: Prevents synchronized retries (thundering herd)
4. **Identify permanent failures**: Don't retry non-transient errors
5. **Monitor retry metrics**: Track retry patterns and success rates
6. **Use context for cancellation**: Allow graceful shutdown

## Testing

```go
func TestRetryWithMockFailures(t *testing.T) {
    attempts := 0
    
    result, err := retry.Do(
        func() (string, error) {
            attempts++
            if attempts < 3 {
                return "", errors.New("temporary failure")
            }
            return "success", nil
        },
        retry.WithMaxRetries(5),
        retry.WithDelay(10*time.Millisecond),
    )
    
    assert.NoError(t, err)
    assert.Equal(t, "success", result)
    assert.Equal(t, 3, attempts)
}

func TestRetryWithTimeout(t *testing.T) {
    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()
    
    _, err := retry.DoWithContext(ctx,
        func(ctx context.Context) (string, error) {
            time.Sleep(200 * time.Millisecond)
            return "too late", nil
        },
        retry.WithMaxRetries(3),
    )
    
    assert.ErrorIs(t, err, context.DeadlineExceeded)
}
```