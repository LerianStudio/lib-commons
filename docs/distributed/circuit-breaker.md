# Circuit Breaker

The circuit breaker pattern prevents cascading failures in distributed systems by monitoring for failures and temporarily blocking requests to failing services.

## Overview

The circuit breaker has three states:
- **Closed**: Normal operation, requests pass through
- **Open**: Failure threshold exceeded, requests are blocked
- **Half-Open**: Testing if the service has recovered

## Basic Usage

```go
import (
    "github.com/LerianStudio/lib-commons/commons/circuitbreaker"
    "time"
)

// Create a circuit breaker
cb := circuitbreaker.New(
    "payment-service",
    circuitbreaker.WithThreshold(5),           // Open after 5 failures
    circuitbreaker.WithTimeout(30*time.Second), // Try half-open after 30s
    circuitbreaker.WithMaxRequests(3),         // Allow 3 requests in half-open
)

// Wrap your function call
result, err := cb.Execute(func() (interface{}, error) {
    // Your actual service call
    return paymentService.ProcessPayment(paymentRequest)
})

if err != nil {
    if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
        // Circuit is open, service is unavailable
        return nil, errors.New("payment service temporarily unavailable")
    }
    // Handle other errors
    return nil, err
}

payment := result.(*PaymentResponse)
```

## Configuration Options

### Threshold

Number of consecutive failures before opening the circuit:

```go
cb := circuitbreaker.New("service", 
    circuitbreaker.WithThreshold(10),
)
```

### Timeout

Duration to wait before attempting to close the circuit:

```go
cb := circuitbreaker.New("service",
    circuitbreaker.WithTimeout(1 * time.Minute),
)
```

### Max Requests in Half-Open

Number of test requests allowed in half-open state:

```go
cb := circuitbreaker.New("service",
    circuitbreaker.WithMaxRequests(5),
)
```

### Success Threshold

Number of successes required to close from half-open:

```go
cb := circuitbreaker.New("service",
    circuitbreaker.WithSuccessThreshold(3),
)
```

### State Change Callbacks

Monitor state transitions:

```go
cb := circuitbreaker.New("service",
    circuitbreaker.WithOnStateChange(func(from, to circuitbreaker.State) {
        logger.Warn("Circuit breaker state changed",
            "service", "payment-service",
            "from", from,
            "to", to,
        )
    }),
)
```

## Advanced Usage

### With Context

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

result, err := cb.ExecuteWithContext(ctx, func(ctx context.Context) (interface{}, error) {
    return service.CallWithContext(ctx, request)
})
```

### Typed Circuit Breaker

Create a typed wrapper for better type safety:

```go
type PaymentCircuitBreaker struct {
    cb *circuitbreaker.CircuitBreaker
}

func NewPaymentCircuitBreaker() *PaymentCircuitBreaker {
    return &PaymentCircuitBreaker{
        cb: circuitbreaker.New("payment-service",
            circuitbreaker.WithThreshold(5),
            circuitbreaker.WithTimeout(30*time.Second),
        ),
    }
}

func (p *PaymentCircuitBreaker) ProcessPayment(req *PaymentRequest) (*PaymentResponse, error) {
    result, err := p.cb.Execute(func() (interface{}, error) {
        // Actual payment processing
        return processPaymentInternal(req)
    })
    
    if err != nil {
        return nil, err
    }
    
    return result.(*PaymentResponse), nil
}
```

### Manual State Control

```go
// Force open the circuit (for emergency situations)
cb.Open()

// Reset the circuit to closed state
cb.Reset()

// Get current state
state := cb.State()
switch state {
case circuitbreaker.StateClosed:
    logger.Info("Circuit is closed")
case circuitbreaker.StateOpen:
    logger.Warn("Circuit is open")
case circuitbreaker.StateHalfOpen:
    logger.Info("Circuit is half-open")
}
```

### Metrics and Monitoring

```go
// Get circuit breaker metrics
metrics := cb.Metrics()
logger.Info("Circuit breaker metrics",
    "requests", metrics.Requests,
    "failures", metrics.Failures,
    "successes", metrics.Successes,
    "consecutive_failures", metrics.ConsecutiveFailures,
    "state", metrics.State,
)

// Expose metrics for Prometheus
func (h *Handler) MetricsHandler(w http.ResponseWriter, r *http.Request) {
    metrics := h.paymentCB.Metrics()
    fmt.Fprintf(w, "circuit_breaker_state{service=\"payment\"} %d\n", metrics.State)
    fmt.Fprintf(w, "circuit_breaker_requests_total{service=\"payment\"} %d\n", metrics.Requests)
    fmt.Fprintf(w, "circuit_breaker_failures_total{service=\"payment\"} %d\n", metrics.Failures)
}
```

## Integration Patterns

### HTTP Client Integration

```go
type ResilientHTTPClient struct {
    client *http.Client
    cb     *circuitbreaker.CircuitBreaker
}

func (c *ResilientHTTPClient) Get(url string) (*http.Response, error) {
    result, err := c.cb.Execute(func() (interface{}, error) {
        return c.client.Get(url)
    })
    
    if err != nil {
        return nil, err
    }
    
    return result.(*http.Response), nil
}
```

### Database Connection

```go
type ResilientDB struct {
    db *sql.DB
    cb *circuitbreaker.CircuitBreaker
}

func (r *ResilientDB) Query(query string, args ...interface{}) (*sql.Rows, error) {
    result, err := r.cb.Execute(func() (interface{}, error) {
        return r.db.Query(query, args...)
    })
    
    if err != nil {
        if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
            // Return cached data or degraded response
            return r.getCachedResults(query, args...)
        }
        return nil, err
    }
    
    return result.(*sql.Rows), nil
}
```

### gRPC Integration

```go
func (c *Client) CallService(ctx context.Context, req *Request) (*Response, error) {
    result, err := c.cb.ExecuteWithContext(ctx, func(ctx context.Context) (interface{}, error) {
        return c.grpcClient.Call(ctx, req)
    })
    
    if err != nil {
        if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
            // Return fallback response
            return c.getFallbackResponse(req), nil
        }
        return nil, err
    }
    
    return result.(*Response), nil
}
```

## Best Practices

1. **Set appropriate thresholds**: Balance between tolerance and quick failure detection
2. **Use timeouts**: Prevent hanging requests from keeping circuits open
3. **Monitor state changes**: Log and alert on circuit state transitions
4. **Implement fallbacks**: Provide degraded service when circuit is open
5. **Test failure scenarios**: Regularly test circuit breaker behavior

## Common Pitfalls

1. **Too sensitive thresholds**: Opens too frequently
2. **Too long timeout**: Takes too long to recover
3. **No fallback strategy**: Complete service failure when open
4. **Shared circuit breakers**: One service affecting another

## Testing

```go
func TestCircuitBreaker(t *testing.T) {
    cb := circuitbreaker.New("test",
        circuitbreaker.WithThreshold(2),
        circuitbreaker.WithTimeout(100*time.Millisecond),
    )
    
    // Simulate failures to open circuit
    for i := 0; i < 2; i++ {
        _, err := cb.Execute(func() (interface{}, error) {
            return nil, errors.New("simulated failure")
        })
        assert.Error(t, err)
    }
    
    // Circuit should be open
    assert.Equal(t, circuitbreaker.StateOpen, cb.State())
    
    // Requests should fail immediately
    _, err := cb.Execute(func() (interface{}, error) {
        return "success", nil
    })
    assert.ErrorIs(t, err, circuitbreaker.ErrCircuitOpen)
    
    // Wait for timeout
    time.Sleep(150 * time.Millisecond)
    
    // Circuit should be half-open
    assert.Equal(t, circuitbreaker.StateHalfOpen, cb.State())
}
```