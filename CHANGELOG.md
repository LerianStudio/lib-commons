## [2.3.0-beta.7](https://github.com/LerianStudio/lib-commons/compare/v2.3.0-beta.6...v2.3.0-beta.7) (2025-09-10)


### Features

* **transaction:** implement balanceKey support in operations :sparkles: ([38ac489](https://github.com/LerianStudio/lib-commons/commit/38ac489a64c11810bf406d7a2141b4aed3ca6746))

## [2.3.0-beta.6](https://github.com/LerianStudio/lib-commons/compare/v2.3.0-beta.5...v2.3.0-beta.6) (2025-08-21)


### Features

* some adjusts; ([60b206a](https://github.com/LerianStudio/lib-commons/commit/60b206a8bf1c8a299648a5df09aea76191dbea0c))


### Bug Fixes

* go.mod and go.sum; ([cda49e7](https://github.com/LerianStudio/lib-commons/commit/cda49e7e7d7a9b5da91155c43bdb9966826a7f4c))
* make lint ([ec9fc3a](https://github.com/LerianStudio/lib-commons/commit/ec9fc3ac4c39996b2e5ce308032f269380df32ee))
* **cursor:** resolve first page prev_cursor bug and infinite loop issues; ([b0f8861](https://github.com/LerianStudio/lib-commons/commit/b0f8861c22521b6ec742a365560a439e28b866c4))
* **cursor:** resolve pagination logic errors and add comprehensive UUID v7 tests ([2d48453](https://github.com/LerianStudio/lib-commons/commit/2d4845332e94b8225e781b267eec9f405519a7f6))

## [2.3.0-beta.5](https://github.com/LerianStudio/lib-commons/compare/v2.3.0-beta.4...v2.3.0-beta.5) (2025-08-20)

## [2.3.0-beta.4](https://github.com/LerianStudio/lib-commons/compare/v2.3.0-beta.3...v2.3.0-beta.4) (2025-08-20)


### Features

* add telemetry and logging to transaction validation and gRPC middleware ([0aabecc](https://github.com/LerianStudio/lib-commons/commit/0aabeccb0a7bb2f50dfc3cf9544cfe6b2dcddf91))

## [2.3.0-beta.3](https://github.com/LerianStudio/lib-commons/compare/v2.3.0-beta.2...v2.3.0-beta.3) (2025-08-19)


### Bug Fixes

* Adjusting instance when telemetry is off. ([68504a7](https://github.com/LerianStudio/lib-commons/commit/68504a7080ce4f437a9f551ae4c259ed7c0daaa6))

## [2.3.0-beta.2](https://github.com/LerianStudio/lib-commons/compare/v2.3.0-beta.1...v2.3.0-beta.2) (2025-08-18)


### Features

* Adding the crypto package of encryption and decryption. ([f309c23](https://github.com/LerianStudio/lib-commons/commit/f309c233404a56ca1bd3f27e7a9a28bd839fac37))
* Adding the crypto package of encryption and decryption. ([577b746](https://github.com/LerianStudio/lib-commons/commit/577b746c0dfad3dc863027bbe6f5508b194f7578))

## [2.3.0-beta.1](https://github.com/LerianStudio/lib-commons/compare/v2.2.0...v2.3.0-beta.1) (2025-08-18)


### Bug Fixes

* **otel:** reorder shutdown sequence to ensure proper telemetry export and add span attributes from request params id ([44fc4c9](https://github.com/LerianStudio/lib-commons/commit/44fc4c996e2f322244965bb31c79e069719a1e1f))
* **http:** use HasPrefix instead of Contains for route exclusion matching ([9891eac](https://github.com/LerianStudio/lib-commons/commit/9891eacbd75dfce11ba57ebf2a6f38144dc04505))

## [2.2.0](https://github.com/LerianStudio/lib-commons/compare/v2.1.0...v2.2.0) (2025-08-08)


### Features

* add new field transaction date to be used to make past transactions; ([fcb4704](https://github.com/LerianStudio/lib-commons/commit/fcb47044c5b11d0da0eb53a75fc31f26ae6f7fb6))
* add span events, UUID conversion and configurable log obfuscation ([d92bb13](https://github.com/LerianStudio/lib-commons/commit/d92bb13aabeb0b49b30a4ed9161182d73aab300f))
* merge pull request [#182](https://github.com/LerianStudio/lib-commons/issues/182) from LerianStudio/feat/COMMONS-1155 ([931fdcb](https://github.com/LerianStudio/lib-commons/commit/931fdcb9c5cdeabf1602108db813855162b8e655))


### Bug Fixes

*  go get -u ./... && make tidy; ([a18914f](https://github.com/LerianStudio/lib-commons/commit/a18914fd032c639bf06732ccbd0c66eabd89753d))
* **otel:** add nil checks and remove unnecessary error handling in span methods ([3f9d468](https://github.com/LerianStudio/lib-commons/commit/3f9d46884dad366520eb1b95a5ee032a2992b959))

## [2.2.0-beta.4](https://github.com/LerianStudio/lib-commons/compare/v2.2.0-beta.3...v2.2.0-beta.4) (2025-08-08)

## [2.2.0-beta.3](https://github.com/LerianStudio/lib-commons/compare/v2.2.0-beta.2...v2.2.0-beta.3) (2025-08-08)

## [2.2.0-beta.2](https://github.com/LerianStudio/lib-commons/compare/v2.2.0-beta.1...v2.2.0-beta.2) (2025-08-08)


### Features

* add span events, UUID conversion and configurable log obfuscation ([d92bb13](https://github.com/LerianStudio/lib-commons/commit/d92bb13aabeb0b49b30a4ed9161182d73aab300f))


### Bug Fixes

* **otel:** add nil checks and remove unnecessary error handling in span methods ([3f9d468](https://github.com/LerianStudio/lib-commons/commit/3f9d46884dad366520eb1b95a5ee032a2992b959))

## [2.2.0-beta.1](https://github.com/LerianStudio/lib-commons/compare/v2.1.0...v2.2.0-beta.1) (2025-08-06)


### Features

* add new field transaction date to be used to make past transactions; ([fcb4704](https://github.com/LerianStudio/lib-commons/commit/fcb47044c5b11d0da0eb53a75fc31f26ae6f7fb6))
* merge pull request [#182](https://github.com/LerianStudio/lib-commons/issues/182) from LerianStudio/feat/COMMONS-1155 ([931fdcb](https://github.com/LerianStudio/lib-commons/commit/931fdcb9c5cdeabf1602108db813855162b8e655))


### Bug Fixes

*  go get -u ./... && make tidy; ([a18914f](https://github.com/LerianStudio/lib-commons/commit/a18914fd032c639bf06732ccbd0c66eabd89753d))

## [2.1.0](https://github.com/LerianStudio/lib-commons/compare/v2.0.0...v2.1.0) (2025-08-01)


### Bug Fixes

* add UTF-8 sanitization for span attributes and error handling improvements ([e69dae8](https://github.com/LerianStudio/lib-commons/commit/e69dae8728c7c2ae669c96e102a811febc45de14))

## [2.1.0-beta.2](https://github.com/LerianStudio/lib-commons/compare/v2.1.0-beta.1...v2.1.0-beta.2) (2025-08-01)

## [2.1.0-beta.1](https://github.com/LerianStudio/lib-commons/compare/v2.0.0...v2.1.0-beta.1) (2025-08-01)


### Bug Fixes

* add UTF-8 sanitization for span attributes and error handling improvements ([e69dae8](https://github.com/LerianStudio/lib-commons/commit/e69dae8728c7c2ae669c96e102a811febc45de14))

## [2.0.0](https://github.com/LerianStudio/lib-commons/compare/v1.18.0...v2.0.0) (2025-07-30)


### ⚠ BREAKING CHANGES

* change version and paths to v2

### Features

* **security:** add accesstoken and refreshtoken to sensitive fields list ([9e884c7](https://github.com/LerianStudio/lib-commons/commit/9e884c784e686c15354196fa09526371570f01e1))
* **security:** add accesstoken and refreshtoken to sensitive fields ([ede9b9b](https://github.com/LerianStudio/lib-commons/commit/ede9b9ba17b7f98ffe53a927d42cfb7b0f867f29))
* **telemetry:** add metrics factory with fluent API for counter, gauge and histogram metrics ([517352b](https://github.com/LerianStudio/lib-commons/commit/517352b95111de59613d9b2f15429c751302b779))
* **telemetry:** add request ID to HTTP span attributes ([3c60b29](https://github.com/LerianStudio/lib-commons/commit/3c60b29f9432c012219f0c08b1403594ea54069b))
* **telemetry:** add telemetry queue propagation ([610c702](https://github.com/LerianStudio/lib-commons/commit/610c702c3f927d08bcd3f5279caf99b75127dfd8))
* adjust internal keys on redis to use generic one; ([c0e4556](https://github.com/LerianStudio/lib-commons/commit/c0e45566040c9da35043601b8128b3792c43cb61))
* create a new balance internal key to lock balance on redis; ([715e2e7](https://github.com/LerianStudio/lib-commons/commit/715e2e72b47c681064fd83dcef89c053c1d33d1c))
* extract logger separator constant and enhance telemetry span attributes ([2f611bb](https://github.com/LerianStudio/lib-commons/commit/2f611bb808f4fb68860b9745490a3ffdf8ba37a9))
* **security:** implement sensitive field obfuscation for telemetry and logging ([b98bd60](https://github.com/LerianStudio/lib-commons/commit/b98bd604259823c733711ef552d23fb347a86956))
* Merge pull request [#166](https://github.com/LerianStudio/lib-commons/issues/166) from LerianStudio/feat/add-new-redis-key ([3199765](https://github.com/LerianStudio/lib-commons/commit/3199765d6832d8a068f8e925773ea44acce5291e))
* Merge pull request [#168](https://github.com/LerianStudio/lib-commons/issues/168) from LerianStudio/feat/COMMONS-redis-balance-key ([2b66484](https://github.com/LerianStudio/lib-commons/commit/2b66484703bb7551fbe5264cc8f20618fe61bd5b))
* merge pull request [#176](https://github.com/LerianStudio/lib-commons/issues/176) from LerianStudio/develop ([69fd3fa](https://github.com/LerianStudio/lib-commons/commit/69fd3face5ada8718fe290ac951e89720c253980))


### Bug Fixes

* Add NormalizeDateTime helper for date offset and time bounds formatting ([838c5f1](https://github.com/LerianStudio/lib-commons/commit/838c5f1940fd06c109ba9480f30781553e80ff45))
* Merge pull request [#164](https://github.com/LerianStudio/lib-commons/issues/164) from LerianStudio/fix/COMMONS-1111 ([295ca40](https://github.com/LerianStudio/lib-commons/commit/295ca4093e919513bfcf7a0de50108c9e5609eb2))
* remove commets; ([333fe49](https://github.com/LerianStudio/lib-commons/commit/333fe499e1a8a43654cd6c0f0546e3a1c5279bc9))


### Code Refactoring

* update module to v2 ([1c20f97](https://github.com/LerianStudio/lib-commons/commit/1c20f97279dd7ab0c59e447b4e1ffc1595077deb))

## [2.0.0-beta.1](https://github.com/LerianStudio/lib-commons/compare/v1.19.0-beta.11...v2.0.0-beta.1) (2025-07-30)


### ⚠ BREAKING CHANGES

* change version and paths to v2

### Features

* **security:** add accesstoken and refreshtoken to sensitive fields list ([9e884c7](https://github.com/LerianStudio/lib-commons/commit/9e884c784e686c15354196fa09526371570f01e1))


### Code Refactoring

* update module to v2 ([1c20f97](https://github.com/LerianStudio/lib-commons/commit/1c20f97279dd7ab0c59e447b4e1ffc1595077deb))

## [1.19.0-beta.11](https://github.com/LerianStudio/lib-commons/v2/compare/v1.19.0-beta.10...v1.19.0-beta.11) (2025-07-30)


### Features

* **telemetry:** add request ID to HTTP span attributes ([3c60b29](https://github.com/LerianStudio/lib-commons/v2/commit/3c60b29f9432c012219f0c08b1403594ea54069b))

## [1.19.0-beta.10](https://github.com/LerianStudio/lib-commons/v2/compare/v1.19.0-beta.9...v1.19.0-beta.10) (2025-07-30)


### Features

* **security:** add accesstoken and refreshtoken to sensitive fields ([ede9b9b](https://github.com/LerianStudio/lib-commons/v2/commit/ede9b9ba17b7f98ffe53a927d42cfb7b0f867f29))

## [1.19.0-beta.9](https://github.com/LerianStudio/lib-commons/v2/compare/v1.19.0-beta.8...v1.19.0-beta.9) (2025-07-30)

## [1.19.0-beta.8](https://github.com/LerianStudio/lib-commons/v2/compare/v1.19.0-beta.7...v1.19.0-beta.8) (2025-07-29)

## [1.19.0-beta.7](https://github.com/LerianStudio/lib-commons/v2/compare/v1.19.0-beta.6...v1.19.0-beta.7) (2025-07-29)


### Features

* extract logger separator constant and enhance telemetry span attributes ([2f611bb](https://github.com/LerianStudio/lib-commons/v2/commit/2f611bb808f4fb68860b9745490a3ffdf8ba37a9))

## [1.19.0-beta.6](https://github.com/LerianStudio/lib-commons/v2/compare/v1.19.0-beta.5...v1.19.0-beta.6) (2025-07-28)


### Features

* **telemetry:** add metrics factory with fluent API for counter, gauge and histogram metrics ([517352b](https://github.com/LerianStudio/lib-commons/v2/commit/517352b95111de59613d9b2f15429c751302b779))

## [1.19.0-beta.5](https://github.com/LerianStudio/lib-commons/v2/compare/v1.19.0-beta.4...v1.19.0-beta.5) (2025-07-28)


### Features

* adjust internal keys on redis to use generic one; ([c0e4556](https://github.com/LerianStudio/lib-commons/v2/commit/c0e45566040c9da35043601b8128b3792c43cb61))
* Merge pull request [#168](https://github.com/LerianStudio/lib-commons/v2/issues/168) from LerianStudio/feat/COMMONS-redis-balance-key ([2b66484](https://github.com/LerianStudio/lib-commons/v2/commit/2b66484703bb7551fbe5264cc8f20618fe61bd5b))

## [1.19.0-beta.4](https://github.com/LerianStudio/lib-commons/v2/compare/v1.19.0-beta.3...v1.19.0-beta.4) (2025-07-28)


### Features

* create a new balance internal key to lock balance on redis; ([715e2e7](https://github.com/LerianStudio/lib-commons/v2/commit/715e2e72b47c681064fd83dcef89c053c1d33d1c))
* Merge pull request [#166](https://github.com/LerianStudio/lib-commons/v2/issues/166) from LerianStudio/feat/add-new-redis-key ([3199765](https://github.com/LerianStudio/lib-commons/v2/commit/3199765d6832d8a068f8e925773ea44acce5291e))

## [1.19.0-beta.3](https://github.com/LerianStudio/lib-commons/v2/compare/v1.19.0-beta.2...v1.19.0-beta.3) (2025-07-25)


### Features

* **telemetry:** add telemetry queue propagation ([610c702](https://github.com/LerianStudio/lib-commons/v2/commit/610c702c3f927d08bcd3f5279caf99b75127dfd8))

## [1.19.0-beta.2](https://github.com/LerianStudio/lib-commons/v2/compare/v1.19.0-beta.1...v1.19.0-beta.2) (2025-07-25)


### Bug Fixes

* Add NormalizeDateTime helper for date offset and time bounds formatting ([838c5f1](https://github.com/LerianStudio/lib-commons/v2/commit/838c5f1940fd06c109ba9480f30781553e80ff45))
* Merge pull request [#164](https://github.com/LerianStudio/lib-commons/v2/issues/164) from LerianStudio/fix/COMMONS-1111 ([295ca40](https://github.com/LerianStudio/lib-commons/v2/commit/295ca4093e919513bfcf7a0de50108c9e5609eb2))
* remove commets; ([333fe49](https://github.com/LerianStudio/lib-commons/v2/commit/333fe499e1a8a43654cd6c0f0546e3a1c5279bc9))

## [1.19.0-beta.1](https://github.com/LerianStudio/lib-commons/v2/compare/v1.18.0...v1.19.0-beta.1) (2025-07-23)


### Features

* **security:** implement sensitive field obfuscation for telemetry and logging ([b98bd60](https://github.com/LerianStudio/lib-commons/v2/commit/b98bd604259823c733711ef552d23fb347a86956))

## [1.18.0](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0...v1.18.0) (2025-07-22)


### Features

* Improve Redis client configuration with UniversalOptions and connection pool tuning ([1587047](https://github.com/LerianStudio/lib-commons/v2/commit/158704738d1c823af6fbf3bc37f97d9e9734ed8e))
* Merge pull request [#159](https://github.com/LerianStudio/lib-commons/v2/issues/159) from LerianStudio/feat/COMMONS-REDIS-RETRY ([e279ae9](https://github.com/LerianStudio/lib-commons/v2/commit/e279ae92be1464100e7f11c236afa9df408834cb))
* Merge pull request [#162](https://github.com/LerianStudio/lib-commons/v2/issues/162) from LerianStudio/develop ([f0778f0](https://github.com/LerianStudio/lib-commons/v2/commit/f0778f040d2e0ec776a5e7ca796578b1a01bd869))


### Bug Fixes

* add on const magic numbers; ([ff4d39b](https://github.com/LerianStudio/lib-commons/v2/commit/ff4d39b9ae209ce83827d5ba8b73f1e54692caad))
* add redis values default; ([7fe8252](https://github.com/LerianStudio/lib-commons/v2/commit/7fe8252291623f0c148155c60e33e48c7e2722ec))
* add variables default config; ([3c0b0a8](https://github.com/LerianStudio/lib-commons/v2/commit/3c0b0a8d5a07979ed668885d9799fb5c1c60aa3b))
* change default values to regular size; ([42ff053](https://github.com/LerianStudio/lib-commons/v2/commit/42ff053d9545be847d7f6033c6e3afd8f4fd4bf0))
* remove alias concat on operation route assignment :bug: ([ddf7530](https://github.com/LerianStudio/lib-commons/v2/commit/ddf7530692f9e1121b986b1c4d7cc27022b22f24))

## [1.18.0-beta.4](https://github.com/LerianStudio/lib-commons/v2/compare/v1.18.0-beta.3...v1.18.0-beta.4) (2025-07-22)


### Bug Fixes

* add redis values default; ([7fe8252](https://github.com/LerianStudio/lib-commons/v2/commit/7fe8252291623f0c148155c60e33e48c7e2722ec))

## [1.18.0-beta.3](https://github.com/LerianStudio/lib-commons/v2/compare/v1.18.0-beta.2...v1.18.0-beta.3) (2025-07-22)


### Bug Fixes

* add variables default config; ([3c0b0a8](https://github.com/LerianStudio/lib-commons/v2/commit/3c0b0a8d5a07979ed668885d9799fb5c1c60aa3b))
* change default values to regular size; ([42ff053](https://github.com/LerianStudio/lib-commons/v2/commit/42ff053d9545be847d7f6033c6e3afd8f4fd4bf0))

## [1.18.0-beta.2](https://github.com/LerianStudio/lib-commons/v2/compare/v1.18.0-beta.1...v1.18.0-beta.2) (2025-07-22)


### Features

* Improve Redis client configuration with UniversalOptions and connection pool tuning ([1587047](https://github.com/LerianStudio/lib-commons/v2/commit/158704738d1c823af6fbf3bc37f97d9e9734ed8e))
* Merge pull request [#159](https://github.com/LerianStudio/lib-commons/v2/issues/159) from LerianStudio/feat/COMMONS-REDIS-RETRY ([e279ae9](https://github.com/LerianStudio/lib-commons/v2/commit/e279ae92be1464100e7f11c236afa9df408834cb))


### Bug Fixes

* add on const magic numbers; ([ff4d39b](https://github.com/LerianStudio/lib-commons/v2/commit/ff4d39b9ae209ce83827d5ba8b73f1e54692caad))

## [1.18.0-beta.1](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0...v1.18.0-beta.1) (2025-07-21)


### Bug Fixes

* remove alias concat on operation route assignment :bug: ([ddf7530](https://github.com/LerianStudio/lib-commons/v2/commit/ddf7530692f9e1121b986b1c4d7cc27022b22f24))

## [1.17.0](https://github.com/LerianStudio/lib-commons/v2/compare/v1.16.0...v1.17.0) (2025-07-17)


### Features

* **transaction:** add accounting routes to Responses struct :sparkles: ([5f36263](https://github.com/LerianStudio/lib-commons/v2/commit/5f36263e6036d5e993d17af7d846c10c9290e610))
* **utils:** add ExtractTokenFromHeader function to parse Authorization headers ([c91ea16](https://github.com/LerianStudio/lib-commons/v2/commit/c91ea16580bba21118a726c3ad0751752fe59e5b))
* **http:** add Fiber error handler with OpenTelemetry span management ([5c7deed](https://github.com/LerianStudio/lib-commons/v2/commit/5c7deed8216321edd0527b10bad220dde1492d2e))
* add gcp credentials to use passing by app like base64 string; ([326ff60](https://github.com/LerianStudio/lib-commons/v2/commit/326ff601e7eccbfd9aa7a31a54488cd68d8d2bbb))
* add new internal key generation functions for settings and accounting routes :sparkles: ([d328f29](https://github.com/LerianStudio/lib-commons/v2/commit/d328f29ef095c8ca2e3741744918da4761a1696f))
* add some refactors ([8cd3f91](https://github.com/LerianStudio/lib-commons/v2/commit/8cd3f915f3b136afe9d2365b36a3cc96934e1c52))
* add TTL support to Redis/Valkey and support cluster + sentinel modes alongside standalone ([1d825df](https://github.com/LerianStudio/lib-commons/v2/commit/1d825dfefbf574bfe3db0bc718b9d0876aec5e03))
* add variable tableAlias variadic to ApplyCursorPagination; ([1579a9e](https://github.com/LerianStudio/lib-commons/v2/commit/1579a9e25eae1da3247422ccd64e48730c59ba31))
* adjust to use only one host; ([22696b0](https://github.com/LerianStudio/lib-commons/v2/commit/22696b0f989eff5db22aeeff06d82df3b16230e4))
* change cacert to string to receive base64; ([a24f5f4](https://github.com/LerianStudio/lib-commons/v2/commit/a24f5f472686e39b44031e00fcc2b7989f1cf6b7))
* create a new const called x-idempotency-replayed; ([df9946c](https://github.com/LerianStudio/lib-commons/v2/commit/df9946c830586ed80577495cc653109b636b4575))
* **otel:** enhance trace context propagation with tracestate support for grpc ([f6f65ee](https://github.com/LerianStudio/lib-commons/v2/commit/f6f65eec7999c9bb4d6c14b2314c5c7e5d7f76ea))
* implements IAM refresh token; ([3d21e04](https://github.com/LerianStudio/lib-commons/v2/commit/3d21e04194a10710a1b9de46a3f3aba89804c8b8))
* Merge pull request [#118](https://github.com/LerianStudio/lib-commons/v2/issues/118) from LerianStudio/feat/COMMONS-52 ([e8f8917](https://github.com/LerianStudio/lib-commons/v2/commit/e8f8917b5c828c487f6bf2236b391dd4f8da5623))
* merge pull request [#120](https://github.com/LerianStudio/lib-commons/v2/issues/120) from LerianStudio/feat/COMMONS-52-2 ([4293e11](https://github.com/LerianStudio/lib-commons/v2/commit/4293e11ae36942afd7a376ab3ee3db3981922ebf))
* merge pull request [#124](https://github.com/LerianStudio/lib-commons/v2/issues/124) from LerianStudio/feat/COMMONS-52-6 ([8aaaf65](https://github.com/LerianStudio/lib-commons/v2/commit/8aaaf652e399746c67c0b8699c57f4a249271ef0))
* merge pull request [#127](https://github.com/LerianStudio/lib-commons/v2/issues/127) from LerianStudio/feat/COMMONS-52-9 ([12ee2a9](https://github.com/LerianStudio/lib-commons/v2/commit/12ee2a947d2fc38e8957b9b9f6e129b65e4b87a2))
* Merge pull request [#128](https://github.com/LerianStudio/lib-commons/v2/issues/128) from LerianStudio/feat/COMMONS-52-10 ([775f24a](https://github.com/LerianStudio/lib-commons/v2/commit/775f24ac85da8eb5e08a6e374ee61f327e798094))
* Merge pull request [#132](https://github.com/LerianStudio/lib-commons/v2/issues/132) from LerianStudio/feat/COMMOS-1023 ([e2cce46](https://github.com/LerianStudio/lib-commons/v2/commit/e2cce46b11ca9172f45769dae444de48e74e051f))
* Merge pull request [#152](https://github.com/LerianStudio/lib-commons/v2/issues/152) from LerianStudio/develop ([9e38ece](https://github.com/LerianStudio/lib-commons/v2/commit/9e38ece58cac8458cf3aed44bd2e210510424a61))
* merge pull request [#153](https://github.com/LerianStudio/lib-commons/v2/issues/153) from LerianStudio/feat/COMMONS-1055 ([1cc6cb5](https://github.com/LerianStudio/lib-commons/v2/commit/1cc6cb53c71515bd0c574ece0bb6335682aab953))
* Preallocate structures and isolate channels per goroutine for CalculateTotal ([8e92258](https://github.com/LerianStudio/lib-commons/v2/commit/8e922587f4b88f93434dfac5e16f0e570bef4a98))
* revert code that was on the main; ([c2f1772](https://github.com/LerianStudio/lib-commons/v2/commit/c2f17729bde8d2f5bbc36381173ad9226640d763))


### Bug Fixes

* .golangci.yml ([038bedd](https://github.com/LerianStudio/lib-commons/v2/commit/038beddbe9ed4a867f6ed93dd4e84480ed65bb1b))
* add fallback logging when logger is nil in shutdown handler ([800d644](https://github.com/LerianStudio/lib-commons/v2/commit/800d644d920bd54abf787d3be457cc0a1117c7a1))
* add new check channel is closed; ([e3956c4](https://github.com/LerianStudio/lib-commons/v2/commit/e3956c46eb8a87e637e035d7676d5c592001b509))
* adjust camel case time name; ([5ba77b9](https://github.com/LerianStudio/lib-commons/v2/commit/5ba77b958a0386a2ab9f8197503bbd4bd57235f0))
* adjust decimal values from remains and percentage; ([e1dc4b1](https://github.com/LerianStudio/lib-commons/v2/commit/e1dc4b183d0ca2d1247f727b81f8f27d4ddcc3c7))
* adjust redis key to use {} to calculate slot on cluster; ([318f269](https://github.com/LerianStudio/lib-commons/v2/commit/318f26947ee847aebfc600ed6e21cb903ee6a795))
* adjust some code and test; ([c6aca75](https://github.com/LerianStudio/lib-commons/v2/commit/c6aca756499e8b9875e1474e4f7949bb9cc9f60c))
* adjust to create tls on redis using variable; ([e78ae20](https://github.com/LerianStudio/lib-commons/v2/commit/e78ae2035b5583ce59654e3c7f145d93d86051e7))
* gitactions; ([7f9ebeb](https://github.com/LerianStudio/lib-commons/v2/commit/7f9ebeb1a9328a902e82c8c60428b2a8246793cf))
* go lint ([2499476](https://github.com/LerianStudio/lib-commons/v2/commit/249947604ed5d5382cd46e28e03c7396b9096d63))
* improve error handling and prevent deadlocks in server and license management ([24282ee](https://github.com/LerianStudio/lib-commons/v2/commit/24282ee9a411e0d5bf1977447a97e1e3fb260835))
* Merge pull request [#119](https://github.com/LerianStudio/lib-commons/v2/issues/119) from LerianStudio/feat/COMMONS-52 ([3ba9ca0](https://github.com/LerianStudio/lib-commons/v2/commit/3ba9ca0e284cf36797772967904d21947f8856a5))
* Merge pull request [#121](https://github.com/LerianStudio/lib-commons/v2/issues/121) from LerianStudio/feat/COMMONS-52-3 ([69c9e00](https://github.com/LerianStudio/lib-commons/v2/commit/69c9e002ab0a4fcd24622c79c5da7857eb22c922))
* Merge pull request [#122](https://github.com/LerianStudio/lib-commons/v2/issues/122) from LerianStudio/feat/COMMONS-52-4 ([46f5140](https://github.com/LerianStudio/lib-commons/v2/commit/46f51404f5f472172776abb1fbfd3bab908fc540))
* Merge pull request [#123](https://github.com/LerianStudio/lib-commons/v2/issues/123) from LerianStudio/fix/COMMONS-52-5 ([788915b](https://github.com/LerianStudio/lib-commons/v2/commit/788915b8c333156046e1d79860f80dc84f9aa08b))
* Merge pull request [#126](https://github.com/LerianStudio/lib-commons/v2/issues/126) from LerianStudio/fix-COMMONS-52-8 ([cfe9bbd](https://github.com/LerianStudio/lib-commons/v2/commit/cfe9bbde1bcf97847faf3fdc7e72e20ff723d586))
* rabbit hearthbeat and log type of client conn on redis/valkey; ([9607bf5](https://github.com/LerianStudio/lib-commons/v2/commit/9607bf5c0abf21603372d32ea8d66b5d34c77ec0))
* revert to original rabbit source; ([351c6ea](https://github.com/LerianStudio/lib-commons/v2/commit/351c6eac3e27301e4a65fce293032567bfd88807))
* **otel:** simplify resource creation to solve schema merging conflict ([318a38c](https://github.com/LerianStudio/lib-commons/v2/commit/318a38c07ca8c3bd6e2345c78302ad0c515d39a3))

## [1.17.0-beta.31](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.30...v1.17.0-beta.31) (2025-07-17)

## [1.17.0-beta.30](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.29...v1.17.0-beta.30) (2025-07-17)


### Bug Fixes

* improve error handling and prevent deadlocks in server and license management ([24282ee](https://github.com/LerianStudio/lib-commons/v2/commit/24282ee9a411e0d5bf1977447a97e1e3fb260835))

## [1.17.0-beta.29](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.28...v1.17.0-beta.29) (2025-07-16)


### Features

* merge pull request [#153](https://github.com/LerianStudio/lib-commons/v2/issues/153) from LerianStudio/feat/COMMONS-1055 ([1cc6cb5](https://github.com/LerianStudio/lib-commons/v2/commit/1cc6cb53c71515bd0c574ece0bb6335682aab953))
* Preallocate structures and isolate channels per goroutine for CalculateTotal ([8e92258](https://github.com/LerianStudio/lib-commons/v2/commit/8e922587f4b88f93434dfac5e16f0e570bef4a98))

## [1.17.0-beta.28](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.27...v1.17.0-beta.28) (2025-07-15)


### Features

* **http:** add Fiber error handler with OpenTelemetry span management ([5c7deed](https://github.com/LerianStudio/lib-commons/v2/commit/5c7deed8216321edd0527b10bad220dde1492d2e))

## [1.17.0-beta.27](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.26...v1.17.0-beta.27) (2025-07-15)

## [1.17.0-beta.26](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.25...v1.17.0-beta.26) (2025-07-15)

## [1.17.0-beta.25](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.24...v1.17.0-beta.25) (2025-07-11)


### Features

* **transaction:** add accounting routes to Responses struct :sparkles: ([5f36263](https://github.com/LerianStudio/lib-commons/v2/commit/5f36263e6036d5e993d17af7d846c10c9290e610))

## [1.17.0-beta.24](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.23...v1.17.0-beta.24) (2025-07-07)


### Bug Fixes

* **otel:** simplify resource creation to solve schema merging conflict ([318a38c](https://github.com/LerianStudio/lib-commons/v2/commit/318a38c07ca8c3bd6e2345c78302ad0c515d39a3))

## [1.17.0-beta.23](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.22...v1.17.0-beta.23) (2025-07-07)

## [1.17.0-beta.22](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.21...v1.17.0-beta.22) (2025-07-07)


### Features

* **otel:** enhance trace context propagation with tracestate support for grpc ([f6f65ee](https://github.com/LerianStudio/lib-commons/v2/commit/f6f65eec7999c9bb4d6c14b2314c5c7e5d7f76ea))

## [1.17.0-beta.21](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.20...v1.17.0-beta.21) (2025-07-02)


### Features

* **utils:** add ExtractTokenFromHeader function to parse Authorization headers ([c91ea16](https://github.com/LerianStudio/lib-commons/v2/commit/c91ea16580bba21118a726c3ad0751752fe59e5b))

## [1.17.0-beta.20](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.19...v1.17.0-beta.20) (2025-07-01)


### Features

* add new internal key generation functions for settings and accounting routes :sparkles: ([d328f29](https://github.com/LerianStudio/lib-commons/v2/commit/d328f29ef095c8ca2e3741744918da4761a1696f))

## [1.17.0-beta.19](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.18...v1.17.0-beta.19) (2025-06-30)


### Features

* create a new const called x-idempotency-replayed; ([df9946c](https://github.com/LerianStudio/lib-commons/v2/commit/df9946c830586ed80577495cc653109b636b4575))
* Merge pull request [#132](https://github.com/LerianStudio/lib-commons/v2/issues/132) from LerianStudio/feat/COMMOS-1023 ([e2cce46](https://github.com/LerianStudio/lib-commons/v2/commit/e2cce46b11ca9172f45769dae444de48e74e051f))

## [1.17.0-beta.18](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.17...v1.17.0-beta.18) (2025-06-27)

## [1.17.0-beta.17](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.16...v1.17.0-beta.17) (2025-06-27)

## [1.17.0-beta.16](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.15...v1.17.0-beta.16) (2025-06-26)


### Features

* add gcp credentials to use passing by app like base64 string; ([326ff60](https://github.com/LerianStudio/lib-commons/v2/commit/326ff601e7eccbfd9aa7a31a54488cd68d8d2bbb))

## [1.17.0-beta.15](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.14...v1.17.0-beta.15) (2025-06-25)


### Features

* add some refactors ([8cd3f91](https://github.com/LerianStudio/lib-commons/v2/commit/8cd3f915f3b136afe9d2365b36a3cc96934e1c52))
* Merge pull request [#128](https://github.com/LerianStudio/lib-commons/v2/issues/128) from LerianStudio/feat/COMMONS-52-10 ([775f24a](https://github.com/LerianStudio/lib-commons/v2/commit/775f24ac85da8eb5e08a6e374ee61f327e798094))

## [1.17.0-beta.14](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.13...v1.17.0-beta.14) (2025-06-25)


### Features

* change cacert to string to receive base64; ([a24f5f4](https://github.com/LerianStudio/lib-commons/v2/commit/a24f5f472686e39b44031e00fcc2b7989f1cf6b7))
* merge pull request [#127](https://github.com/LerianStudio/lib-commons/v2/issues/127) from LerianStudio/feat/COMMONS-52-9 ([12ee2a9](https://github.com/LerianStudio/lib-commons/v2/commit/12ee2a947d2fc38e8957b9b9f6e129b65e4b87a2))

## [1.17.0-beta.13](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.12...v1.17.0-beta.13) (2025-06-25)


### Bug Fixes

* Merge pull request [#126](https://github.com/LerianStudio/lib-commons/v2/issues/126) from LerianStudio/fix-COMMONS-52-8 ([cfe9bbd](https://github.com/LerianStudio/lib-commons/v2/commit/cfe9bbde1bcf97847faf3fdc7e72e20ff723d586))
* revert to original rabbit source; ([351c6ea](https://github.com/LerianStudio/lib-commons/v2/commit/351c6eac3e27301e4a65fce293032567bfd88807))

## [1.17.0-beta.12](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.11...v1.17.0-beta.12) (2025-06-25)


### Bug Fixes

* add new check channel is closed; ([e3956c4](https://github.com/LerianStudio/lib-commons/v2/commit/e3956c46eb8a87e637e035d7676d5c592001b509))

## [1.17.0-beta.11](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.10...v1.17.0-beta.11) (2025-06-25)


### Features

* merge pull request [#124](https://github.com/LerianStudio/lib-commons/v2/issues/124) from LerianStudio/feat/COMMONS-52-6 ([8aaaf65](https://github.com/LerianStudio/lib-commons/v2/commit/8aaaf652e399746c67c0b8699c57f4a249271ef0))


### Bug Fixes

* rabbit hearthbeat and log type of client conn on redis/valkey; ([9607bf5](https://github.com/LerianStudio/lib-commons/v2/commit/9607bf5c0abf21603372d32ea8d66b5d34c77ec0))

## [1.17.0-beta.10](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.9...v1.17.0-beta.10) (2025-06-24)


### Bug Fixes

* adjust camel case time name; ([5ba77b9](https://github.com/LerianStudio/lib-commons/v2/commit/5ba77b958a0386a2ab9f8197503bbd4bd57235f0))
* Merge pull request [#123](https://github.com/LerianStudio/lib-commons/v2/issues/123) from LerianStudio/fix/COMMONS-52-5 ([788915b](https://github.com/LerianStudio/lib-commons/v2/commit/788915b8c333156046e1d79860f80dc84f9aa08b))

## [1.17.0-beta.9](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.8...v1.17.0-beta.9) (2025-06-24)


### Bug Fixes

* adjust redis key to use {} to calculate slot on cluster; ([318f269](https://github.com/LerianStudio/lib-commons/v2/commit/318f26947ee847aebfc600ed6e21cb903ee6a795))
* Merge pull request [#122](https://github.com/LerianStudio/lib-commons/v2/issues/122) from LerianStudio/feat/COMMONS-52-4 ([46f5140](https://github.com/LerianStudio/lib-commons/v2/commit/46f51404f5f472172776abb1fbfd3bab908fc540))

## [1.17.0-beta.8](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.7...v1.17.0-beta.8) (2025-06-24)


### Features

* implements IAM refresh token; ([3d21e04](https://github.com/LerianStudio/lib-commons/v2/commit/3d21e04194a10710a1b9de46a3f3aba89804c8b8))


### Bug Fixes

* Merge pull request [#121](https://github.com/LerianStudio/lib-commons/v2/issues/121) from LerianStudio/feat/COMMONS-52-3 ([69c9e00](https://github.com/LerianStudio/lib-commons/v2/commit/69c9e002ab0a4fcd24622c79c5da7857eb22c922))

## [1.17.0-beta.7](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.6...v1.17.0-beta.7) (2025-06-24)


### Features

* merge pull request [#120](https://github.com/LerianStudio/lib-commons/v2/issues/120) from LerianStudio/feat/COMMONS-52-2 ([4293e11](https://github.com/LerianStudio/lib-commons/v2/commit/4293e11ae36942afd7a376ab3ee3db3981922ebf))


### Bug Fixes

* adjust to create tls on redis using variable; ([e78ae20](https://github.com/LerianStudio/lib-commons/v2/commit/e78ae2035b5583ce59654e3c7f145d93d86051e7))
* go lint ([2499476](https://github.com/LerianStudio/lib-commons/v2/commit/249947604ed5d5382cd46e28e03c7396b9096d63))

## [1.17.0-beta.6](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.5...v1.17.0-beta.6) (2025-06-23)


### Features

* adjust to use only one host; ([22696b0](https://github.com/LerianStudio/lib-commons/v2/commit/22696b0f989eff5db22aeeff06d82df3b16230e4))


### Bug Fixes

* Merge pull request [#119](https://github.com/LerianStudio/lib-commons/v2/issues/119) from LerianStudio/feat/COMMONS-52 ([3ba9ca0](https://github.com/LerianStudio/lib-commons/v2/commit/3ba9ca0e284cf36797772967904d21947f8856a5))

## [1.17.0-beta.5](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.4...v1.17.0-beta.5) (2025-06-23)


### Features

* add TTL support to Redis/Valkey and support cluster + sentinel modes alongside standalone ([1d825df](https://github.com/LerianStudio/lib-commons/v2/commit/1d825dfefbf574bfe3db0bc718b9d0876aec5e03))
* Merge pull request [#118](https://github.com/LerianStudio/lib-commons/v2/issues/118) from LerianStudio/feat/COMMONS-52 ([e8f8917](https://github.com/LerianStudio/lib-commons/v2/commit/e8f8917b5c828c487f6bf2236b391dd4f8da5623))


### Bug Fixes

* .golangci.yml ([038bedd](https://github.com/LerianStudio/lib-commons/v2/commit/038beddbe9ed4a867f6ed93dd4e84480ed65bb1b))
* gitactions; ([7f9ebeb](https://github.com/LerianStudio/lib-commons/v2/commit/7f9ebeb1a9328a902e82c8c60428b2a8246793cf))

## [1.17.0-beta.4](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.3...v1.17.0-beta.4) (2025-06-20)


### Bug Fixes

* adjust decimal values from remains and percentage; ([e1dc4b1](https://github.com/LerianStudio/lib-commons/v2/commit/e1dc4b183d0ca2d1247f727b81f8f27d4ddcc3c7))
* adjust some code and test; ([c6aca75](https://github.com/LerianStudio/lib-commons/v2/commit/c6aca756499e8b9875e1474e4f7949bb9cc9f60c))

## [1.17.0-beta.3](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.2...v1.17.0-beta.3) (2025-06-20)


### Bug Fixes

* add fallback logging when logger is nil in shutdown handler ([800d644](https://github.com/LerianStudio/lib-commons/v2/commit/800d644d920bd54abf787d3be457cc0a1117c7a1))

## [1.17.0-beta.2](https://github.com/LerianStudio/lib-commons/v2/compare/v1.17.0-beta.1...v1.17.0-beta.2) (2025-06-20)


### Features

* add variable tableAlias variadic to ApplyCursorPagination; ([1579a9e](https://github.com/LerianStudio/lib-commons/v2/commit/1579a9e25eae1da3247422ccd64e48730c59ba31))

## [1.17.0-beta.1](https://github.com/LerianStudio/lib-commons/v2/compare/v1.16.0...v1.17.0-beta.1) (2025-06-16)


### Features

* revert code that was on the main; ([c2f1772](https://github.com/LerianStudio/lib-commons/v2/commit/c2f17729bde8d2f5bbc36381173ad9226640d763))

## [1.12.0](https://github.com/LerianStudio/lib-commons/v2/compare/v1.11.0...v1.12.0) (2025-06-13)


### Features

* add log test; ([7ad741f](https://github.com/LerianStudio/lib-commons/v2/commit/7ad741f558e7a725e95dab257500d5d24b2536e5))
* add shutdown test ([9d5fb77](https://github.com/LerianStudio/lib-commons/v2/commit/9d5fb77893e10a708136767eda3f9bac99363ba4))


### Bug Fixes

* Add integer overflow protection to transaction operations; :bug: ([32904de](https://github.com/LerianStudio/lib-commons/v2/commit/32904def9bee6388f12a6e2cc997c20a594db696))
* add url for health check to read. from envs; update testes; update go mod and go sum; ([e9b8333](https://github.com/LerianStudio/lib-commons/v2/commit/e9b83330834c7c2949dfb05a4dc46f4786cd509d))
* create redis test; ([3178547](https://github.com/LerianStudio/lib-commons/v2/commit/317854731e550d222713503eecbdf26e2c26fa90))

## [1.12.0-beta.1](https://github.com/LerianStudio/lib-commons/v2/compare/v1.11.0...v1.12.0-beta.1) (2025-06-13)


### Features

* add log test; ([7ad741f](https://github.com/LerianStudio/lib-commons/v2/commit/7ad741f558e7a725e95dab257500d5d24b2536e5))
* add shutdown test ([9d5fb77](https://github.com/LerianStudio/lib-commons/v2/commit/9d5fb77893e10a708136767eda3f9bac99363ba4))


### Bug Fixes

* Add integer overflow protection to transaction operations; :bug: ([32904de](https://github.com/LerianStudio/lib-commons/v2/commit/32904def9bee6388f12a6e2cc997c20a594db696))
* add url for health check to read. from envs; update testes; update go mod and go sum; ([e9b8333](https://github.com/LerianStudio/lib-commons/v2/commit/e9b83330834c7c2949dfb05a4dc46f4786cd509d))
* create redis test; ([3178547](https://github.com/LerianStudio/lib-commons/v2/commit/317854731e550d222713503eecbdf26e2c26fa90))

## [1.12.0](https://github.com/LerianStudio/lib-commons/v2/compare/v1.11.0...v1.12.0) (2025-06-13)


### Features

* add log test; ([7ad741f](https://github.com/LerianStudio/lib-commons/v2/commit/7ad741f558e7a725e95dab257500d5d24b2536e5))


### Bug Fixes

* Add integer overflow protection to transaction operations; :bug: ([32904de](https://github.com/LerianStudio/lib-commons/v2/commit/32904def9bee6388f12a6e2cc997c20a594db696))
* add url for health check to read. from envs; update testes; update go mod and go sum; ([e9b8333](https://github.com/LerianStudio/lib-commons/v2/commit/e9b83330834c7c2949dfb05a4dc46f4786cd509d))
* create redis test; ([3178547](https://github.com/LerianStudio/lib-commons/v2/commit/317854731e550d222713503eecbdf26e2c26fa90))

## [1.12.0-beta.1](https://github.com/LerianStudio/lib-commons/v2/compare/v1.11.0...v1.12.0-beta.1) (2025-06-13)


### Features

* add log test; ([7ad741f](https://github.com/LerianStudio/lib-commons/v2/commit/7ad741f558e7a725e95dab257500d5d24b2536e5))


### Bug Fixes

* Add integer overflow protection to transaction operations; :bug: ([32904de](https://github.com/LerianStudio/lib-commons/v2/commit/32904def9bee6388f12a6e2cc997c20a594db696))
* add url for health check to read. from envs; update testes; update go mod and go sum; ([e9b8333](https://github.com/LerianStudio/lib-commons/v2/commit/e9b83330834c7c2949dfb05a4dc46f4786cd509d))
* create redis test; ([3178547](https://github.com/LerianStudio/lib-commons/v2/commit/317854731e550d222713503eecbdf26e2c26fa90))

## [1.12.0-beta.1](https://github.com/LerianStudio/lib-commons/v2/compare/v1.11.0...v1.12.0-beta.1) (2025-06-13)


### Bug Fixes

* Add integer overflow protection to transaction operations; :bug: ([32904de](https://github.com/LerianStudio/lib-commons/v2/commit/32904def9bee6388f12a6e2cc997c20a594db696))
* add url for health check to read. from envs; update testes; update go mod and go sum; ([e9b8333](https://github.com/LerianStudio/lib-commons/v2/commit/e9b83330834c7c2949dfb05a4dc46f4786cd509d))
* create redis test; ([3178547](https://github.com/LerianStudio/lib-commons/v2/commit/317854731e550d222713503eecbdf26e2c26fa90))

## [1.12.0](https://github.com/LerianStudio/lib-commons/v2/compare/v1.11.0...v1.12.0) (2025-06-13)


### Bug Fixes

* Add integer overflow protection to transaction operations; :bug: ([32904de](https://github.com/LerianStudio/lib-commons/v2/commit/32904def9bee6388f12a6e2cc997c20a594db696))
* add url for health check to read. from envs; update testes; update go mod and go sum; ([e9b8333](https://github.com/LerianStudio/lib-commons/v2/commit/e9b83330834c7c2949dfb05a4dc46f4786cd509d))

## [1.12.0-beta.1](https://github.com/LerianStudio/lib-commons/v2/compare/v1.11.0...v1.12.0-beta.1) (2025-06-13)


### Bug Fixes

* Add integer overflow protection to transaction operations; :bug: ([32904de](https://github.com/LerianStudio/lib-commons/v2/commit/32904def9bee6388f12a6e2cc997c20a594db696))
* add url for health check to read. from envs; update testes; update go mod and go sum; ([e9b8333](https://github.com/LerianStudio/lib-commons/v2/commit/e9b83330834c7c2949dfb05a4dc46f4786cd509d))

## [1.11.0](https://github.com/LerianStudio/lib-commons/v2/compare/v1.10.0...v1.11.0) (2025-05-19)


### Features

* add info and debug log levels to zap logger initializer by env name ([c132299](https://github.com/LerianStudio/lib-commons/v2/commit/c13229910647081facf9f555e4b4efa74aff60ec))
* add start app with graceful shutdown module ([21d9697](https://github.com/LerianStudio/lib-commons/v2/commit/21d9697c35686e82adbf3f41744ce25c369119ce))
* bump lib-license-go version to v1.0.8 ([4d93834](https://github.com/LerianStudio/lib-commons/v2/commit/4d93834af0dd4d4d48564b98f9d2dc766369c1be))
* move license shutdown to the end of execution and add recover from panic in graceful shutdown ([6cf1171](https://github.com/LerianStudio/lib-commons/v2/commit/6cf117159cc10b3fa97200c53fbb6a058566c7d6))


### Bug Fixes

* fix lint - remove cuddled if blocks ([cd6424b](https://github.com/LerianStudio/lib-commons/v2/commit/cd6424b741811ec119a2bf35189760070883b993))
* import corret lib license go uri ([f55338f](https://github.com/LerianStudio/lib-commons/v2/commit/f55338fa2c9ed1d974ab61f28b1c70101b35eb61))

## [1.11.0-beta.2](https://github.com/LerianStudio/lib-commons/v2/compare/v1.11.0-beta.1...v1.11.0-beta.2) (2025-05-19)


### Features

* add start app with graceful shutdown module ([21d9697](https://github.com/LerianStudio/lib-commons/v2/commit/21d9697c35686e82adbf3f41744ce25c369119ce))
* bump lib-license-go version to v1.0.8 ([4d93834](https://github.com/LerianStudio/lib-commons/v2/commit/4d93834af0dd4d4d48564b98f9d2dc766369c1be))
* move license shutdown to the end of execution and add recover from panic in graceful shutdown ([6cf1171](https://github.com/LerianStudio/lib-commons/v2/commit/6cf117159cc10b3fa97200c53fbb6a058566c7d6))


### Bug Fixes

* fix lint - remove cuddled if blocks ([cd6424b](https://github.com/LerianStudio/lib-commons/v2/commit/cd6424b741811ec119a2bf35189760070883b993))
* import corret lib license go uri ([f55338f](https://github.com/LerianStudio/lib-commons/v2/commit/f55338fa2c9ed1d974ab61f28b1c70101b35eb61))

## [1.11.0-beta.1](https://github.com/LerianStudio/lib-commons/v2/compare/v1.10.0...v1.11.0-beta.1) (2025-05-19)


### Features

* add info and debug log levels to zap logger initializer by env name ([c132299](https://github.com/LerianStudio/lib-commons/v2/commit/c13229910647081facf9f555e4b4efa74aff60ec))

## [1.10.0](https://github.com/LerianStudio/lib-commons/v2/compare/v1.9.0...v1.10.0) (2025-05-14)


### Features

* **postgres:** sets migrations path from environment variable :sparkles: ([7f9d40e](https://github.com/LerianStudio/lib-commons/v2/commit/7f9d40e88a9e9b94a8d6076121e73324421bd6e8))

## [1.10.0-beta.1](https://github.com/LerianStudio/lib-commons/v2/compare/v1.9.0...v1.10.0-beta.1) (2025-05-14)


### Features

* **postgres:** sets migrations path from environment variable :sparkles: ([7f9d40e](https://github.com/LerianStudio/lib-commons/v2/commit/7f9d40e88a9e9b94a8d6076121e73324421bd6e8))

## [1.9.0](https://github.com/LerianStudio/lib-commons/v2/compare/v1.8.0...v1.9.0) (2025-05-14)


### Bug Fixes

* add check if account is empty using accountAlias; :bug: ([d2054d8](https://github.com/LerianStudio/lib-commons/v2/commit/d2054d8e0924accd15cfcac95ef1be6e58abae93))
* **transaction:** add index variable to loop iteration ([e2974f0](https://github.com/LerianStudio/lib-commons/v2/commit/e2974f0c2cc87f39417bf42943e143188c3f9fc8))
* final adjust to use multiple identical accounts; :bug: ([b2165de](https://github.com/LerianStudio/lib-commons/v2/commit/b2165de3642c9c9949cda25d370cad9358e5f5be))
* **transaction:** improve validation in send source and distribute calculations ([625f2f9](https://github.com/LerianStudio/lib-commons/v2/commit/625f2f9598a61dbb4227722f605e1d4798a9a881))
* **transaction:** improve validation in send source and distribute calculations ([2b05323](https://github.com/LerianStudio/lib-commons/v2/commit/2b05323b81eea70278dbb2326423dedaf5078373))
* **transaction:** improve validation in send source and distribute calculations ([4a8f3f5](https://github.com/LerianStudio/lib-commons/v2/commit/4a8f3f59da5563842e0785732ad5b05989f62fb7))
* **transaction:** improve validation in send source and distribute calculations ([1cf5b04](https://github.com/LerianStudio/lib-commons/v2/commit/1cf5b04fb510594c5d13989c137cc8401ea2e23d))
* **transaction:** optimize balance operations in UpdateBalances function ([524fe97](https://github.com/LerianStudio/lib-commons/v2/commit/524fe975d125742d10920236e055db879809b01e))
* **transaction:** optimize balance operations in UpdateBalances function ([63201dd](https://github.com/LerianStudio/lib-commons/v2/commit/63201ddeb00835d8b8b9269f8a32850e4f28374e))
* **transaction:** optimize balance operations in UpdateBalances function ([8b6397d](https://github.com/LerianStudio/lib-commons/v2/commit/8b6397df3261cc0f5af190c69b16a55e215952ed))
* some more adjusts; :bug: ([af69b44](https://github.com/LerianStudio/lib-commons/v2/commit/af69b447658b0f4dfcd2e2f252dd2d0d68753094))

## [1.9.0-beta.8](https://github.com/LerianStudio/lib-commons/v2/compare/v1.9.0-beta.7...v1.9.0-beta.8) (2025-05-14)


### Bug Fixes

* final adjust to use multiple identical accounts; :bug: ([b2165de](https://github.com/LerianStudio/lib-commons/v2/commit/b2165de3642c9c9949cda25d370cad9358e5f5be))

## [1.9.0-beta.7](https://github.com/LerianStudio/lib-commons/v2/compare/v1.9.0-beta.6...v1.9.0-beta.7) (2025-05-13)


### Bug Fixes

* add check if account is empty using accountAlias; :bug: ([d2054d8](https://github.com/LerianStudio/lib-commons/v2/commit/d2054d8e0924accd15cfcac95ef1be6e58abae93))
* some more adjusts; :bug: ([af69b44](https://github.com/LerianStudio/lib-commons/v2/commit/af69b447658b0f4dfcd2e2f252dd2d0d68753094))

## [1.9.0-beta.6](https://github.com/LerianStudio/lib-commons/v2/compare/v1.9.0-beta.5...v1.9.0-beta.6) (2025-05-12)


### Bug Fixes

* **transaction:** optimize balance operations in UpdateBalances function ([524fe97](https://github.com/LerianStudio/lib-commons/v2/commit/524fe975d125742d10920236e055db879809b01e))
* **transaction:** optimize balance operations in UpdateBalances function ([63201dd](https://github.com/LerianStudio/lib-commons/v2/commit/63201ddeb00835d8b8b9269f8a32850e4f28374e))

## [1.9.0-beta.5](https://github.com/LerianStudio/lib-commons/v2/compare/v1.9.0-beta.4...v1.9.0-beta.5) (2025-05-12)


### Bug Fixes

* **transaction:** optimize balance operations in UpdateBalances function ([8b6397d](https://github.com/LerianStudio/lib-commons/v2/commit/8b6397df3261cc0f5af190c69b16a55e215952ed))

## [1.9.0-beta.4](https://github.com/LerianStudio/lib-commons/v2/compare/v1.9.0-beta.3...v1.9.0-beta.4) (2025-05-09)


### Bug Fixes

* **transaction:** add index variable to loop iteration ([e2974f0](https://github.com/LerianStudio/lib-commons/v2/commit/e2974f0c2cc87f39417bf42943e143188c3f9fc8))

## [1.9.0-beta.3](https://github.com/LerianStudio/lib-commons/v2/compare/v1.9.0-beta.2...v1.9.0-beta.3) (2025-05-09)


### Bug Fixes

* **transaction:** improve validation in send source and distribute calculations ([625f2f9](https://github.com/LerianStudio/lib-commons/v2/commit/625f2f9598a61dbb4227722f605e1d4798a9a881))

## [1.9.0-beta.2](https://github.com/LerianStudio/lib-commons/v2/compare/v1.9.0-beta.1...v1.9.0-beta.2) (2025-05-09)


### Bug Fixes

* **transaction:** improve validation in send source and distribute calculations ([2b05323](https://github.com/LerianStudio/lib-commons/v2/commit/2b05323b81eea70278dbb2326423dedaf5078373))

## [1.9.0-beta.1](https://github.com/LerianStudio/lib-commons/v2/compare/v1.8.0...v1.9.0-beta.1) (2025-05-09)


### Bug Fixes

* **transaction:** improve validation in send source and distribute calculations ([4a8f3f5](https://github.com/LerianStudio/lib-commons/v2/commit/4a8f3f59da5563842e0785732ad5b05989f62fb7))
* **transaction:** improve validation in send source and distribute calculations ([1cf5b04](https://github.com/LerianStudio/lib-commons/v2/commit/1cf5b04fb510594c5d13989c137cc8401ea2e23d))

## [1.8.0](https://github.com/LerianStudio/lib-commons/v2/compare/v1.7.0...v1.8.0) (2025-04-24)


### Features

* update go mod and go sum and change method health visibility; :sparkles: ([355991f](https://github.com/LerianStudio/lib-commons/v2/commit/355991f4416722ee51356139ed3c4fe08e1fe47e))

## [1.8.0-beta.1](https://github.com/LerianStudio/lib-commons/v2/compare/v1.7.0...v1.8.0-beta.1) (2025-04-24)


### Features

* update go mod and go sum and change method health visibility; :sparkles: ([355991f](https://github.com/LerianStudio/lib-commons/v2/commit/355991f4416722ee51356139ed3c4fe08e1fe47e))

## [1.7.0](https://github.com/LerianStudio/lib-commons/v2/compare/v1.6.0...v1.7.0) (2025-04-16)


### Bug Fixes

* fix lint cuddled code ([dcbf7c6](https://github.com/LerianStudio/lib-commons/v2/commit/dcbf7c6f26f379cec9790e14b76ee2e6868fb142))
* lint complexity over 31 in getBodyObfuscatedString ([0f9eb4a](https://github.com/LerianStudio/lib-commons/v2/commit/0f9eb4a82a544204119500db09d38fd6ec003c7e))
* obfuscate password field in the body before logging ([e35bfa3](https://github.com/LerianStudio/lib-commons/v2/commit/e35bfa36424caae3f90b351ed979d2c6e6e143f5))

## [1.7.0-beta.3](https://github.com/LerianStudio/lib-commons/v2/compare/v1.7.0-beta.2...v1.7.0-beta.3) (2025-04-16)


### Bug Fixes

* lint complexity over 31 in getBodyObfuscatedString ([0f9eb4a](https://github.com/LerianStudio/lib-commons/v2/commit/0f9eb4a82a544204119500db09d38fd6ec003c7e))

## [1.7.0-beta.2](https://github.com/LerianStudio/lib-commons/v2/compare/v1.7.0-beta.1...v1.7.0-beta.2) (2025-04-16)

## [1.7.0-beta.1](https://github.com/LerianStudio/lib-commons/v2/compare/v1.6.0...v1.7.0-beta.1) (2025-04-16)


### Bug Fixes

* fix lint cuddled code ([dcbf7c6](https://github.com/LerianStudio/lib-commons/v2/commit/dcbf7c6f26f379cec9790e14b76ee2e6868fb142))
* obfuscate password field in the body before logging ([e35bfa3](https://github.com/LerianStudio/lib-commons/v2/commit/e35bfa36424caae3f90b351ed979d2c6e6e143f5))

## [1.6.0](https://github.com/LerianStudio/lib-commons/v2/compare/v1.5.0...v1.6.0) (2025-04-11)


### Bug Fixes

* **transaction:** correct percentage calculation in CalculateTotal ([02b939c](https://github.com/LerianStudio/lib-commons/v2/commit/02b939c3abf1834de2078c2d0ae40b4fd9095bca))

## [1.6.0-beta.1](https://github.com/LerianStudio/lib-commons/v2/compare/v1.5.0...v1.6.0-beta.1) (2025-04-11)


### Bug Fixes

* **transaction:** correct percentage calculation in CalculateTotal ([02b939c](https://github.com/LerianStudio/lib-commons/v2/commit/02b939c3abf1834de2078c2d0ae40b4fd9095bca))

## [1.5.0](https://github.com/LerianStudio/lib-commons/v2/compare/v1.4.0...v1.5.0) (2025-04-10)


### Features

* adding accountAlias field to keep backward compatibility ([81bf528](https://github.com/LerianStudio/lib-commons/v2/commit/81bf528dfa8ceb5055714589745c1d3987cfa6da))

## [1.5.0-beta.1](https://github.com/LerianStudio/lib-commons/v2/compare/v1.4.0...v1.5.0-beta.1) (2025-04-09)


### Features

* adding accountAlias field to keep backward compatibility ([81bf528](https://github.com/LerianStudio/lib-commons/v2/commit/81bf528dfa8ceb5055714589745c1d3987cfa6da))

## [1.4.0](https://github.com/LerianStudio/lib-commons/v2/compare/v1.3.0...v1.4.0) (2025-04-08)

## [1.4.0-beta.1](https://github.com/LerianStudio/lib-commons/v2/compare/v1.3.1-beta.1...v1.4.0-beta.1) (2025-04-08)

## [1.3.1-beta.1](https://github.com/LerianStudio/lib-commons/v2/compare/v1.3.0...v1.3.1-beta.1) (2025-04-08)

## [1.3.0](https://github.com/LerianStudio/lib-commons/v2/compare/v1.2.0...v1.3.0) (2025-04-08)

## [1.3.0-beta.1](https://github.com/LerianStudio/lib-commons/v2/compare/v1.2.0...v1.3.0-beta.1) (2025-04-08)

## [1.2.0](https://github.com/LerianStudio/lib-commons/v2/compare/v1.1.0...v1.2.0) (2025-04-03)


### Bug Fixes

* update safe uint convertion to convert int instead of int64 ([a85628b](https://github.com/LerianStudio/lib-commons/v2/commit/a85628bb031d64d542b378180c2254c198e9ae59))
* update safe uint convertion to convert max int to uint first to validate ([c7dee02](https://github.com/LerianStudio/lib-commons/v2/commit/c7dee026532f42712eabdb3fde0c8d2b8ec7cdd8))

## [1.2.0-beta.1](https://github.com/LerianStudio/lib-commons/v2/compare/v1.1.0...v1.2.0-beta.1) (2025-04-03)


### Bug Fixes

* update safe uint convertion to convert int instead of int64 ([a85628b](https://github.com/LerianStudio/lib-commons/v2/commit/a85628bb031d64d542b378180c2254c198e9ae59))
* update safe uint convertion to convert max int to uint first to validate ([c7dee02](https://github.com/LerianStudio/lib-commons/v2/commit/c7dee026532f42712eabdb3fde0c8d2b8ec7cdd8))

## [1.1.0](https://github.com/LerianStudio/lib-commons/v2/compare/v1.0.0...v1.1.0) (2025-04-03)


### Features

* add safe uint convertion ([0d9e405](https://github.com/LerianStudio/lib-commons/v2/commit/0d9e4052ebbd70b18508d68906296c35b881d85e))
* organize golangci-lint module ([8d71f3b](https://github.com/LerianStudio/lib-commons/v2/commit/8d71f3bb2079457617a5ff8a8290492fd885b30d))


### Bug Fixes

* golang lint fixed version to v1.64.8; go mod and sum update packages; :bug: ([6b825c1](https://github.com/LerianStudio/lib-commons/v2/commit/6b825c1a0162326df2abb93b128419f2ea9a4175))

## [1.1.0-beta.3](https://github.com/LerianStudio/lib-commons/v2/compare/v1.1.0-beta.2...v1.1.0-beta.3) (2025-04-03)


### Features

* add safe uint convertion ([0d9e405](https://github.com/LerianStudio/lib-commons/v2/commit/0d9e4052ebbd70b18508d68906296c35b881d85e))

## [1.1.0-beta.2](https://github.com/LerianStudio/lib-commons/v2/compare/v1.1.0-beta.1...v1.1.0-beta.2) (2025-03-27)


### Features

* organize golangci-lint module ([8d71f3b](https://github.com/LerianStudio/lib-commons/v2/commit/8d71f3bb2079457617a5ff8a8290492fd885b30d))

## [1.1.0-beta.1](https://github.com/LerianStudio/lib-commons/v2/compare/v1.0.0...v1.1.0-beta.1) (2025-03-25)


### Bug Fixes

* golang lint fixed version to v1.64.8; go mod and sum update packages; :bug: ([6b825c1](https://github.com/LerianStudio/lib-commons/v2/commit/6b825c1a0162326df2abb93b128419f2ea9a4175))

## 1.0.0 (2025-03-19)


### Features

* add transaction validations to the lib-commons; :sparkles: ([098b730](https://github.com/LerianStudio/lib-commons/v2/commit/098b730fa1686b2f683faec69fabd6aa1607cf0b))
* initial commit to lib commons; ([7d49924](https://github.com/LerianStudio/lib-commons/v2/commit/7d4992494a1328fd1c0afc4f5814fa5c63cb0f9c))
* initiate new implements from lib-commons; ([18dff5c](https://github.com/LerianStudio/lib-commons/v2/commit/18dff5cbde19bd2659368ce5665a01f79119e7ef))


### Bug Fixes

* remove midaz reference; :bug: ([27cbdaa](https://github.com/LerianStudio/lib-commons/v2/commit/27cbdaa5ad103edf903fb24d2b652e7e9f15d909))
* remove wrong tests; :bug: ([9f9d30f](https://github.com/LerianStudio/lib-commons/v2/commit/9f9d30f0d783ab3f9f4f6e7141981e3b266ba600))
* update message withBasicAuth.go ([d1dcdbc](https://github.com/LerianStudio/lib-commons/v2/commit/d1dcdbc7dfd4ef829b94de19db71e273452be425))
* update some places and adjust golint; :bug: ([db18dbb](https://github.com/LerianStudio/lib-commons/v2/commit/db18dbb7270675e87c150f3216ac9be1b2610c1c))
* update to return err instead of nil; :bug: ([8aade18](https://github.com/LerianStudio/lib-commons/v2/commit/8aade18d65bf6fe0d4e925f3bf178c51672fd7f4))
* update to use one response json objetc; :bug: ([2e42859](https://github.com/LerianStudio/lib-commons/v2/commit/2e428598b1f41f9c2de369a34510c5ed2ba21569))

## [1.0.0-beta.2](https://github.com/LerianStudio/lib-commons/v2/compare/v1.0.0-beta.1...v1.0.0-beta.2) (2025-03-19)


### Features

* add transaction validations to the lib-commons; :sparkles: ([098b730](https://github.com/LerianStudio/lib-commons/v2/commit/098b730fa1686b2f683faec69fabd6aa1607cf0b))


### Bug Fixes

* update some places and adjust golint; :bug: ([db18dbb](https://github.com/LerianStudio/lib-commons/v2/commit/db18dbb7270675e87c150f3216ac9be1b2610c1c))
* update to use one response json objetc; :bug: ([2e42859](https://github.com/LerianStudio/lib-commons/v2/commit/2e428598b1f41f9c2de369a34510c5ed2ba21569))

## 1.0.0-beta.1 (2025-03-18)


### Features

* initial commit to lib commons; ([7d49924](https://github.com/LerianStudio/lib-commons/v2/commit/7d4992494a1328fd1c0afc4f5814fa5c63cb0f9c))
* initiate new implements from lib-commons; ([18dff5c](https://github.com/LerianStudio/lib-commons/v2/commit/18dff5cbde19bd2659368ce5665a01f79119e7ef))


### Bug Fixes

* remove midaz reference; :bug: ([27cbdaa](https://github.com/LerianStudio/lib-commons/v2/commit/27cbdaa5ad103edf903fb24d2b652e7e9f15d909))
* remove wrong tests; :bug: ([9f9d30f](https://github.com/LerianStudio/lib-commons/v2/commit/9f9d30f0d783ab3f9f4f6e7141981e3b266ba600))
* update message withBasicAuth.go ([d1dcdbc](https://github.com/LerianStudio/lib-commons/v2/commit/d1dcdbc7dfd4ef829b94de19db71e273452be425))
* update to return err instead of nil; :bug: ([8aade18](https://github.com/LerianStudio/lib-commons/v2/commit/8aade18d65bf6fe0d4e925f3bf178c51672fd7f4))

## 1.0.0 (2025-03-06)


### Features

* configuration of CI/CD ([1bb1c4c](https://github.com/LerianStudio/lib-boilerplate/commit/1bb1c4ca0659e593ff22b3b5bf919163366301a7))
* set configuration of boilerplate ([138a60c](https://github.com/LerianStudio/lib-boilerplate/commit/138a60c7947a9e82e4808fa16cc53975e27e7de5))

## 1.0.0-beta.1 (2025-03-06)


### Features

* configuration of CI/CD ([1bb1c4c](https://github.com/LerianStudio/lib-boilerplate/commit/1bb1c4ca0659e593ff22b3b5bf919163366301a7))
* set configuration of boilerplate ([138a60c](https://github.com/LerianStudio/lib-boilerplate/commit/138a60c7947a9e82e4808fa16cc53975e27e7de5))
