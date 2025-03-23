# lib-commons

A comprehensive Go library providing common utilities and components for building robust microservices and applications in the Lerian Studio ecosystem.

## Overview

`lib-commons` is a utility library that provides a collection of reusable components and helpers for Go applications. It includes standardized implementations for database connections, message queuing, logging, context management, error handling, transaction processing, and more.

## Features

### Core Components

- **App Management**: Framework for managing application lifecycle and runtime (`app.go`)
- **Context Utilities**: Enhanced context management with support for logging, tracing, and header IDs (`context.go`)
- **Error Handling**: Standardized business error handling and responses (`errors.go`)

### Database Connectors

- **PostgreSQL**: Connection management, migrations, and utilities for PostgreSQL databases
- **MongoDB**: Connection management and utilities for MongoDB
- **Redis**: Client implementation and utilities for Redis

### Messaging

- **RabbitMQ**: Client implementation and utilities for RabbitMQ

### Observability

- **Logging**: Pluggable logging interface with multiple implementations
- **OpenTelemetry**: Integrated tracing, metrics, and logs through OpenTelemetry
- **Zap**: Integration with Uber's Zap logging library

### Utilities

- **String Utilities**: Common string manipulation functions
- **Type Conversion**: Safe type conversion utilities
- **Time Helpers**: Date and time manipulation functions
- **OS Utilities**: Operating system related utilities
- **Pointer Utilities**: Helper functions for pointer type operations
- **Transaction Processing**: Utilities for financial transaction processing and validation

## Getting Started

### Prerequisites

- Go 1.23.2 or higher

### Installation

```bash
go get github.com/LerianStudio/lib-commons
```

## API Reference

### Core Components

#### Application Management (`commons`)

| Method                           | Description                                              |
| -------------------------------- | -------------------------------------------------------- |
| `NewLauncher(...LauncherOption)` | Creates a new application launcher with provided options |
| `WithLogger(logger)`             | LauncherOption that adds a logger to the launcher        |
| `RunApp(name, app)`              | LauncherOption that registers an application to run      |
| `Launcher.Add(appName, app)`     | Registers an application to run                          |
| `Launcher.Run()`                 | Runs all registered applications in goroutines           |

#### Context Utilities (`commons`)

| Method                               | Description                     |
| ------------------------------------ | ------------------------------- |
| `ContextWithLogger(ctx, logger)`     | Returns a context with logger   |
| `NewLoggerFromContext(ctx)`          | Extracts logger from context    |
| `ContextWithTracer(ctx, tracer)`     | Returns a context with tracer   |
| `NewTracerFromContext(ctx)`          | Extracts tracer from context    |
| `ContextWithHeaderID(ctx, headerID)` | Returns a context with headerID |
| `NewHeaderIDFromContext(ctx)`        | Extracts headerID from context  |

#### Error Handling (`commons`)

| Method                                            | Description                                                                  |
| ------------------------------------------------- | ---------------------------------------------------------------------------- |
| `ValidateBusinessError(err, entityType, ...args)` | Maps domain errors to business responses with appropriate codes and messages |
| `Response.Error()`                                | Returns the error message from a Response                                    |

### Database Connectors

#### PostgreSQL (`commons/postgres`)

| Method                                      | Description                                              |
| ------------------------------------------- | -------------------------------------------------------- |
| `PostgresConnection.Connect()`              | Establishes connection to PostgreSQL primary and replica |
| `PostgresConnection.GetDB()`                | Returns the database connection                          |
| `PostgresConnection.MigrateUp(sourceDir)`   | Runs database migrations                                 |
| `PostgresConnection.MigrateDown(sourceDir)` | Reverts database migrations                              |
| `GetPagination(page, pageSize)`             | Gets pagination parameters for SQL queries               |

#### MongoDB (`commons/mongo`)

| Method                                | Description                       |
| ------------------------------------- | --------------------------------- |
| `MongoConnection.Connect()`           | Establishes connection to MongoDB |
| `MongoConnection.GetClient()`         | Returns the MongoDB client        |
| `MongoConnection.GetDatabase()`       | Returns the MongoDB database      |
| `MongoConnection.GetCollection(name)` | Returns a MongoDB collection      |

#### Redis (`commons/redis`)

| Method                                             | Description                     |
| -------------------------------------------------- | ------------------------------- |
| `RedisConnection.Connect()`                        | Establishes connection to Redis |
| `RedisConnection.GetClient()`                      | Returns the Redis client        |
| `RedisConnection.Set(ctx, key, value, expiration)` | Sets a key-value pair in Redis  |
| `RedisConnection.Get(ctx, key)`                    | Gets a value from Redis by key  |
| `RedisConnection.Del(ctx, keys...)`                | Deletes keys from Redis         |

### Messaging

#### RabbitMQ (`commons/rabbitmq`)

| Method                                                      | Description                        |
| ----------------------------------------------------------- | ---------------------------------- |
| `RabbitMQConnection.Connect()`                              | Establishes connection to RabbitMQ |
| `RabbitMQConnection.GetChannel()`                           | Returns a RabbitMQ channel         |
| `RabbitMQConnection.DeclareQueue(name)`                     | Declares a queue                   |
| `RabbitMQConnection.DeclareExchange(name, kind)`            | Declares an exchange               |
| `RabbitMQConnection.QueueBind(queue, exchange, routingKey)` | Binds a queue to an exchange       |
| `RabbitMQConnection.Publish(exchange, routingKey, body)`    | Publishes a message                |
| `RabbitMQConnection.Consume(queue, consumer)`               | Consumes messages from a queue     |

### Observability

#### Logging (`commons/log`)

| Method                                | Description                                    |
| ------------------------------------- | ---------------------------------------------- |
| `Info(args...)`                       | Logs info level message                        |
| `Infof(format, args...)`              | Logs formatted info level message              |
| `Error(args...)`                      | Logs error level message                       |
| `Errorf(format, args...)`             | Logs formatted error level message             |
| `Warn(args...)`                       | Logs warning level message                     |
| `Warnf(format, args...)`              | Logs formatted warning level message           |
| `Debug(args...)`                      | Logs debug level message                       |
| `Debugf(format, args...)`             | Logs formatted debug level message             |
| `Fatal(args...)`                      | Logs fatal level message and exits             |
| `Fatalf(format, args...)`             | Logs formatted fatal level message and exits   |
| `WithFields(fields...)`               | Returns a logger with additional fields        |
| `WithDefaultMessageTemplate(message)` | Returns a logger with default message template |
| `Sync()`                              | Flushes any buffered log entries               |

#### Zap Integration (`commons/zap`)

| Method                                   | Description                                 |
| ---------------------------------------- | ------------------------------------------- |
| `NewZapLogger(config)`                   | Creates a new Zap logger                    |
| `ZapLoggerAdapter.Info(args...)`         | Logs info level message using Zap           |
| `ZapLoggerAdapter.Error(args...)`        | Logs error level message using Zap          |
| `ZapLoggerAdapter.WithFields(fields...)` | Returns a Zap logger with additional fields |

#### OpenTelemetry (`commons/opentelemetry`)

| Method                                  | Description                                                     |
| --------------------------------------- | --------------------------------------------------------------- |
| `Telemetry.InitializeTelemetry(logger)` | Initializes OpenTelemetry with trace, metric, and log providers |
| `Telemetry.ShutdownTelemetry()`         | Shuts down OpenTelemetry providers                              |
| `Telemetry.GetTracer()`                 | Returns a tracer from the provider                              |
| `Telemetry.GetMeter()`                  | Returns a meter from the provider                               |
| `Telemetry.GetLogger()`                 | Returns a logger from the provider                              |
| `Telemetry.StartSpan(ctx, name)`        | Starts a new trace span                                         |
| `Telemetry.EndSpan(span, err)`          | Ends a trace span with optional error                           |

### Utilities

#### String Utilities (`commons`)

| Method                                | Description                              |
| ------------------------------------- | ---------------------------------------- |
| `IsNilOrEmpty(s)`                     | Checks if string pointer is nil or empty |
| `TruncateString(s, maxLen)`           | Truncates string to maximum length       |
| `MaskEmail(email)`                    | Masks email address for privacy          |
| `MaskLastDigits(value, digitsToShow)` | Masks all but last digits of a string    |
| `StringToObject(s, obj)`              | Converts JSON string to object           |
| `ObjectToString(obj)`                 | Converts object to JSON string           |

#### OS Utilities (`commons`)

| Method                  | Description                                  |
| ----------------------- | -------------------------------------------- |
| `GetEnv(key, fallback)` | Gets environment variable with fallback      |
| `MustGetEnv(key)`       | Gets required environment variable or panics |
| `LoadEnvFile(file)`     | Loads environment variables from file        |
| `GetMemUsage()`         | Gets current memory usage statistics         |
| `GetCPUUsage()`         | Gets current CPU usage statistics            |

#### Time Utilities (`commons`)

| Method                       | Description                          |
| ---------------------------- | ------------------------------------ |
| `FormatTime(t, layout)`      | Formats time according to layout     |
| `ParseTime(s, layout)`       | Parses time from string using layout |
| `GetCurrentTime()`           | Gets current time in UTC             |
| `TimeBetween(t, start, end)` | Checks if time is between two times  |

#### Pointer Utilities (`commons/pointers`)

| Method             | Description                                            |
| ------------------ | ------------------------------------------------------ |
| `ToString(s)`      | Creates string pointer from string                     |
| `ToInt(i)`         | Creates int pointer from int                           |
| `ToBool(b)`        | Creates bool pointer from bool                         |
| `FromStringPtr(s)` | Gets string from string pointer with safe nil handling |
| `FromIntPtr(i)`    | Gets int from int pointer with safe nil handling       |
| `FromBoolPtr(b)`   | Gets bool from bool pointer with safe nil handling     |

#### Transaction Processing (`commons/transaction`)

| Method                              | Description                                            |
| ----------------------------------- | ------------------------------------------------------ |
| `ValidateTransactionRequest(req)`   | Validates transaction request against business rules   |
| `ValidateAccountBalances(accounts)` | Validates account balances for transaction processing  |
| `ValidateAssetCode(code)`           | Validates asset code existence and status              |
| `ValidateAccountStatuses(accounts)` | Validates account statuses for transaction eligibility |

#### Shell Utilities (`commons/shell`)

| Method                                        | Description                               |
| --------------------------------------------- | ----------------------------------------- |
| `ExecuteCommand(command)`                     | Executes shell command and returns output |
| `ExecuteCommandWithTimeout(command, timeout)` | Executes shell command with timeout       |
| `ExecuteCommandInBackground(command)`         | Executes shell command in background      |

#### Network Utilities (`commons/net`)

| Method                   | Description                            |
| ------------------------ | -------------------------------------- |
| `ValidateURL(url)`       | Validates URL format and accessibility |
| `GetLocalIP()`           | Gets local IP address                  |
| `IsPortOpen(host, port)` | Checks if port is open on host         |
| `GetFreePort()`          | Gets a free port on local machine      |

## Contributing

Please read the contributing guidelines before submitting pull requests.

## License

This project is licensed under the terms found in the LICENSE file in the root directory.