# HTTP Client for Internal Networks

## Overview

The HTTP Client is designed for internal network environments where services may or may not have SSL certificates. It attempts HTTPS first but gracefully falls back to HTTP when SSL is unavailable, making it perfect for systems behind strong firewalls.

## Key Features

- **HTTPS First**: Always attempts HTTPS connection first
- **HTTP Fallback**: Falls back to HTTP if HTTPS fails
- **Internal Network Optimized**: Allows self-signed certificates
- **Security Configurable**: Can be made more restrictive for production
- **Connection Pooling**: Optimized for high-performance scenarios

## Basic Usage

### For Internal Networks (Recommended)

```go
package main

import (
    "context"
    "fmt"
    "net/http"
    
    httputil "github.com/LerianStudio/lib-commons/commons/net/http"
)

func main() {
    // Create a client optimized for internal networks
    client, err := httputil.NewHTTPClientForInternalNetwork()
    if err != nil {
        panic(err)
    }
    
    // This will try HTTPS first, then fall back to HTTP
    req, _ := http.NewRequest("GET", "http://internal-service:8080/api/health", nil)
    resp, err := client.Do(req)
    if err != nil {
        fmt.Printf("Request failed: %v\n", err)
        return
    }
    defer resp.Body.Close()
    
    fmt.Printf("Response status: %s\n", resp.Status)
    fmt.Printf("Used protocol: %s\n", resp.Request.URL.Scheme)
}
```

### Custom Configuration

```go
// Create a custom configuration for your specific needs
config := httputil.InternalNetworkClientConfig()

// Adjust settings as needed
config.Timeout = 60 * time.Second
config.AllowHTTPFallback = true
config.PreferHTTPS = true

client, err := httputil.NewHTTPClient(config)
if err != nil {
    panic(err)
}
```

### Using Options Pattern

```go
client, err := httputil.NewHTTPClientWithOptions(
    httputil.WithInternalNetworkMode(),
    httputil.WithTimeouts(30*time.Second, 10*time.Second, 10*time.Second),
    httputil.WithHTTPFallback(true),
)
```

## Configuration Options

### Internal Network Mode

```go
// Optimized for internal networks behind firewalls
config := httputil.InternalNetworkClientConfig()

// Features:
// - Allows HTTP fallback
// - Accepts self-signed certificates
// - Less strict SSL verification
// - Still tries HTTPS first
```

### Production Mode

```go
// Strict security for production environments
config := httputil.ProductionClientConfig()

// Features:
// - Requires TLS 1.3
// - No HTTP fallback
// - Strict certificate verification
// - Enhanced security
```

## How the HTTP Client Works

The client follows this logic:

1. **Convert to HTTPS**: If request URL is HTTP, convert to HTTPS
2. **Try HTTPS First**: Attempt the HTTPS request
3. **Success**: If HTTPS succeeds, return the response
4. **Fallback**: If HTTPS fails and fallback is enabled, try HTTP
5. **Error Handling**: Return appropriate errors with context

```go
// Example of protocol conversion
// Input:  http://service:80/api/users
// First:  https://service:443/api/users  (tries this first)
// Fallback: http://service:80/api/users  (if HTTPS fails)
```

## Security Considerations

### Internal Networks
- ✅ HTTP fallback allowed
- ✅ Self-signed certificates accepted
- ✅ TLS 1.2+ required
- ✅ Connection timeouts enforced

### Production Networks
- ❌ No HTTP fallback
- ❌ Only valid certificates
- ✅ TLS 1.3 required
- ✅ Stricter timeouts

## Example Use Cases

### Microservices Communication

```go
// Service-to-service calls in Kubernetes/Docker environments
func callUserService(userID string) (*User, error) {
    client, _ := httputil.NewHTTPClientForInternalNetwork()
    
    url := fmt.Sprintf("http://user-service/api/users/%s", userID)
    req, _ := http.NewRequest("GET", url, nil)
    
    resp, err := client.Do(req)
    if err != nil {
        return nil, fmt.Errorf("failed to call user service: %w", err)
    }
    defer resp.Body.Close()
    
    // Parse response...
    return user, nil
}
```

### Health Checks

```go
// Health check that works with both HTTP and HTTPS
func healthCheck(serviceURL string) error {
    client, _ := httputil.NewHTTPClient(
        httputil.InternalNetworkClientConfig(),
    )
    
    req, _ := http.NewRequest("GET", serviceURL+"/health", nil)
    resp, err := client.Do(req)
    if err != nil {
        return fmt.Errorf("health check failed: %w", err)
    }
    resp.Body.Close()
    
    if resp.StatusCode != 200 {
        return fmt.Errorf("service unhealthy: %s", resp.Status)
    }
    
    return nil
}
```

### API Gateway Integration

```go
// API gateway that routes to backend services
func proxyToBackend(w http.ResponseWriter, r *http.Request) {
    client, _ := httputil.NewHTTPClientForInternalNetwork()
    
    // Create backend request
    backendURL := "http://backend-service" + r.URL.Path
    req, _ := http.NewRequestWithContext(r.Context(), r.Method, backendURL, r.Body)
    
    // Copy headers
    for k, v := range r.Header {
        req.Header[k] = v
    }
    
    // Forward request
    resp, err := client.Do(req)
    if err != nil {
        http.Error(w, "Backend unavailable", 502)
        return
    }
    defer resp.Body.Close()
    
    // Copy response back to client
    // ... implementation
}
```

## Performance Optimization

### Connection Pooling

The client automatically configures connection pooling:

```go
// Default connection limits (optimized for internal networks)
MaxIdleConns:        100  // Total idle connections
MaxIdleConnsPerHost: 10   // Per-host idle connections  
MaxConnsPerHost:     10   // Max connections per host
IdleConnTimeout:     90s  // Keep connections alive
```

### Timeout Configuration

```go
// Default timeouts (balanced for reliability)
Timeout:             30s  // Overall request timeout
DialTimeout:         10s  // Connection establishment
TLSHandshakeTimeout: 10s  // SSL/TLS handshake
```

## Error Handling

The client provides clear error context:

```go
resp, err := client.Do(req)
if err != nil {
    // Check if this was an HTTPS failure that fell back to HTTP
    if strings.Contains(err.Error(), "HTTPS request failed") {
        log.Warn("HTTPS failed, using HTTP fallback")
    }
    
    // Handle other errors...
}
```

## Migration from Standard HTTP Client

### Before (Standard Client)

```go
client := &http.Client{
    Timeout: 30 * time.Second,
}
```

### After (Enhanced Client)

```go
client, _ := httputil.NewHTTPClientForInternalNetwork()
// Now handles HTTPS/HTTP intelligently with better security
```

## Best Practices

1. **Use Internal Network Mode** for services behind firewalls
2. **Set Appropriate Timeouts** based on your network latency
3. **Enable HTTP Fallback** only for internal networks
4. **Monitor Protocol Usage** to identify which services support HTTPS
5. **Upgrade to Production Mode** when moving to external networks
6. **Configure Connection Pooling** based on expected load

## Integration with Observability

The client works seamlessly with the observability middleware:

```go
// Combine with observability
provider, _ := observability.New(ctx, 
    observability.WithServiceName("api-gateway"),
)

middleware := observability.NewHTTPClientMiddleware(provider)
client, _ := httputil.NewHTTPClientForInternalNetwork()

// Wrap with observability
observableClient := &http.Client{
    Transport: middleware(client.httpsClient.Transport),
    Timeout:   client.httpsClient.Timeout,
}
```

This provides automatic tracing, metrics, and logging for both HTTPS and HTTP requests. 