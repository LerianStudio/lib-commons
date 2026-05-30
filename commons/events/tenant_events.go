// Package events provides constants and helpers for inter-service event
// channels used by Lerian Go services.
package events

// TenantEventsChannelPrefix is the base prefix for tenant lifecycle event
// channels. The full channel name is composed as
// TenantEventsChannelPrefix + env + ":".
const TenantEventsChannelPrefix = "tenant-events:"

// TenantEventsChannel returns the env-scoped Valkey pub/sub channel for
// tenant lifecycle events. Pair with commons.CurrentEnv() at subscriber
// boot:
//
//	env, err := commons.CurrentEnv()
//	if err != nil { return err }
//	channel := events.TenantEventsChannel(env)  // "tenant-events:staging:"
//	pubsub := redisClient.Subscribe(ctx, channel)
//
// The returned channel name has a trailing colon. Do NOT append anything to
// it without explicit coordination with the publisher (tenant-manager).
//
// This function does NOT validate the env value. Callers MUST validate
// upstream via commons.CurrentEnv() to ensure the channel name is well-formed.
func TenantEventsChannel(env string) string {
	return TenantEventsChannelPrefix + env + ":"
}
