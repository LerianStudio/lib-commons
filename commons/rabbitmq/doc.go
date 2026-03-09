// Package rabbitmq provides AMQP connection, consumer, and producer helpers.
//
// It includes safer connection-string error sanitization and health-check helpers,
// a confirmable publisher abstraction with broker-ack waiting and auto-recovery
// (serialized publish+confirm per publisher instance for deterministic confirms),
// and DLQ topology declaration helpers.
//
// Health-check security defaults:
//   - Basic auth over plain HTTP is rejected unless AllowInsecureHealthCheck=true.
//   - Basic-auth health checks require HealthCheckAllowedHosts unless
//     AllowInsecureHealthCheck=true. Hosts can be derived automatically from
//     AMQP connection settings when explicit allowlist entries are not set.
//   - Health-check host restrictions can be enforced with HealthCheckAllowedHosts
//     (entries may be host, host:port, or CIDR) and RequireHealthCheckAllowedHosts.
//   - When basic auth is not used and no explicit allowlist is configured,
//     compatibility mode keeps host validation permissive by default.
package rabbitmq
