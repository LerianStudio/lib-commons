# Changelog

All notable changes to lib-commons will be documented in this file.

## [v5.1.2] — 2026-05-30 (backport hotfix)

### Added (backport from v5.4.0)
- `commons.CurrentEnv()` / `commons.MustCurrentEnv()` — reads `ENVIRONMENT_NAME` (preferred) or `ENV_NAME` (also accepted), validates value is `staging` or `production`.
- `commons.EnvStaging`, `commons.EnvProduction` constants.
- `events.TenantEventsChannel(env)` — returns `"tenant-events:{env}:"`.
- `events.TenantEventsChannelPrefix` constant.

### Fixed (backport from v5.4.1)
- Multi-tenant event listener (`commons/tenant-manager/event.TenantEventListener`) now subscribes ONLY to the env-scoped channel `tenant-events:{env}:` (resolved via `commons.CurrentEnv()`). The previous wildcard `PSubscribe("tenant-events:*")` leaked events across environments. `Start()` now requires `ENVIRONMENT_NAME` (or `ENV_NAME`) to be set in the multi-tenant consumer process — apps that wire `NewTenantEventListener` but do not set the env var will fail to start. Single-tenant apps that never wire the listener are unaffected.

### Deprecated
- `event.SubscriptionPattern` (`"tenant-events:*"`) — retained for backward compatibility but no longer used by the listener. Use `events.TenantEventsChannel(commons.CurrentEnv())` instead. Subscribing to the legacy wildcard leaks events across environments.

### NOT backported (left in v5.4.x line)
- Security framework simplification (removal of `commons/security_override.go`, `SecurityTier`, `CheckSecurityRule`, etc.)
- Rate limit env var changes (`RATE_LIMIT_ENABLED` default flip, `ALLOW_RATELIMIT_DISABLED` removal)
- Other v5.2.x — v5.4.x improvements

Apps that want the security/ratelimit changes should bump to v5.4.1+.
