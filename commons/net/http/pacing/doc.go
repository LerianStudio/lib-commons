// Package pacing paces outbound HTTP calls against rate budgets shared by every
// process that points at the same Redis.
//
// It exists for the case a process-local limiter cannot serve: several replicas
// of one service calling one external rail that counts requests per tenant and
// per institution. A local limiter gives each replica the whole budget, so N
// replicas emit N times the permitted rate.
//
// # Fail closed
//
// Every failure refuses the outbound call. A Redis command failure, a timeout, a
// reply this package cannot interpret, a rate provider that errored, a rate
// outside the permitted range, and a backend clock that moved backwards all
// return an error, and an error from Acquire means the call MUST NOT be made.
// There is no fail-open mode and no option to add one. A paced rail that stops
// receiving traffic recovers; a rail that rate-bans the caller does not.
//
// # Atomic across buckets
//
// One Acquire charges every supplied bucket in a single Redis evaluation, or
// charges none of them. A tenant permit is therefore never spent while its
// institution bucket blocks. Refusals write no bucket at all, so a caller that
// waits and retries does not leak budget on each attempt.
//
// # One clock
//
// Time comes from the Redis TIME command inside the evaluation, so replicas
// never disagree about now and no local clock is read. Each evaluation also
// records a high-water mark; a later evaluation that sees an earlier time —
// a failover onto a lagging node, or an NTP step backwards — refuses, because a
// backwards clock re-issues a budget that was already spent.
//
// Because the script calls TIME before writing keys, the backend must replicate
// script effects rather than the script itself: Redis 5.0+ (where effects
// replication is the default) or Valkey 7.2.5+ is required.
//
// # Usage
//
//	pacer, err := pacing.NewPacer(redisConn, "dataprev", pacing.WithMaxRate(50))
//	if err != nil {
//	    return err
//	}
//
//	buckets := func(req *http.Request) ([]pacing.Bucket, error) {
//	    tenant, err := pacing.TenantBucket(tmcore.GetTenantIDContext(req.Context()), tenantRate)
//	    if err != nil {
//	        return nil, err
//	    }
//
//	    institution, err := pacing.InstitutionBucket(institutionOf(req), institutionRate)
//	    if err != nil {
//	        return nil, err
//	    }
//
//	    return []pacing.Bucket{tenant, institution}, nil
//	}
//
//	transport, err := pacing.NewRoundTripper(base, pacer, buckets)
//	if err != nil {
//	    return err
//	}
//
//	client := &http.Client{Transport: transport}
//
// The rate providers are read on every wait, so a rate raised or lowered at
// runtime takes effect without a restart, and a provider reporting zero pauses
// the bucket until it reports a positive rate or the context ends.
//
// That read frequency is the contract a provider must be built for: with the
// default poll interval a waiting call reads every provider four times per
// second, multiplied by concurrent waiters and buckets. A provider must
// therefore serve from a cached or hot-reloaded in-process value and must not
// block on a network call; a provider that read a remote source on every
// invocation would amplify one waiting request into a stream of remote reads.
//
// # Known ceilings
//
// A permit is issued for one instant and is not refundable: a caller that
// abandons the call after Acquire returns leaves the budget under-spent, never
// over-spent.
//
// Waiters are not queued and retry without jitter, so under contention the order
// of grants is arbitrary and each waiter costs one Redis evaluation per wait.
// That is sized for the tens-of-calls-per-second traffic this package paces; a
// fair queue would be the upgrade path if grant order ever mattered.
//
// The burst is fixed at one: a rate of ten per second admits one call every
// 100ms, never ten at once. That is the conservative reading of a published
// rail limit, and it is why no burst option exists.
package pacing
