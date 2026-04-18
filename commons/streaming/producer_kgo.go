package streaming

import (
	"fmt"
	"math"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// maxBatchMaxBytes is the upper bound we will accept for
// Config.BatchMaxBytes. franz-go takes int32 for this option, so anything
// larger than math.MaxInt32 would overflow on the narrowing conversion.
// In practice brokers cap this around 1 MiB - 10 MiB; giving operators room
// for 2 GiB is already far past any real-world setting.
const maxBatchMaxBytes = math.MaxInt32

// buildKgoOpts translates a validated streaming.Config plus the resolved
// emitterOptions into the franz-go option slice. Each option is pinned
// explicitly per TRD risk R1 — franz-go defaults have flipped between
// versions (ProducerLinger 0ms→10ms at v1.17→v1.20), and we refuse to
// silently absorb that kind of drift.
//
// cfg carries env-driven runtime knobs; opts carries caller-supplied wiring
// that must never come from environment variables (TLS certs, SASL secrets)
// per the TRD §8 security boundary. Either argument may be zero-valued.
//
// This function assumes cfg has already passed cfg.validate(); invalid
// compression codec / acks values surface as ErrInvalidCompression /
// ErrInvalidAcks defensively but the happy path never hits them.
func buildKgoOpts(cfg Config, opts emitterOptions) ([]kgo.Opt, error) {
	codec, err := resolveCompression(cfg.Compression)
	if err != nil {
		return nil, err
	}

	acks, err := resolveAcks(cfg.RequiredAcks)
	if err != nil {
		return nil, err
	}

	// Bounds check before the int→int32 narrowing. In practice operators set
	// BatchMaxBytes at or below 1 MiB; a misconfiguration above math.MaxInt32
	// would silently overflow so we clamp instead. Negative values are
	// normalized to zero — franz-go will fall back to its own default.
	batchMaxBytes := min(cfg.BatchMaxBytes, maxBatchMaxBytes)
	batchMaxBytes = max(batchMaxBytes, 0)

	kgoOpts := []kgo.Opt{
		kgo.SeedBrokers(cfg.Brokers...),

		// Batching. ProducerLinger default flipped between franz-go
		// versions — pinning it here avoids surprise latency changes.
		kgo.ProducerLinger(time.Duration(cfg.BatchLingerMs) * time.Millisecond),
		// #nosec G115 — bounds checked above; batchMaxBytes ≤ math.MaxInt32.
		kgo.ProducerBatchMaxBytes(int32(batchMaxBytes)),

		// Backpressure ceiling. When reached, ProduceSync blocks until a
		// record clears — the caller feels natural pushback instead of
		// unbounded memory growth.
		kgo.MaxBufferedRecords(cfg.MaxBufferedRecords),

		// Compression preference. A single-codec slice is fine; franz-go
		// will try the first codec and fall back silently to NoCompression
		// if it's unsupported by a particular broker.
		kgo.ProducerBatchCompression(codec),

		// Retry budget. Per-record cap; once exhausted franz-go returns
		// kgo.ErrRecordRetries which the classifier routes to DLQ in T5.
		kgo.RecordRetries(cfg.RecordRetries),
		kgo.RecordDeliveryTimeout(cfg.RecordDeliveryTimeout),

		// Durability. "all" maps to AllISRAcks; "leader" to LeaderAck;
		// "none" to NoAck. Validated at cfg.validate() time.
		kgo.RequiredAcks(acks),

		// Per-record topic is set on each kgo.Record, so the default
		// topic is a no-op — but we set it to an empty string explicitly
		// so missing cfg shows up as a ProduceSync error rather than
		// accidentally publishing to a default topic.
		kgo.DefaultProduceTopic(""),

		// Partitioning: StickyKeyPartitioner with nil hasher picks up the
		// default Kafka murmur2 hashing — we do NOT pass KafkaHasher(nil)
		// because that builds a PartitionerHasher over a nil hashFn which
		// panics at produce time. Events with the same partition key land
		// on the same partition, which is what per-tenant FIFO requires.
		kgo.RecordPartitioner(kgo.StickyKeyPartitioner(nil)),
	}

	// franz-go's idempotent producer requires acks=all. If the operator
	// opted into acks=leader/none, they've consciously traded idempotency
	// for lower latency — we must disable idempotent writes so the kgo
	// client starts up instead of failing validation.
	if cfg.RequiredAcks != "all" {
		kgoOpts = append(kgoOpts, kgo.DisableIdempotentWrite())
	}

	// ClientID is optional — if the operator didn't set it, let franz-go
	// pick a reasonable default. Empty ClientID is valid; we only add the
	// option when the operator has something specific to say.
	if cfg.ClientID != "" {
		kgoOpts = append(kgoOpts, kgo.ClientID(cfg.ClientID))
	}

	// TLS configuration (T8). franz-go's DialTLSConfig clones the config
	// per-dial and auto-fills ServerName from the broker host; callers
	// rarely need to set it. We never source TLS material from env — the
	// only path in is WithTLSConfig.
	if opts.tlsConfig != nil {
		kgoOpts = append(kgoOpts, kgo.DialTLSConfig(opts.tlsConfig))
	}

	// SASL mechanism (T8). kgo.SASL is variadic; we always pass exactly one
	// mechanism in v1. Multi-mechanism fallback (negotiate the first
	// broker-supported one) is out of scope until a real auth flow ships.
	if opts.saslMechanism != nil {
		kgoOpts = append(kgoOpts, kgo.SASL(opts.saslMechanism))
	}

	return kgoOpts, nil
}

// resolveCompression maps the Config string to a kgo.CompressionCodec. The
// match is exact (lowercase); cfg.validate() normalizes.
func resolveCompression(name string) (kgo.CompressionCodec, error) {
	switch name {
	case "snappy":
		return kgo.SnappyCompression(), nil
	case "lz4":
		return kgo.Lz4Compression(), nil
	case "zstd":
		return kgo.ZstdCompression(), nil
	case "gzip":
		return kgo.GzipCompression(), nil
	case "none":
		return kgo.NoCompression(), nil
	default:
		return kgo.CompressionCodec{}, fmt.Errorf("%w: %q", ErrInvalidCompression, name)
	}
}

// resolveAcks maps the Config string to a kgo.Acks value. cfg.validate()
// already rejects anything outside the closed set; the default branch is
// defensive.
func resolveAcks(name string) (kgo.Acks, error) {
	switch name {
	case "all":
		return kgo.AllISRAcks(), nil
	case "leader":
		return kgo.LeaderAck(), nil
	case "none":
		return kgo.NoAck(), nil
	default:
		return kgo.Acks{}, fmt.Errorf("%w: %q", ErrInvalidAcks, name)
	}
}
