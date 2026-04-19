//go:build unit

package streaming

import (
	"encoding/json"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"
	"time"
)

// --- GROUP B: streaming property tests. ---
//
// Property tests use stdlib `testing/quick.Check` exclusively — no new
// deps. `quick` generates 100 random inputs per Check call by default
// (overridable via quick.Config.MaxCount); we keep the default because
// the properties exercised here are cheap.
//
// Every property test filters out inputs that violate documented
// preconditions (e.g. control-char-bearing strings fail preFlight and
// can't round-trip headers). Filtered inputs do NOT count toward the
// property assertion but DO count toward quick's iteration budget —
// which is fine: the generators below produce mostly-valid inputs.

// propertyEventGenerator produces a streaming.Event satisfying preFlight
// and the CloudEvents round-trip preconditions. Fields constrained:
//
//   - TenantID, ResourceType, EventType, Source: ASCII printable chars
//     within byte limits. No control chars (preFlight would reject).
//   - Subject: optional, ASCII printable.
//   - EventID: UUID-shaped string.
//   - Timestamp: a non-zero time, rounded to nanoseconds so RFC3339Nano
//     round-trips exactly.
//   - SchemaVersion: constrained to "1.0.0" or "v2.0.0" or "3.1.4" so
//     parseMajorVersion produces a deterministic outcome.
//   - DataContentType: fixed "application/json" (ApplyDefaults fills it).
//   - Payload: valid JSON.
//   - SystemEvent: false (we test non-system Event round-trips below;
//     system events hit a different code path).
func propertyEventGenerator(rng *rand.Rand, _ int) reflect.Value {
	// Alphabet constrained to lowercase ASCII + digits + hyphen — a
	// conservative subset that satisfies "no control chars" and is
	// representative of real tenant/event identifiers.
	alphabet := []byte("abcdefghijklmnopqrstuvwxyz0123456789-")

	pick := func(length int) string {
		if length <= 0 {
			return ""
		}

		buf := make([]byte, length)
		for i := range buf {
			buf[i] = alphabet[rng.Intn(len(alphabet))]
		}

		return string(buf)
	}

	schemaVersions := []string{"1.0.0", "1.2.3", "2.0.0", "2.1.5", "3.0.0"}

	event := Event{
		TenantID:        pick(1 + rng.Intn(32)),
		ResourceType:    pick(1 + rng.Intn(20)),
		EventType:       pick(1 + rng.Intn(20)),
		EventID:         pick(36), // UUID-like length
		SchemaVersion:   schemaVersions[rng.Intn(len(schemaVersions))],
		Timestamp:       time.Unix(1_700_000_000+int64(rng.Intn(1_000_000)), int64(rng.Intn(1_000_000_000))).UTC(),
		Source:          "//" + pick(2+rng.Intn(50)),
		Subject:         pick(rng.Intn(40)),
		DataContentType: "application/json",
		Payload:         json.RawMessage(`{"k":"v"}`),
		SystemEvent:     false,
	}

	return reflect.ValueOf(event)
}

// propertyConfig wraps propertyEventGenerator into a *quick.Config so
// callers just pass it to quick.Check. Rand is seeded explicitly at 0
// for deterministic, reproducible runs.
func propertyConfig() *quick.Config {
	return &quick.Config{
		Rand: rand.New(rand.NewSource(0)),
		Values: func(args []reflect.Value, rng *rand.Rand) {
			args[0] = propertyEventGenerator(rng, 0)
		},
	}
}

// TestProperty_CloudEvents_BuildParse_Roundtrip asserts that for any Event
// satisfying the preFlight preconditions, the round-trip through
// buildCloudEventsHeaders and ParseCloudEventsHeaders recovers the
// required + optional fields.
//
// Timestamps are compared via time.Equal() because RFC3339Nano
// serialization + parsing can shift representation (e.g., UTC→UTC) without
// changing the instant.
func TestProperty_CloudEvents_BuildParse_Roundtrip(t *testing.T) {
	t.Parallel()

	property := func(event Event) bool {
		// Defensive: skip events that preFlight would reject. Pure unit
		// level — we don't need a Producer to evaluate the predicate.
		//
		// We instantiate a dummy Producer solely to reuse preFlight's
		// validation table. allowSystemEvents=false is fine because our
		// generator never sets SystemEvent=true.
		p := &Producer{}
		if err := p.preFlight(event); err != nil {
			return true // filtered; property doesn't apply
		}

		headers := buildCloudEventsHeaders(event)

		parsed, err := ParseCloudEventsHeaders(headers)
		if err != nil {
			t.Errorf("ParseCloudEventsHeaders err = %v for event %+v", err, event)
			return false
		}

		if parsed.EventID != event.EventID {
			return false
		}
		if parsed.Source != event.Source {
			return false
		}
		if parsed.ResourceType != event.ResourceType {
			return false
		}
		if parsed.EventType != event.EventType {
			return false
		}
		if parsed.TenantID != event.TenantID {
			return false
		}
		if parsed.SchemaVersion != event.SchemaVersion {
			return false
		}
		if !parsed.Timestamp.Equal(event.Timestamp) {
			return false
		}
		if parsed.Subject != event.Subject {
			return false
		}
		if parsed.DataContentType != event.DataContentType {
			return false
		}
		if parsed.SystemEvent != event.SystemEvent {
			return false
		}

		return true
	}

	if err := quick.Check(property, propertyConfig()); err != nil {
		t.Error(err)
	}
}

// TestProperty_ApplyDefaults_Idempotent asserts that applying defaults
// twice yields the same Event as applying them once. Load-bearing for
// the "safe to call on pre-populated events" contract in the godoc.
//
// The property operates on a LOCAL copy because ApplyDefaults mutates
// through its pointer receiver.
func TestProperty_ApplyDefaults_Idempotent(t *testing.T) {
	t.Parallel()

	// Use a bespoke generator here: ApplyDefaults only fills zero values,
	// so a mix of zero + non-zero fields is the interesting case. Using
	// propertyEventGenerator directly is fine because its outputs populate
	// every field, which exercises the no-op branch of ApplyDefaults.
	property := func(event Event) bool {
		once := event
		(&once).ApplyDefaults()

		twice := once
		(&twice).ApplyDefaults()

		return reflect.DeepEqual(once, twice)
	}

	if err := quick.Check(property, propertyConfig()); err != nil {
		t.Error(err)
	}
}

// TestProperty_Topic_Deterministic asserts:
//   - e.Topic() is deterministic (two calls return the same string)
//   - the ".v<major>" suffix appears iff the parsed major version is >= 2
func TestProperty_Topic_Deterministic(t *testing.T) {
	t.Parallel()

	property := func(event Event) bool {
		t1 := event.Topic()
		t2 := event.Topic()

		if t1 != t2 {
			return false
		}

		major := parseMajorVersion(event.SchemaVersion)

		// Base form: "lerian.streaming.<resource>.<event>"
		base := topicPrefix + event.ResourceType + "." + event.EventType

		if major >= 2 {
			expectedSuffix := ".v" + itoa(major)
			if t1 != base+expectedSuffix {
				return false
			}
		} else if t1 != base {
			return false
		}

		return true
	}

	if err := quick.Check(property, propertyConfig()); err != nil {
		t.Error(err)
	}
}

// TestProperty_DeriveAggregateID_Deterministic asserts:
//   - For two non-system events with identical PartitionKey (TenantID),
//     deriveAggregateID produces the same UUID.
//   - For system events (SystemEvent=true), deriveAggregateID is
//     non-deterministic (two calls yield different UUIDs).
//
// The non-system branch uses uuid.NewSHA1 which is a pure hash — same
// input = same output. The system branch uses uuid.New() which is
// random. This property pins that split.
func TestProperty_DeriveAggregateID_Deterministic(t *testing.T) {
	t.Parallel()

	// Non-system branch: deterministic.
	nonSystemProperty := func(event Event) bool {
		// Force non-system for this property.
		event.SystemEvent = false
		// Filter empty tenant — PartitionKey would be "" and the
		// derivation is still deterministic but tests nothing useful.
		if event.TenantID == "" {
			return true
		}

		// Construct a sibling event with same TenantID but different
		// non-partition-key fields. Their PartitionKeys match, so
		// AggregateIDs should match too.
		sibling := event
		sibling.EventID = event.EventID + "-sibling"
		sibling.Subject = "different-subject"

		a := DeriveAggregateIDForTest(event)
		b := DeriveAggregateIDForTest(sibling)

		return a == b
	}

	if err := quick.Check(nonSystemProperty, propertyConfig()); err != nil {
		t.Error(err)
	}

	// System branch: NON-deterministic. Two back-to-back calls on the
	// same event yield different UUIDs. Running 10 iterations so a
	// vanishingly-small collision probability can't mask a real bug.
	sysEvent := Event{
		ResourceType: "x",
		EventType:    "y",
		Source:       "//system",
		SystemEvent:  true,
	}

	for i := range 10 {
		a := DeriveAggregateIDForTest(sysEvent)
		b := DeriveAggregateIDForTest(sysEvent)

		if a == b {
			t.Fatalf("iter %d: system event yielded identical UUIDs %v; want non-deterministic", i, a)
		}
	}
}

// itoa is a tiny helper that avoids pulling in strconv for a single
// stringified integer inside the Topic property. Wrapping strconv.Itoa
// would work too; the local helper keeps the property file's imports
// minimal.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}

	neg := n < 0
	if neg {
		n = -n
	}

	// Buffer sized for int64 max: 20 digits + sign.
	buf := [21]byte{}
	i := len(buf)

	for n > 0 {
		i--
		buf[i] = byte('0' + (n % 10))
		n /= 10
	}

	if neg {
		i--
		buf[i] = '-'
	}

	return string(buf[i:])
}
