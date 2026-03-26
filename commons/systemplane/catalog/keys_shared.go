// Copyright 2025 Lerian Studio.

package catalog

import "github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"

var corsKeys = []SharedKey{
	{Key: "cors.allowed_origins", EnvVar: "CORS_ALLOWED_ORIGINS", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true, Component: domain.ComponentNone, Group: "cors", Description: "CORS allowed origins (comma-separated)"},
	{Key: "cors.allowed_methods", EnvVar: "CORS_ALLOWED_METHODS", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true, Component: domain.ComponentNone, Group: "cors", Description: "CORS allowed methods (comma-separated)"},
	{Key: "cors.allowed_headers", EnvVar: "CORS_ALLOWED_HEADERS", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true, Component: domain.ComponentNone, Group: "cors", Description: "CORS allowed headers (comma-separated)"},
}

var appServerKeys = []SharedKey{
	{Key: "app.env_name", EnvVar: "ENV_NAME", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBootstrapOnly, MutableAtRuntime: false, Component: domain.ComponentNone, Group: "app", Description: "Environment name"},
	{Key: "app.log_level", EnvVar: "LOG_LEVEL", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true, Component: domain.ComponentNone, Group: "app", Description: "Application log level"},
	{Key: "server.address", EnvVar: "SERVER_ADDRESS", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBootstrapOnly, MutableAtRuntime: false, Component: domain.ComponentNone, Group: "server", Description: "HTTP server listen address"},
	{Key: "server.body_limit_bytes", EnvVar: "HTTP_BODY_LIMIT_BYTES", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyBootstrapOnly, MutableAtRuntime: false, Component: domain.ComponentNone, Group: "server", Description: "HTTP request body size limit in bytes"},
	{Key: "server.tls_cert_file", EnvVar: "SERVER_TLS_CERT_FILE", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBootstrapOnly, MutableAtRuntime: false, Component: domain.ComponentNone, Group: "server.tls", Description: "TLS certificate file path"},
	{Key: "server.tls_key_file", EnvVar: "SERVER_TLS_KEY_FILE", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBootstrapOnly, MutableAtRuntime: false, Component: domain.ComponentNone, Group: "server.tls", Description: "TLS key file path"},
}

var rateLimitKeys = []SharedKey{
	{Key: "rate_limit.enabled", EnvVar: "RATE_LIMIT_ENABLED", ValueType: domain.ValueTypeBool, ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true, Component: domain.ComponentNone, Group: "rate_limit", Description: "Enable rate limiting"},
	{Key: "rate_limit.max", EnvVar: "RATE_LIMIT_MAX", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true, Component: domain.ComponentNone, Group: "rate_limit", Description: "Maximum requests per window"},
	{Key: "rate_limit.expiry_sec", EnvVar: "RATE_LIMIT_EXPIRY_SEC", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true, Component: domain.ComponentNone, Group: "rate_limit", Description: "Rate limit window duration in seconds"},
}

// AuthKeys defines canonical auth keys.
// EnvVar is intentionally empty because products and plugins use different
// environment variable conventions for the same canonical systemplane keys.
var authKeys = []SharedKey{
	{Key: "auth.enabled", EnvVar: "", MatchEnvVars: []string{"AUTH_ENABLED", "PLUGIN_AUTH_ENABLED"}, ValueType: domain.ValueTypeBool, ApplyBehavior: domain.ApplyBootstrapOnly, MutableAtRuntime: false, Component: domain.ComponentNone, Group: "auth", Description: "Enable authentication middleware"},
	{Key: "auth.address", EnvVar: "", MatchEnvVars: []string{"AUTH_ADDRESS", "PLUGIN_AUTH_ADDRESS"}, ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBootstrapOnly, MutableAtRuntime: false, Component: domain.ComponentNone, Group: "auth", Description: "Auth service address"},
	{Key: "auth.client_id", EnvVar: "", MatchEnvVars: []string{"AUTH_CLIENT_ID", "PLUGIN_AUTH_CLIENT_ID"}, ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBootstrapOnly, MutableAtRuntime: false, Component: domain.ComponentNone, Group: "auth", Description: "Auth client ID"},
	{Key: "auth.client_secret", EnvVar: "", MatchEnvVars: []string{"AUTH_CLIENT_SECRET", "PLUGIN_AUTH_CLIENT_SECRET"}, ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBootstrapOnly, MutableAtRuntime: false, Component: domain.ComponentNone, Group: "auth", Secret: true, RedactPolicy: domain.RedactFull, Description: "Auth client secret"},
	{Key: "auth.cache_ttl_sec", EnvVar: "", MatchEnvVars: []string{"AUTH_CACHE_TTL_SEC"}, ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyBootstrapOnly, MutableAtRuntime: false, Component: domain.ComponentNone, Group: "auth", Description: "Auth cache TTL in seconds"},
}

var telemetryKeys = []SharedKey{
	{Key: "telemetry.enabled", EnvVar: "ENABLE_TELEMETRY", ValueType: domain.ValueTypeBool, ApplyBehavior: domain.ApplyBootstrapOnly, MutableAtRuntime: false, Component: domain.ComponentNone, Group: "telemetry", Description: "Enable OpenTelemetry"},
	{Key: "telemetry.service_name", EnvVar: "OTEL_RESOURCE_SERVICE_NAME", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBootstrapOnly, MutableAtRuntime: false, Component: domain.ComponentNone, Group: "telemetry", Description: "OTEL service name"},
	{Key: "telemetry.library_name", EnvVar: "OTEL_LIBRARY_NAME", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBootstrapOnly, MutableAtRuntime: false, Component: domain.ComponentNone, Group: "telemetry", Description: "OTEL library name"},
	{Key: "telemetry.service_version", EnvVar: "OTEL_RESOURCE_SERVICE_VERSION", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBootstrapOnly, MutableAtRuntime: false, Component: domain.ComponentNone, Group: "telemetry", Description: "OTEL service version"},
	{Key: "telemetry.deployment_env", EnvVar: "OTEL_RESOURCE_DEPLOYMENT_ENVIRONMENT", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBootstrapOnly, MutableAtRuntime: false, Component: domain.ComponentNone, Group: "telemetry", Description: "OTEL deployment environment"},
	{Key: "telemetry.collector_endpoint", EnvVar: "OTEL_EXPORTER_OTLP_ENDPOINT", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBootstrapOnly, MutableAtRuntime: false, Component: domain.ComponentNone, Group: "telemetry", Description: "OTEL collector endpoint"},
}

var rabbitMQKeys = []SharedKey{
	{Key: "rabbitmq.enabled", EnvVar: "RABBITMQ_ENABLED", ValueType: domain.ValueTypeBool, ApplyBehavior: domain.ApplyBundleRebuildAndReconcile, MutableAtRuntime: true, Component: "rabbitmq", Group: "rabbitmq", Description: "Enable RabbitMQ integration"},
	{Key: "rabbitmq.url", EnvVar: "RABBITMQ_URL", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "rabbitmq", Group: "rabbitmq.connection", Secret: true, RedactPolicy: domain.RedactFull, Description: "RabbitMQ connection URL"},
	{Key: "rabbitmq.exchange", EnvVar: "RABBITMQ_EXCHANGE", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyBundleRebuild, MutableAtRuntime: true, Component: "rabbitmq", Group: "rabbitmq.connection", Description: "RabbitMQ exchange name"},
	// LiveRead messaging params
	{Key: "rabbitmq.routing_key_prefix", EnvVar: "RABBITMQ_ROUTING_KEY_PREFIX", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true, Component: domain.ComponentNone, Group: "rabbitmq.messaging", Description: "RabbitMQ routing key prefix"},
	{Key: "rabbitmq.publish_timeout_ms", EnvVar: "RABBITMQ_PUBLISH_TIMEOUT_MS", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true, Component: domain.ComponentNone, Group: "rabbitmq.messaging", Description: "RabbitMQ publish timeout in milliseconds"},
	{Key: "rabbitmq.max_retries", EnvVar: "RABBITMQ_MAX_RETRIES", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true, Component: domain.ComponentNone, Group: "rabbitmq.messaging", Description: "RabbitMQ maximum publish retries"},
	{Key: "rabbitmq.retry_backoff_ms", EnvVar: "RABBITMQ_RETRY_BACKOFF_MS", ValueType: domain.ValueTypeInt, ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true, Component: domain.ComponentNone, Group: "rabbitmq.messaging", Description: "RabbitMQ retry backoff in milliseconds"},
	{Key: "rabbitmq.event_signing_secret", EnvVar: "RABBITMQ_EVENT_SIGNING_SECRET", ValueType: domain.ValueTypeString, ApplyBehavior: domain.ApplyLiveRead, MutableAtRuntime: true, Component: domain.ComponentNone, Group: "rabbitmq.messaging", Secret: true, RedactPolicy: domain.RedactFull, Description: "RabbitMQ event signing secret"},
}

// CORSKeys returns canonical keys for CORS configuration.
func CORSKeys() []SharedKey {
	return cloneSharedKeys(corsKeys)
}

// AppServerKeys returns canonical keys for application and HTTP server config.
func AppServerKeys() []SharedKey {
	return cloneSharedKeys(appServerKeys)
}

// RateLimitKeys returns canonical keys for rate limiting.
func RateLimitKeys() []SharedKey {
	return cloneSharedKeys(rateLimitKeys)
}

// AuthKeys returns canonical auth keys.
func AuthKeys() []SharedKey {
	return cloneSharedKeys(authKeys)
}

// TelemetryKeys returns canonical keys for OpenTelemetry configuration.
func TelemetryKeys() []SharedKey {
	return cloneSharedKeys(telemetryKeys)
}

// RabbitMQKeys returns canonical keys for RabbitMQ configuration.
func RabbitMQKeys() []SharedKey {
	return cloneSharedKeys(rabbitMQKeys)
}

// AllSharedKeys returns all canonical keys from all categories.
func AllSharedKeys() []SharedKey {
	all := make([]SharedKey, 0,
		len(postgresKeys)+
			len(redisKeys)+
			len(corsKeys)+
			len(appServerKeys)+
			len(rateLimitKeys)+
			len(authKeys)+
			len(telemetryKeys)+
			len(rabbitMQKeys),
	)

	all = append(all, postgresKeys...)
	all = append(all, redisKeys...)
	all = append(all, corsKeys...)
	all = append(all, appServerKeys...)
	all = append(all, rateLimitKeys...)
	all = append(all, authKeys...)
	all = append(all, telemetryKeys...)
	all = append(all, rabbitMQKeys...)

	return cloneSharedKeys(all)
}
