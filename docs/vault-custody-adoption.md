# Adopting the Vault custody backend

`commons/secretsmanager` now holds credential material on either AWS Secrets
Manager (the default) or HashiCorp Vault KV v2. This is the order in which
consumers should adopt it, and the traps measured while building it.

Nothing here changes an existing deployment: the zero `Config` selects AWS, and
a consumer that never sets a backend keeps the behaviour it has today.

## What the lib gives a consumer

- `Config{Backend, Vault}.NewReader(awsClient)` returns a `SecretsManagerClient`.
  Every reader in the package (`GetM2MCredentials`, `GetExternalCredentials`,
  `GetExternalCredentialsByReference`) takes that interface, so a consumer that
  already holds one swaps the construction and changes nothing else.
- `Config{...}.NewWriter(awsClient)` returns a `SecretWriter`
  (`CreateSecretString`, `DeleteSecret`) — create-only, no recovery window.
- `secretsmanagertest.Run` is the contract suite both backends pass.

## Order of adoption

**1. Lender, the matcher M2M wire.** Lowest blast radius: read-only, one
credential, and the failure is visible at boot rather than mid-transaction.
`MATCHER_ENABLED=true` currently refuses to boot without an M2M provider whose
only backend is AWS Secrets Manager. Point the provider's client at
`Config.NewReader` and a deployment outside AWS boots.

Note the lender declares its own narrower client interface
(`GetSecretValue(ctx, secretID string) (string, error)`) rather than the one this
package exports. Adoption needs a small adapter, or the lender narrows onto the
lib interface — the second is preferable, since the lib type is what both
backends implement.

**2. Gateway, per-tenant Dataprev custody.** Higher blast radius: it reads AND
writes, and the material is on the money path to the government rail. The
gateway already aliases this package's reader (`SecretReader =
secretsmanager.SecretsManagerClient`), so the read half is a construction
change. The write half currently lives in the gateway behind its own
`SecretWriter` port, hand-rolled because this package wrapped reads only; that
gap is now closed, so the gateway can delete its local writer and take
`Config.NewWriter`.

## Traps, measured

**The reference carries the environment, and no backend re-derives it.** A
secret is addressed by
`tenants/{ENV_NAME}/{tenantId}/{app}/external/{target}/credentials/versions/{uuid}`.
`ParseExternalCredentialReference` rejects a scope mismatch, so material written
under one `ENV_NAME` and read under another does not resolve — and that refusal
is *not* the not-found sentinel, so the caller's fallback to static
configuration never fires and every call for that tenant fails. Material must be
written under the FINAL `ENV_NAME`; there is no pre-staging. This is identical
on both backends, and the contract suite pins it.

**An absence must classify as an absence.** Callers branch on
`ErrExternalCredentialsNotFound` / `ErrM2MCredentialsNotFound` to fall back to
static configuration. A Vault 404 cannot produce an AWS SDK error type, so the
readers' classifiers check backend-neutral sentinels first. A consumer adding a
third backend must map its absence onto `ErrBackendSecretNotFound` or every
fallback branch upstream goes dark.

**Selection never falls back.** A Vault that is unreachable, or an unknown
backend name, is an error at construction — never a quiet switch to the other
backend. Credential material answering from somewhere other than where the
operator pointed it is a defect, not resilience.

**Vault token lifecycle is the deployment's, not the lib's.** `VaultConfig`
takes a static token, which is the simplest posture and not the best one. A
deployment on Kubernetes should authenticate its own `*vaultapi.Client`
(Kubernetes auth, AppRole) and pass it to `NewVaultClientFrom`, so token renewal
stays somewhere an operator can observe it.

**Payload shape is set by the narrower backend.** Vault KV stores key/value
members and cannot hold a bare scalar or array, so both writers refuse a payload
that is not a JSON object. A payload that writes today therefore still writes
after a migration.

## Vault-side prerequisites

- A KV **v2** mount (`DefaultVaultMount` is `secret`). KV v1 has no
  check-and-set, which is what makes writes create-only.
- A policy granting `create` and `read` on `{mount}/data/{prefix}/*`, plus
  `delete` on `{mount}/metadata/{prefix}/*` for credential cleanup. Deletion uses
  the metadata endpoint on purpose: KV v2's plain delete only tombstones the
  current version and leaves the material readable by version, which is not a
  deletion of secret material.
