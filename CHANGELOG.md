## [1.17.0-beta.23](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.22...v1.17.0-beta.23) (2025-07-07)

## [1.17.0-beta.22](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.21...v1.17.0-beta.22) (2025-07-07)


### Features

* **otel:** enhance trace context propagation with tracestate support for grpc ([f6f65ee](https://github.com/LerianStudio/lib-commons/commit/f6f65eec7999c9bb4d6c14b2314c5c7e5d7f76ea))

## [1.17.0-beta.21](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.20...v1.17.0-beta.21) (2025-07-02)


### Features

* **utils:** add ExtractTokenFromHeader function to parse Authorization headers ([c91ea16](https://github.com/LerianStudio/lib-commons/commit/c91ea16580bba21118a726c3ad0751752fe59e5b))

## [1.17.0-beta.20](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.19...v1.17.0-beta.20) (2025-07-01)


### Features

* add new internal key generation functions for settings and accounting routes :sparkles: ([d328f29](https://github.com/LerianStudio/lib-commons/commit/d328f29ef095c8ca2e3741744918da4761a1696f))

## [1.17.0-beta.19](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.18...v1.17.0-beta.19) (2025-06-30)


### Features

* create a new const called x-idempotency-replayed; ([df9946c](https://github.com/LerianStudio/lib-commons/commit/df9946c830586ed80577495cc653109b636b4575))
* Merge pull request [#132](https://github.com/LerianStudio/lib-commons/issues/132) from LerianStudio/feat/COMMOS-1023 ([e2cce46](https://github.com/LerianStudio/lib-commons/commit/e2cce46b11ca9172f45769dae444de48e74e051f))

## [1.17.0-beta.18](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.17...v1.17.0-beta.18) (2025-06-27)

## [1.17.0-beta.17](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.16...v1.17.0-beta.17) (2025-06-27)

## [1.17.0-beta.16](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.15...v1.17.0-beta.16) (2025-06-26)


### Features

* add gcp credentials to use passing by app like base64 string; ([326ff60](https://github.com/LerianStudio/lib-commons/commit/326ff601e7eccbfd9aa7a31a54488cd68d8d2bbb))

## [1.17.0-beta.15](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.14...v1.17.0-beta.15) (2025-06-25)


### Features

* add some refactors ([8cd3f91](https://github.com/LerianStudio/lib-commons/commit/8cd3f915f3b136afe9d2365b36a3cc96934e1c52))
* Merge pull request [#128](https://github.com/LerianStudio/lib-commons/issues/128) from LerianStudio/feat/COMMONS-52-10 ([775f24a](https://github.com/LerianStudio/lib-commons/commit/775f24ac85da8eb5e08a6e374ee61f327e798094))

## [1.17.0-beta.14](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.13...v1.17.0-beta.14) (2025-06-25)


### Features

* change cacert to string to receive base64; ([a24f5f4](https://github.com/LerianStudio/lib-commons/commit/a24f5f472686e39b44031e00fcc2b7989f1cf6b7))
* merge pull request [#127](https://github.com/LerianStudio/lib-commons/issues/127) from LerianStudio/feat/COMMONS-52-9 ([12ee2a9](https://github.com/LerianStudio/lib-commons/commit/12ee2a947d2fc38e8957b9b9f6e129b65e4b87a2))

## [1.17.0-beta.13](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.12...v1.17.0-beta.13) (2025-06-25)


### Bug Fixes

* Merge pull request [#126](https://github.com/LerianStudio/lib-commons/issues/126) from LerianStudio/fix-COMMONS-52-8 ([cfe9bbd](https://github.com/LerianStudio/lib-commons/commit/cfe9bbde1bcf97847faf3fdc7e72e20ff723d586))
* revert to original rabbit source; ([351c6ea](https://github.com/LerianStudio/lib-commons/commit/351c6eac3e27301e4a65fce293032567bfd88807))

## [1.17.0-beta.12](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.11...v1.17.0-beta.12) (2025-06-25)


### Bug Fixes

* add new check channel is closed; ([e3956c4](https://github.com/LerianStudio/lib-commons/commit/e3956c46eb8a87e637e035d7676d5c592001b509))

## [1.17.0-beta.11](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.10...v1.17.0-beta.11) (2025-06-25)


### Features

* merge pull request [#124](https://github.com/LerianStudio/lib-commons/issues/124) from LerianStudio/feat/COMMONS-52-6 ([8aaaf65](https://github.com/LerianStudio/lib-commons/commit/8aaaf652e399746c67c0b8699c57f4a249271ef0))


### Bug Fixes

* rabbit hearthbeat and log type of client conn on redis/valkey; ([9607bf5](https://github.com/LerianStudio/lib-commons/commit/9607bf5c0abf21603372d32ea8d66b5d34c77ec0))

## [1.17.0-beta.10](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.9...v1.17.0-beta.10) (2025-06-24)


### Bug Fixes

* adjust camel case time name; ([5ba77b9](https://github.com/LerianStudio/lib-commons/commit/5ba77b958a0386a2ab9f8197503bbd4bd57235f0))
* Merge pull request [#123](https://github.com/LerianStudio/lib-commons/issues/123) from LerianStudio/fix/COMMONS-52-5 ([788915b](https://github.com/LerianStudio/lib-commons/commit/788915b8c333156046e1d79860f80dc84f9aa08b))

## [1.17.0-beta.9](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.8...v1.17.0-beta.9) (2025-06-24)


### Bug Fixes

* adjust redis key to use {} to calculate slot on cluster; ([318f269](https://github.com/LerianStudio/lib-commons/commit/318f26947ee847aebfc600ed6e21cb903ee6a795))
* Merge pull request [#122](https://github.com/LerianStudio/lib-commons/issues/122) from LerianStudio/feat/COMMONS-52-4 ([46f5140](https://github.com/LerianStudio/lib-commons/commit/46f51404f5f472172776abb1fbfd3bab908fc540))

## [1.17.0-beta.8](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.7...v1.17.0-beta.8) (2025-06-24)


### Features

* implements IAM refresh token; ([3d21e04](https://github.com/LerianStudio/lib-commons/commit/3d21e04194a10710a1b9de46a3f3aba89804c8b8))


### Bug Fixes

* Merge pull request [#121](https://github.com/LerianStudio/lib-commons/issues/121) from LerianStudio/feat/COMMONS-52-3 ([69c9e00](https://github.com/LerianStudio/lib-commons/commit/69c9e002ab0a4fcd24622c79c5da7857eb22c922))

## [1.17.0-beta.7](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.6...v1.17.0-beta.7) (2025-06-24)


### Features

* merge pull request [#120](https://github.com/LerianStudio/lib-commons/issues/120) from LerianStudio/feat/COMMONS-52-2 ([4293e11](https://github.com/LerianStudio/lib-commons/commit/4293e11ae36942afd7a376ab3ee3db3981922ebf))


### Bug Fixes

* adjust to create tls on redis using variable; ([e78ae20](https://github.com/LerianStudio/lib-commons/commit/e78ae2035b5583ce59654e3c7f145d93d86051e7))
* go lint ([2499476](https://github.com/LerianStudio/lib-commons/commit/249947604ed5d5382cd46e28e03c7396b9096d63))

## [1.17.0-beta.6](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.5...v1.17.0-beta.6) (2025-06-23)


### Features

* adjust to use only one host; ([22696b0](https://github.com/LerianStudio/lib-commons/commit/22696b0f989eff5db22aeeff06d82df3b16230e4))


### Bug Fixes

* Merge pull request [#119](https://github.com/LerianStudio/lib-commons/issues/119) from LerianStudio/feat/COMMONS-52 ([3ba9ca0](https://github.com/LerianStudio/lib-commons/commit/3ba9ca0e284cf36797772967904d21947f8856a5))

## [1.17.0-beta.5](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.4...v1.17.0-beta.5) (2025-06-23)


### Features

* add TTL support to Redis/Valkey and support cluster + sentinel modes alongside standalone ([1d825df](https://github.com/LerianStudio/lib-commons/commit/1d825dfefbf574bfe3db0bc718b9d0876aec5e03))
* Merge pull request [#118](https://github.com/LerianStudio/lib-commons/issues/118) from LerianStudio/feat/COMMONS-52 ([e8f8917](https://github.com/LerianStudio/lib-commons/commit/e8f8917b5c828c487f6bf2236b391dd4f8da5623))


### Bug Fixes

* .golangci.yml ([038bedd](https://github.com/LerianStudio/lib-commons/commit/038beddbe9ed4a867f6ed93dd4e84480ed65bb1b))
* gitactions; ([7f9ebeb](https://github.com/LerianStudio/lib-commons/commit/7f9ebeb1a9328a902e82c8c60428b2a8246793cf))

## [1.17.0-beta.4](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.3...v1.17.0-beta.4) (2025-06-20)


### Bug Fixes

* adjust decimal values from remains and percentage; ([e1dc4b1](https://github.com/LerianStudio/lib-commons/commit/e1dc4b183d0ca2d1247f727b81f8f27d4ddcc3c7))
* adjust some code and test; ([c6aca75](https://github.com/LerianStudio/lib-commons/commit/c6aca756499e8b9875e1474e4f7949bb9cc9f60c))

## [1.17.0-beta.3](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.2...v1.17.0-beta.3) (2025-06-20)


### Bug Fixes

* add fallback logging when logger is nil in shutdown handler ([800d644](https://github.com/LerianStudio/lib-commons/commit/800d644d920bd54abf787d3be457cc0a1117c7a1))

## [1.17.0-beta.2](https://github.com/LerianStudio/lib-commons/compare/v1.17.0-beta.1...v1.17.0-beta.2) (2025-06-20)


### Features

* add variable tableAlias variadic to ApplyCursorPagination; ([1579a9e](https://github.com/LerianStudio/lib-commons/commit/1579a9e25eae1da3247422ccd64e48730c59ba31))

## [1.17.0-beta.1](https://github.com/LerianStudio/lib-commons/compare/v1.16.0...v1.17.0-beta.1) (2025-06-16)


### Features

* revert code that was on the main; ([c2f1772](https://github.com/LerianStudio/lib-commons/commit/c2f17729bde8d2f5bbc36381173ad9226640d763))

## [1.12.0](https://github.com/LerianStudio/lib-commons/compare/v1.11.0...v1.12.0) (2025-06-13)


### Features

* add log test; ([7ad741f](https://github.com/LerianStudio/lib-commons/commit/7ad741f558e7a725e95dab257500d5d24b2536e5))
* add shutdown test ([9d5fb77](https://github.com/LerianStudio/lib-commons/commit/9d5fb77893e10a708136767eda3f9bac99363ba4))


### Bug Fixes

* Add integer overflow protection to transaction operations; :bug: ([32904de](https://github.com/LerianStudio/lib-commons/commit/32904def9bee6388f12a6e2cc997c20a594db696))
* add url for health check to read. from envs; update testes; update go mod and go sum; ([e9b8333](https://github.com/LerianStudio/lib-commons/commit/e9b83330834c7c2949dfb05a4dc46f4786cd509d))
* create redis test; ([3178547](https://github.com/LerianStudio/lib-commons/commit/317854731e550d222713503eecbdf26e2c26fa90))

## [1.12.0-beta.1](https://github.com/LerianStudio/lib-commons/compare/v1.11.0...v1.12.0-beta.1) (2025-06-13)


### Features

* add log test; ([7ad741f](https://github.com/LerianStudio/lib-commons/commit/7ad741f558e7a725e95dab257500d5d24b2536e5))
* add shutdown test ([9d5fb77](https://github.com/LerianStudio/lib-commons/commit/9d5fb77893e10a708136767eda3f9bac99363ba4))


### Bug Fixes

* Add integer overflow protection to transaction operations; :bug: ([32904de](https://github.com/LerianStudio/lib-commons/commit/32904def9bee6388f12a6e2cc997c20a594db696))
* add url for health check to read. from envs; update testes; update go mod and go sum; ([e9b8333](https://github.com/LerianStudio/lib-commons/commit/e9b83330834c7c2949dfb05a4dc46f4786cd509d))
* create redis test; ([3178547](https://github.com/LerianStudio/lib-commons/commit/317854731e550d222713503eecbdf26e2c26fa90))

## [1.12.0](https://github.com/LerianStudio/lib-commons/compare/v1.11.0...v1.12.0) (2025-06-13)


### Features

* add log test; ([7ad741f](https://github.com/LerianStudio/lib-commons/commit/7ad741f558e7a725e95dab257500d5d24b2536e5))


### Bug Fixes

* Add integer overflow protection to transaction operations; :bug: ([32904de](https://github.com/LerianStudio/lib-commons/commit/32904def9bee6388f12a6e2cc997c20a594db696))
* add url for health check to read. from envs; update testes; update go mod and go sum; ([e9b8333](https://github.com/LerianStudio/lib-commons/commit/e9b83330834c7c2949dfb05a4dc46f4786cd509d))
* create redis test; ([3178547](https://github.com/LerianStudio/lib-commons/commit/317854731e550d222713503eecbdf26e2c26fa90))

## [1.12.0-beta.1](https://github.com/LerianStudio/lib-commons/compare/v1.11.0...v1.12.0-beta.1) (2025-06-13)


### Features

* add log test; ([7ad741f](https://github.com/LerianStudio/lib-commons/commit/7ad741f558e7a725e95dab257500d5d24b2536e5))


### Bug Fixes

* Add integer overflow protection to transaction operations; :bug: ([32904de](https://github.com/LerianStudio/lib-commons/commit/32904def9bee6388f12a6e2cc997c20a594db696))
* add url for health check to read. from envs; update testes; update go mod and go sum; ([e9b8333](https://github.com/LerianStudio/lib-commons/commit/e9b83330834c7c2949dfb05a4dc46f4786cd509d))
* create redis test; ([3178547](https://github.com/LerianStudio/lib-commons/commit/317854731e550d222713503eecbdf26e2c26fa90))

## [1.12.0-beta.1](https://github.com/LerianStudio/lib-commons/compare/v1.11.0...v1.12.0-beta.1) (2025-06-13)


### Bug Fixes

* Add integer overflow protection to transaction operations; :bug: ([32904de](https://github.com/LerianStudio/lib-commons/commit/32904def9bee6388f12a6e2cc997c20a594db696))
* add url for health check to read. from envs; update testes; update go mod and go sum; ([e9b8333](https://github.com/LerianStudio/lib-commons/commit/e9b83330834c7c2949dfb05a4dc46f4786cd509d))
* create redis test; ([3178547](https://github.com/LerianStudio/lib-commons/commit/317854731e550d222713503eecbdf26e2c26fa90))

## [1.12.0](https://github.com/LerianStudio/lib-commons/compare/v1.11.0...v1.12.0) (2025-06-13)


### Bug Fixes

* Add integer overflow protection to transaction operations; :bug: ([32904de](https://github.com/LerianStudio/lib-commons/commit/32904def9bee6388f12a6e2cc997c20a594db696))
* add url for health check to read. from envs; update testes; update go mod and go sum; ([e9b8333](https://github.com/LerianStudio/lib-commons/commit/e9b83330834c7c2949dfb05a4dc46f4786cd509d))

## [1.12.0-beta.1](https://github.com/LerianStudio/lib-commons/compare/v1.11.0...v1.12.0-beta.1) (2025-06-13)


### Bug Fixes

* Add integer overflow protection to transaction operations; :bug: ([32904de](https://github.com/LerianStudio/lib-commons/commit/32904def9bee6388f12a6e2cc997c20a594db696))
* add url for health check to read. from envs; update testes; update go mod and go sum; ([e9b8333](https://github.com/LerianStudio/lib-commons/commit/e9b83330834c7c2949dfb05a4dc46f4786cd509d))

## [1.11.0](https://github.com/LerianStudio/lib-commons/compare/v1.10.0...v1.11.0) (2025-05-19)


### Features

* add info and debug log levels to zap logger initializer by env name ([c132299](https://github.com/LerianStudio/lib-commons/commit/c13229910647081facf9f555e4b4efa74aff60ec))
* add start app with graceful shutdown module ([21d9697](https://github.com/LerianStudio/lib-commons/commit/21d9697c35686e82adbf3f41744ce25c369119ce))
* bump lib-license-go version to v1.0.8 ([4d93834](https://github.com/LerianStudio/lib-commons/commit/4d93834af0dd4d4d48564b98f9d2dc766369c1be))
* move license shutdown to the end of execution and add recover from panic in graceful shutdown ([6cf1171](https://github.com/LerianStudio/lib-commons/commit/6cf117159cc10b3fa97200c53fbb6a058566c7d6))


### Bug Fixes

* fix lint - remove cuddled if blocks ([cd6424b](https://github.com/LerianStudio/lib-commons/commit/cd6424b741811ec119a2bf35189760070883b993))
* import corret lib license go uri ([f55338f](https://github.com/LerianStudio/lib-commons/commit/f55338fa2c9ed1d974ab61f28b1c70101b35eb61))

## [1.11.0-beta.2](https://github.com/LerianStudio/lib-commons/compare/v1.11.0-beta.1...v1.11.0-beta.2) (2025-05-19)


### Features

* add start app with graceful shutdown module ([21d9697](https://github.com/LerianStudio/lib-commons/commit/21d9697c35686e82adbf3f41744ce25c369119ce))
* bump lib-license-go version to v1.0.8 ([4d93834](https://github.com/LerianStudio/lib-commons/commit/4d93834af0dd4d4d48564b98f9d2dc766369c1be))
* move license shutdown to the end of execution and add recover from panic in graceful shutdown ([6cf1171](https://github.com/LerianStudio/lib-commons/commit/6cf117159cc10b3fa97200c53fbb6a058566c7d6))


### Bug Fixes

* fix lint - remove cuddled if blocks ([cd6424b](https://github.com/LerianStudio/lib-commons/commit/cd6424b741811ec119a2bf35189760070883b993))
* import corret lib license go uri ([f55338f](https://github.com/LerianStudio/lib-commons/commit/f55338fa2c9ed1d974ab61f28b1c70101b35eb61))

## [1.11.0-beta.1](https://github.com/LerianStudio/lib-commons/compare/v1.10.0...v1.11.0-beta.1) (2025-05-19)


### Features

* add info and debug log levels to zap logger initializer by env name ([c132299](https://github.com/LerianStudio/lib-commons/commit/c13229910647081facf9f555e4b4efa74aff60ec))

## [1.10.0](https://github.com/LerianStudio/lib-commons/compare/v1.9.0...v1.10.0) (2025-05-14)


### Features

* **postgres:** sets migrations path from environment variable :sparkles: ([7f9d40e](https://github.com/LerianStudio/lib-commons/commit/7f9d40e88a9e9b94a8d6076121e73324421bd6e8))

## [1.10.0-beta.1](https://github.com/LerianStudio/lib-commons/compare/v1.9.0...v1.10.0-beta.1) (2025-05-14)


### Features

* **postgres:** sets migrations path from environment variable :sparkles: ([7f9d40e](https://github.com/LerianStudio/lib-commons/commit/7f9d40e88a9e9b94a8d6076121e73324421bd6e8))

## [1.9.0](https://github.com/LerianStudio/lib-commons/compare/v1.8.0...v1.9.0) (2025-05-14)


### Bug Fixes

* add check if account is empty using accountAlias; :bug: ([d2054d8](https://github.com/LerianStudio/lib-commons/commit/d2054d8e0924accd15cfcac95ef1be6e58abae93))
* **transaction:** add index variable to loop iteration ([e2974f0](https://github.com/LerianStudio/lib-commons/commit/e2974f0c2cc87f39417bf42943e143188c3f9fc8))
* final adjust to use multiple identical accounts; :bug: ([b2165de](https://github.com/LerianStudio/lib-commons/commit/b2165de3642c9c9949cda25d370cad9358e5f5be))
* **transaction:** improve validation in send source and distribute calculations ([625f2f9](https://github.com/LerianStudio/lib-commons/commit/625f2f9598a61dbb4227722f605e1d4798a9a881))
* **transaction:** improve validation in send source and distribute calculations ([2b05323](https://github.com/LerianStudio/lib-commons/commit/2b05323b81eea70278dbb2326423dedaf5078373))
* **transaction:** improve validation in send source and distribute calculations ([4a8f3f5](https://github.com/LerianStudio/lib-commons/commit/4a8f3f59da5563842e0785732ad5b05989f62fb7))
* **transaction:** improve validation in send source and distribute calculations ([1cf5b04](https://github.com/LerianStudio/lib-commons/commit/1cf5b04fb510594c5d13989c137cc8401ea2e23d))
* **transaction:** optimize balance operations in UpdateBalances function ([524fe97](https://github.com/LerianStudio/lib-commons/commit/524fe975d125742d10920236e055db879809b01e))
* **transaction:** optimize balance operations in UpdateBalances function ([63201dd](https://github.com/LerianStudio/lib-commons/commit/63201ddeb00835d8b8b9269f8a32850e4f28374e))
* **transaction:** optimize balance operations in UpdateBalances function ([8b6397d](https://github.com/LerianStudio/lib-commons/commit/8b6397df3261cc0f5af190c69b16a55e215952ed))
* some more adjusts; :bug: ([af69b44](https://github.com/LerianStudio/lib-commons/commit/af69b447658b0f4dfcd2e2f252dd2d0d68753094))

## [1.9.0-beta.8](https://github.com/LerianStudio/lib-commons/compare/v1.9.0-beta.7...v1.9.0-beta.8) (2025-05-14)


### Bug Fixes

* final adjust to use multiple identical accounts; :bug: ([b2165de](https://github.com/LerianStudio/lib-commons/commit/b2165de3642c9c9949cda25d370cad9358e5f5be))

## [1.9.0-beta.7](https://github.com/LerianStudio/lib-commons/compare/v1.9.0-beta.6...v1.9.0-beta.7) (2025-05-13)


### Bug Fixes

* add check if account is empty using accountAlias; :bug: ([d2054d8](https://github.com/LerianStudio/lib-commons/commit/d2054d8e0924accd15cfcac95ef1be6e58abae93))
* some more adjusts; :bug: ([af69b44](https://github.com/LerianStudio/lib-commons/commit/af69b447658b0f4dfcd2e2f252dd2d0d68753094))

## [1.9.0-beta.6](https://github.com/LerianStudio/lib-commons/compare/v1.9.0-beta.5...v1.9.0-beta.6) (2025-05-12)


### Bug Fixes

* **transaction:** optimize balance operations in UpdateBalances function ([524fe97](https://github.com/LerianStudio/lib-commons/commit/524fe975d125742d10920236e055db879809b01e))
* **transaction:** optimize balance operations in UpdateBalances function ([63201dd](https://github.com/LerianStudio/lib-commons/commit/63201ddeb00835d8b8b9269f8a32850e4f28374e))

## [1.9.0-beta.5](https://github.com/LerianStudio/lib-commons/compare/v1.9.0-beta.4...v1.9.0-beta.5) (2025-05-12)


### Bug Fixes

* **transaction:** optimize balance operations in UpdateBalances function ([8b6397d](https://github.com/LerianStudio/lib-commons/commit/8b6397df3261cc0f5af190c69b16a55e215952ed))

## [1.9.0-beta.4](https://github.com/LerianStudio/lib-commons/compare/v1.9.0-beta.3...v1.9.0-beta.4) (2025-05-09)


### Bug Fixes

* **transaction:** add index variable to loop iteration ([e2974f0](https://github.com/LerianStudio/lib-commons/commit/e2974f0c2cc87f39417bf42943e143188c3f9fc8))

## [1.9.0-beta.3](https://github.com/LerianStudio/lib-commons/compare/v1.9.0-beta.2...v1.9.0-beta.3) (2025-05-09)


### Bug Fixes

* **transaction:** improve validation in send source and distribute calculations ([625f2f9](https://github.com/LerianStudio/lib-commons/commit/625f2f9598a61dbb4227722f605e1d4798a9a881))

## [1.9.0-beta.2](https://github.com/LerianStudio/lib-commons/compare/v1.9.0-beta.1...v1.9.0-beta.2) (2025-05-09)


### Bug Fixes

* **transaction:** improve validation in send source and distribute calculations ([2b05323](https://github.com/LerianStudio/lib-commons/commit/2b05323b81eea70278dbb2326423dedaf5078373))

## [1.9.0-beta.1](https://github.com/LerianStudio/lib-commons/compare/v1.8.0...v1.9.0-beta.1) (2025-05-09)


### Bug Fixes

* **transaction:** improve validation in send source and distribute calculations ([4a8f3f5](https://github.com/LerianStudio/lib-commons/commit/4a8f3f59da5563842e0785732ad5b05989f62fb7))
* **transaction:** improve validation in send source and distribute calculations ([1cf5b04](https://github.com/LerianStudio/lib-commons/commit/1cf5b04fb510594c5d13989c137cc8401ea2e23d))

## [1.8.0](https://github.com/LerianStudio/lib-commons/compare/v1.7.0...v1.8.0) (2025-04-24)


### Features

* update go mod and go sum and change method health visibility; :sparkles: ([355991f](https://github.com/LerianStudio/lib-commons/commit/355991f4416722ee51356139ed3c4fe08e1fe47e))

## [1.8.0-beta.1](https://github.com/LerianStudio/lib-commons/compare/v1.7.0...v1.8.0-beta.1) (2025-04-24)


### Features

* update go mod and go sum and change method health visibility; :sparkles: ([355991f](https://github.com/LerianStudio/lib-commons/commit/355991f4416722ee51356139ed3c4fe08e1fe47e))

## [1.7.0](https://github.com/LerianStudio/lib-commons/compare/v1.6.0...v1.7.0) (2025-04-16)


### Bug Fixes

* fix lint cuddled code ([dcbf7c6](https://github.com/LerianStudio/lib-commons/commit/dcbf7c6f26f379cec9790e14b76ee2e6868fb142))
* lint complexity over 31 in getBodyObfuscatedString ([0f9eb4a](https://github.com/LerianStudio/lib-commons/commit/0f9eb4a82a544204119500db09d38fd6ec003c7e))
* obfuscate password field in the body before logging ([e35bfa3](https://github.com/LerianStudio/lib-commons/commit/e35bfa36424caae3f90b351ed979d2c6e6e143f5))

## [1.7.0-beta.3](https://github.com/LerianStudio/lib-commons/compare/v1.7.0-beta.2...v1.7.0-beta.3) (2025-04-16)


### Bug Fixes

* lint complexity over 31 in getBodyObfuscatedString ([0f9eb4a](https://github.com/LerianStudio/lib-commons/commit/0f9eb4a82a544204119500db09d38fd6ec003c7e))

## [1.7.0-beta.2](https://github.com/LerianStudio/lib-commons/compare/v1.7.0-beta.1...v1.7.0-beta.2) (2025-04-16)

## [1.7.0-beta.1](https://github.com/LerianStudio/lib-commons/compare/v1.6.0...v1.7.0-beta.1) (2025-04-16)


### Bug Fixes

* fix lint cuddled code ([dcbf7c6](https://github.com/LerianStudio/lib-commons/commit/dcbf7c6f26f379cec9790e14b76ee2e6868fb142))
* obfuscate password field in the body before logging ([e35bfa3](https://github.com/LerianStudio/lib-commons/commit/e35bfa36424caae3f90b351ed979d2c6e6e143f5))

## [1.6.0](https://github.com/LerianStudio/lib-commons/compare/v1.5.0...v1.6.0) (2025-04-11)


### Bug Fixes

* **transaction:** correct percentage calculation in CalculateTotal ([02b939c](https://github.com/LerianStudio/lib-commons/commit/02b939c3abf1834de2078c2d0ae40b4fd9095bca))

## [1.6.0-beta.1](https://github.com/LerianStudio/lib-commons/compare/v1.5.0...v1.6.0-beta.1) (2025-04-11)


### Bug Fixes

* **transaction:** correct percentage calculation in CalculateTotal ([02b939c](https://github.com/LerianStudio/lib-commons/commit/02b939c3abf1834de2078c2d0ae40b4fd9095bca))

## [1.5.0](https://github.com/LerianStudio/lib-commons/compare/v1.4.0...v1.5.0) (2025-04-10)


### Features

* adding accountAlias field to keep backward compatibility ([81bf528](https://github.com/LerianStudio/lib-commons/commit/81bf528dfa8ceb5055714589745c1d3987cfa6da))

## [1.5.0-beta.1](https://github.com/LerianStudio/lib-commons/compare/v1.4.0...v1.5.0-beta.1) (2025-04-09)


### Features

* adding accountAlias field to keep backward compatibility ([81bf528](https://github.com/LerianStudio/lib-commons/commit/81bf528dfa8ceb5055714589745c1d3987cfa6da))

## [1.4.0](https://github.com/LerianStudio/lib-commons/compare/v1.3.0...v1.4.0) (2025-04-08)

## [1.4.0-beta.1](https://github.com/LerianStudio/lib-commons/compare/v1.3.1-beta.1...v1.4.0-beta.1) (2025-04-08)

## [1.3.1-beta.1](https://github.com/LerianStudio/lib-commons/compare/v1.3.0...v1.3.1-beta.1) (2025-04-08)

## [1.3.0](https://github.com/LerianStudio/lib-commons/compare/v1.2.0...v1.3.0) (2025-04-08)

## [1.3.0-beta.1](https://github.com/LerianStudio/lib-commons/compare/v1.2.0...v1.3.0-beta.1) (2025-04-08)

## [1.2.0](https://github.com/LerianStudio/lib-commons/compare/v1.1.0...v1.2.0) (2025-04-03)


### Bug Fixes

* update safe uint convertion to convert int instead of int64 ([a85628b](https://github.com/LerianStudio/lib-commons/commit/a85628bb031d64d542b378180c2254c198e9ae59))
* update safe uint convertion to convert max int to uint first to validate ([c7dee02](https://github.com/LerianStudio/lib-commons/commit/c7dee026532f42712eabdb3fde0c8d2b8ec7cdd8))

## [1.2.0-beta.1](https://github.com/LerianStudio/lib-commons/compare/v1.1.0...v1.2.0-beta.1) (2025-04-03)


### Bug Fixes

* update safe uint convertion to convert int instead of int64 ([a85628b](https://github.com/LerianStudio/lib-commons/commit/a85628bb031d64d542b378180c2254c198e9ae59))
* update safe uint convertion to convert max int to uint first to validate ([c7dee02](https://github.com/LerianStudio/lib-commons/commit/c7dee026532f42712eabdb3fde0c8d2b8ec7cdd8))

## [1.1.0](https://github.com/LerianStudio/lib-commons/compare/v1.0.0...v1.1.0) (2025-04-03)


### Features

* add safe uint convertion ([0d9e405](https://github.com/LerianStudio/lib-commons/commit/0d9e4052ebbd70b18508d68906296c35b881d85e))
* organize golangci-lint module ([8d71f3b](https://github.com/LerianStudio/lib-commons/commit/8d71f3bb2079457617a5ff8a8290492fd885b30d))


### Bug Fixes

* golang lint fixed version to v1.64.8; go mod and sum update packages; :bug: ([6b825c1](https://github.com/LerianStudio/lib-commons/commit/6b825c1a0162326df2abb93b128419f2ea9a4175))

## [1.1.0-beta.3](https://github.com/LerianStudio/lib-commons/compare/v1.1.0-beta.2...v1.1.0-beta.3) (2025-04-03)


### Features

* add safe uint convertion ([0d9e405](https://github.com/LerianStudio/lib-commons/commit/0d9e4052ebbd70b18508d68906296c35b881d85e))

## [1.1.0-beta.2](https://github.com/LerianStudio/lib-commons/compare/v1.1.0-beta.1...v1.1.0-beta.2) (2025-03-27)


### Features

* organize golangci-lint module ([8d71f3b](https://github.com/LerianStudio/lib-commons/commit/8d71f3bb2079457617a5ff8a8290492fd885b30d))

## [1.1.0-beta.1](https://github.com/LerianStudio/lib-commons/compare/v1.0.0...v1.1.0-beta.1) (2025-03-25)


### Bug Fixes

* golang lint fixed version to v1.64.8; go mod and sum update packages; :bug: ([6b825c1](https://github.com/LerianStudio/lib-commons/commit/6b825c1a0162326df2abb93b128419f2ea9a4175))

## 1.0.0 (2025-03-19)


### Features

* add transaction validations to the lib-commons; :sparkles: ([098b730](https://github.com/LerianStudio/lib-commons/commit/098b730fa1686b2f683faec69fabd6aa1607cf0b))
* initial commit to lib commons; ([7d49924](https://github.com/LerianStudio/lib-commons/commit/7d4992494a1328fd1c0afc4f5814fa5c63cb0f9c))
* initiate new implements from lib-commons; ([18dff5c](https://github.com/LerianStudio/lib-commons/commit/18dff5cbde19bd2659368ce5665a01f79119e7ef))


### Bug Fixes

* remove midaz reference; :bug: ([27cbdaa](https://github.com/LerianStudio/lib-commons/commit/27cbdaa5ad103edf903fb24d2b652e7e9f15d909))
* remove wrong tests; :bug: ([9f9d30f](https://github.com/LerianStudio/lib-commons/commit/9f9d30f0d783ab3f9f4f6e7141981e3b266ba600))
* update message withBasicAuth.go ([d1dcdbc](https://github.com/LerianStudio/lib-commons/commit/d1dcdbc7dfd4ef829b94de19db71e273452be425))
* update some places and adjust golint; :bug: ([db18dbb](https://github.com/LerianStudio/lib-commons/commit/db18dbb7270675e87c150f3216ac9be1b2610c1c))
* update to return err instead of nil; :bug: ([8aade18](https://github.com/LerianStudio/lib-commons/commit/8aade18d65bf6fe0d4e925f3bf178c51672fd7f4))
* update to use one response json objetc; :bug: ([2e42859](https://github.com/LerianStudio/lib-commons/commit/2e428598b1f41f9c2de369a34510c5ed2ba21569))

## [1.0.0-beta.2](https://github.com/LerianStudio/lib-commons/compare/v1.0.0-beta.1...v1.0.0-beta.2) (2025-03-19)


### Features

* add transaction validations to the lib-commons; :sparkles: ([098b730](https://github.com/LerianStudio/lib-commons/commit/098b730fa1686b2f683faec69fabd6aa1607cf0b))


### Bug Fixes

* update some places and adjust golint; :bug: ([db18dbb](https://github.com/LerianStudio/lib-commons/commit/db18dbb7270675e87c150f3216ac9be1b2610c1c))
* update to use one response json objetc; :bug: ([2e42859](https://github.com/LerianStudio/lib-commons/commit/2e428598b1f41f9c2de369a34510c5ed2ba21569))

## 1.0.0-beta.1 (2025-03-18)


### Features

* initial commit to lib commons; ([7d49924](https://github.com/LerianStudio/lib-commons/commit/7d4992494a1328fd1c0afc4f5814fa5c63cb0f9c))
* initiate new implements from lib-commons; ([18dff5c](https://github.com/LerianStudio/lib-commons/commit/18dff5cbde19bd2659368ce5665a01f79119e7ef))


### Bug Fixes

* remove midaz reference; :bug: ([27cbdaa](https://github.com/LerianStudio/lib-commons/commit/27cbdaa5ad103edf903fb24d2b652e7e9f15d909))
* remove wrong tests; :bug: ([9f9d30f](https://github.com/LerianStudio/lib-commons/commit/9f9d30f0d783ab3f9f4f6e7141981e3b266ba600))
* update message withBasicAuth.go ([d1dcdbc](https://github.com/LerianStudio/lib-commons/commit/d1dcdbc7dfd4ef829b94de19db71e273452be425))
* update to return err instead of nil; :bug: ([8aade18](https://github.com/LerianStudio/lib-commons/commit/8aade18d65bf6fe0d4e925f3bf178c51672fd7f4))

## 1.0.0 (2025-03-06)


### Features

* configuration of CI/CD ([1bb1c4c](https://github.com/LerianStudio/lib-boilerplate/commit/1bb1c4ca0659e593ff22b3b5bf919163366301a7))
* set configuration of boilerplate ([138a60c](https://github.com/LerianStudio/lib-boilerplate/commit/138a60c7947a9e82e4808fa16cc53975e27e7de5))

## 1.0.0-beta.1 (2025-03-06)


### Features

* configuration of CI/CD ([1bb1c4c](https://github.com/LerianStudio/lib-boilerplate/commit/1bb1c4ca0659e593ff22b3b5bf919163366301a7))
* set configuration of boilerplate ([138a60c](https://github.com/LerianStudio/lib-boilerplate/commit/138a60c7947a9e82e4808fa16cc53975e27e7de5))
