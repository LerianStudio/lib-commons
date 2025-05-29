# Midaz Commons-Go Refactoring Examples

This directory contains practical examples of how current implementations in Midaz components can be refactored to leverage the improved commons-go library patterns. The goal is to provide concrete, actionable guidance for teams to modernize their codebases.

## 🎯 Purpose

- **Showcase Real Refactoring Opportunities**: Examples from actual core, CRM, fees, and smart-templates code
- **Demonstrate Commons-Go Benefits**: Before/after comparisons showing reliability, observability, and performance improvements  
- **Provide Migration Guidance**: Step-by-step instructions for safe refactoring
- **Prevent Code Duplication**: Identify shared patterns that can use commons-go instead of custom implementations

## 📁 Directory Structure

### 🏗️ Components
Component-specific refactoring examples from actual Midaz services:

- [`components/core/`](./components/core/) - Core service refactoring examples
- [`components/crm/`](./components/crm/) - CRM plugin refactoring examples  
- [`components/fees/`](./components/fees/) - Fees plugin refactoring examples
- [`components/smart-templates/`](./components/smart-templates/) - Smart templates refactoring examples

### 🔧 Patterns  
Pattern-based examples showcasing commons-go improvements:

- [`patterns/database/`](./patterns/database/) - Database connection and error handling patterns
  - PostgreSQL Fatal calls → proper error handling
  - MongoDB connection improvements
  - Connection pooling and health checks
- [`patterns/http/`](./patterns/http/) - HTTP client and middleware patterns
  - Retry with jitter implementation
  - Circuit breaker integration  
  - Observability middleware
- [`patterns/observability/`](./patterns/observability/) - Monitoring and tracing patterns
- [`patterns/messaging/`](./patterns/messaging/) - RabbitMQ and messaging patterns

### 🚀 Migrations
Step-by-step migration guides:

- [`migrations/postgres-migration-guide.md`](./migrations/postgres-migration-guide.md) - How to migrate PostgreSQL code
- [`migrations/mongo-migration-guide.md`](./migrations/mongo-migration-guide.md) - How to migrate MongoDB code
- [`migrations/http-client-migration-guide.md`](./migrations/http-client-migration-guide.md) - How to migrate HTTP clients

## 🔥 High-Impact Refactoring Examples

### 1. **Critical: Database Fatal Calls** 
**Impact**: 🚨 **Application Crashes Prevention**
- **Found in**: Accounting, Smart-Templates plugins
- **Issue**: `Logger.Fatal()` calls crash applications on database errors
- **Solution**: Use commons-go postgres patterns with proper error handling
- **Example**: [`patterns/database/postgres-fatal-to-error.md`](./patterns/database/postgres-fatal-to-error.md)

### 2. **High: HTTP Client Reliability**
**Impact**: 🎯 **Improved Resilience**  
- **Found in**: All services making HTTP calls
- **Issue**: No retry logic, circuit breakers, or proper timeouts
- **Solution**: Use commons-go HTTP client with jitter and circuit breaking
- **Example**: [`patterns/http/retry-with-jitter.md`](./patterns/http/retry-with-jitter.md)

### 3. **Medium: MongoDB Connection Patterns**
**Impact**: 📊 **Better Performance & Monitoring**
- **Found in**: Fees plugin
- **Issue**: Basic connection patterns without health checks
- **Solution**: Use commons-go mongo patterns with connection pooling
- **Example**: [`patterns/database/mongo-connection-improvements.md`](./patterns/database/mongo-connection-improvements.md)

## 📊 Refactoring Benefits Summary

| Pattern                  | Before                | After                             | Benefits                                                                |
| ------------------------ | --------------------- | --------------------------------- | ----------------------------------------------------------------------- |
| **Database Connections** | Fatal calls crash app | Graceful error handling           | ✅ No crashes<br/>✅ Better error visibility<br/>✅ Graceful degradation   |
| **HTTP Clients**         | Basic http.Client     | Commons-go HTTP with retry/jitter | ✅ Automatic retries<br/>✅ Circuit breaking<br/>✅ Better observability   |
| **MongoDB**              | Basic mongo.Connect   | Commons-go mongo patterns         | ✅ Connection pooling<br/>✅ Health checks<br/>✅ Better error handling    |
| **Observability**        | Custom logging        | Commons-go observability          | ✅ Structured logging<br/>✅ Distributed tracing<br/>✅ Metrics collection |

## 🎓 How to Use This Guide

1. **Find Your Component**: Start with your specific component directory
2. **Identify Patterns**: Look for similar patterns in the patterns/ directory  
3. **Follow Migration Guide**: Use step-by-step migration instructions
4. **Validate Changes**: Use provided test examples to ensure correctness

## 🤝 Contributing Examples

When adding new refactoring examples:

1. **Show Real Code**: Use actual examples from Midaz components
2. **Before/After Format**: Always show current code vs improved code
3. **Explain Benefits**: Clearly state what improvements are gained
4. **Provide Migration Steps**: Include practical refactoring instructions
5. **Add Tests**: Show how to test the refactored code

## 📚 Additional Resources

- [Commons-Go Documentation](../../README.md)
- [Database Patterns](../../database/)
- [HTTP Patterns](../../http/)
- [Observability Patterns](../../observability/)

---

**💡 Pro Tip**: Start with high-impact, low-risk refactoring like database Fatal calls, then progressively adopt more advanced patterns like circuit breakers and distributed tracing. 