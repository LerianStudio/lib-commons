//go:build unit

package streaming

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
)

// fuzzURLPattern mirrors sanitize.go's urlPattern so fuzz assertions only
// apply to inputs the sanitizer actually targets. Kept separate here to
// avoid exporting the production regex — if sanitize.go's pattern diverges,
// this one needs to be updated deliberately.
var fuzzURLPattern = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s]+`)

// --- GROUP A: streaming fuzz tests. ---
//
// Fuzz tests do NOT run under `go test` by default — they require
// `go test -fuzz=<FuzzFoo>`. This keeps unit-test runs fast while still
// letting fuzz campaigns probe the code paths below for panics and
// contract violations.
//
// Seeds are added inline via f.Add so the corpus lives next to the fuzz
// target; Go persists any newly-discovered crashing inputs into
// testdata/fuzz/<FuzzFoo>/<hash> automatically. No manual file wiring.
//
// How to run a short smoke pass:
//
//	go test -tags=unit -fuzz=FuzzSanitizeBrokerURL -fuzztime=5s ./commons/streaming/
//
// How to run extended:
//
//	go test -tags=unit -fuzz=FuzzSanitizeBrokerURL -fuzztime=5m ./commons/streaming/

// FuzzParseCloudEventsHeaders drives ParseCloudEventsHeaders with arbitrary
// header bytes. Invariant: never panics. When parsing fails, returns the
// zero Event + a non-nil error; when it succeeds, returns an Event + nil.
// NEVER both a populated Event AND an error.
//
// The fuzz harness encodes a header list as a compact byte stream: for each
// header, one length byte (N) then N key bytes, one length byte (M) then
// M value bytes. The decoder tolerates truncated input by bailing early —
// we just need a mechanical way to feed the fuzzer structured data.
func FuzzParseCloudEventsHeaders(f *testing.F) {
	// --- Seed corpus (>= 5 per task spec). ---

	// Valid full header set: specversion, id, source, type, time, plus optionals.
	f.Add(encodeHeaders([]kgo.RecordHeader{
		{Key: "ce-specversion", Value: []byte("1.0")},
		{Key: "ce-id", Value: []byte("seed-valid-1")},
		{Key: "ce-source", Value: []byte("//seed/service")},
		{Key: "ce-type", Value: []byte("studio.lerian.transaction.created")},
		{Key: "ce-time", Value: []byte("2026-04-18T00:00:00Z")},
		{Key: "ce-tenantid", Value: []byte("t-seed")},
	}))

	// Missing required header (no ce-id).
	f.Add(encodeHeaders([]kgo.RecordHeader{
		{Key: "ce-specversion", Value: []byte("1.0")},
		{Key: "ce-source", Value: []byte("//seed/service")},
		{Key: "ce-type", Value: []byte("studio.lerian.x.y")},
		{Key: "ce-time", Value: []byte("2026-04-18T00:00:00Z")},
	}))

	// Invalid RFC3339 time in ce-time.
	f.Add(encodeHeaders([]kgo.RecordHeader{
		{Key: "ce-specversion", Value: []byte("1.0")},
		{Key: "ce-id", Value: []byte("seed-bad-time")},
		{Key: "ce-source", Value: []byte("//seed/service")},
		{Key: "ce-type", Value: []byte("studio.lerian.x.y")},
		{Key: "ce-time", Value: []byte("not-a-timestamp")},
	}))

	// Duplicate headers: last wins (documented behavior).
	f.Add(encodeHeaders([]kgo.RecordHeader{
		{Key: "ce-specversion", Value: []byte("1.0")},
		{Key: "ce-id", Value: []byte("seed-dup-1")},
		{Key: "ce-id", Value: []byte("seed-dup-2")},
		{Key: "ce-source", Value: []byte("//seed/service")},
		{Key: "ce-type", Value: []byte("studio.lerian.x.y")},
		{Key: "ce-time", Value: []byte("2026-04-18T00:00:00Z")},
	}))

	// Empty header values for required keys — specversion/id/source/type/time
	// all set but empty. Parser returns values as-is; this exercises the
	// zero-value handling.
	f.Add(encodeHeaders([]kgo.RecordHeader{
		{Key: "ce-specversion", Value: []byte("1.0")},
		{Key: "ce-id", Value: []byte("")},
		{Key: "ce-source", Value: []byte("")},
		{Key: "ce-type", Value: []byte("")},
		{Key: "ce-time", Value: []byte("2026-04-18T00:00:00Z")},
	}))

	f.Fuzz(func(t *testing.T, raw []byte) {
		headers := decodeHeaders(raw)

		// Never panics.
		event, err := ParseCloudEventsHeaders(headers)

		// Contract invariant: we never return both a populated Event AND
		// an error. "Populated" means the EventID was set — the function
		// writes fields in a fixed order, and a non-empty EventID means
		// the required-header checks passed. (Using Event.EventID as the
		// populated-sentinel mirrors the implementation's own short-
		// circuit: a missing ce-id causes an early return with the zero
		// Event.)
		if err != nil && event.EventID != "" {
			t.Fatalf("invariant violated: err=%v AND populated Event (EventID=%q)",
				err, event.EventID)
		}
	})
}

// FuzzSanitizeBrokerURL asserts the load-bearing invariant: if the input
// contains a URL-shaped `scheme://...:<password>@...` form with the
// hunter2 marker as the password, the output MUST NOT contain that
// literal ":hunter2@" substring anymore. Proving this at the literal-byte
// level is stronger than "password absent" — it rules out partial
// redactions that preserve the credential token inside a normalized URL.
//
// Inputs without the URL scheme prefix are out of scope for redaction by
// design (sanitize.go's urlPattern only matches `scheme://...`). We still
// verify the sanitizer doesn't panic on non-URL inputs — that's the
// secondary invariant.
func FuzzSanitizeBrokerURL(f *testing.F) {
	// --- Seed corpus (>= 5). ---
	// Mirrors the 6 sanitize_test.go cases + adversarial additions.
	f.Add("failed to connect: sasl://user:hunter2@broker.example.com:9092/cluster")
	f.Add("dial failed kafka://admin:supersecret@kafka:9092")
	f.Add("client config password=hunter2 host=broker")
	f.Add("config pass=opensesame and other stuff")
	f.Add("ordinary error message with no credentials")
	f.Add("")

	// Adversarial — nested userinfo.
	f.Add("scheme://u1:p1@u2:hunter2@host:9092")

	// Adversarial — unicode homoglyph userinfo (Cyrillic 'а' looks like Latin 'a').
	f.Add("kafka://аdmin:hunter2@kafka:9092")

	// Adversarial — URL-encoded colon inside password.
	f.Add("kafka://user%3Apass:hunter2@kafka:9092")

	// Adversarial — very long credentialed URL.
	f.Add("kafka://user:" + strings.Repeat("hunter2", 1000) + "@broker:9092")

	f.Fuzz(func(t *testing.T, in string) {
		// Primary invariant: never panic. Run the sanitizer on every
		// input.
		out := sanitizeBrokerURL(in)

		// Secondary invariant (conditional — see preconditions below):
		// when the input contains a URL-shaped token that url.Parse
		// decodes as having a real userinfo password equal to the
		// hunter2 marker, the output MUST NOT contain that exact
		// password substring.
		//
		// Precondition A: fuzzURLPattern matches a token.
		// Precondition B: url.Parse on that token yields a userinfo
		//                 whose Password() returns the hunter2 marker.
		//
		// Both preconditions ensure the sanitizer's redaction path
		// actually fires — on inputs where `@` lives in a fragment or
		// path instead of userinfo, url.Parse returns User == nil
		// (even if the raw string contains ":hunter2@"), and the
		// sanitizer deliberately leaves those malformed inputs alone.
		// Those cases are out of scope for this invariant.
		const marker = "hunter2"

		for _, m := range fuzzURLPattern.FindAllString(in, -1) {
			// Trim trailing punctuation mirroring sanitize.go. Using
			// a fresh copy per match so we don't cross-contaminate.
			trimmed, _ := splitTrailingPunctuation(m)

			parsed, err := url.Parse(trimmed)
			if err != nil || parsed == nil || parsed.User == nil {
				continue
			}

			pw, hasPW := parsed.User.Password()
			if !hasPW || pw != marker {
				continue
			}

			// Real credential present in a well-formed URL — the
			// sanitizer MUST have erased the literal password from
			// the output. Checking the exact string prevents trivial
			// passes where the sanitizer replaces the password with
			// another string that happens to contain "hunter2".
			if strings.Contains(out, marker) {
				t.Fatalf("credential leak: input=%q output=%q URL-match=%q parsed-password=%q",
					in, out, m, pw)
			}
		}
	})
}

// FuzzParseMajorVersion exercises the semver-major parser with arbitrary
// inputs. Invariants: never panics; returns int >= 0; parse failures yield
// 0 (callers depend on this short-circuit to fall through to the base topic
// form).
func FuzzParseMajorVersion(f *testing.F) {
	// --- Seed corpus (>= 5). ---
	f.Add("")              // empty → 0
	f.Add("v")             // just prefix → 0
	f.Add("1.0.0")         // valid v1 → 1
	f.Add("v2.0.0")        // valid v2 with prefix → 2
	f.Add("2.0.0-rc1")     // pre-release → 2
	f.Add("2.0.0+gitsha")  // build metadata → 2
	f.Add("9999999999")    // very large numeric → either 9999999999 or 0 if overflow
	f.Add("not.a.version") // invalid semver → 0
	f.Add("_1.0.0")        // underscore prefix → 0
	f.Add("v\u2070.0.0")   // unicode digit zero → depends; must not panic
	f.Add("-1.0.0")        // negative-looking → 0 (semver rejects)
	f.Add("1")             // no minor/patch → usually 1 per semver rules

	f.Fuzz(func(t *testing.T, in string) {
		got := parseMajorVersion(in)

		if got < 0 {
			t.Fatalf("parseMajorVersion(%q) = %d; invariant: result >= 0", in, got)
		}
	})
}

// FuzzSplitCSV exercises the CSV-splitter used by LoadConfig to parse
// STREAMING_BROKERS. Invariants:
//   - never panics
//   - every element in the result is non-empty (empty entries are dropped)
//   - every element is trimmed (no leading/trailing whitespace)
func FuzzSplitCSV(f *testing.F) {
	// --- Seed corpus (>= 5). ---
	f.Add("")                      // empty → empty slice
	f.Add("one")                   // single → ["one"]
	f.Add("one,two,three")         // comma-separated → 3 elems
	f.Add("   whitespace  ")       // trims whitespace
	f.Add("a,,b")                  // empty middle → ["a","b"]
	f.Add("a, b, c")               // whitespace around commas
	f.Add("trailing,")             // trailing comma → ["trailing"]
	f.Add(",leading")              // leading comma → ["leading"]
	f.Add("embedded,spaces, only") // mixed

	f.Fuzz(func(t *testing.T, in string) {
		got := splitCSV(in)

		for i, elem := range got {
			if elem == "" {
				t.Fatalf("splitCSV(%q)[%d] is empty; invariant: no empty strings", in, i)
			}

			if strings.TrimSpace(elem) != elem {
				t.Fatalf("splitCSV(%q)[%d] = %q; invariant: every element is trimmed", in, i, elem)
			}
		}
	})
}

// --- Helpers for FuzzParseCloudEventsHeaders. ---

// encodeHeaders serializes a kgo.RecordHeader slice into a compact byte
// stream: for each header, [keyLen][keyBytes][valLen][valBytes]. Lengths
// are single bytes — fuzz seeds that exceed 255 bytes per field cannot be
// encoded by this helper, which is fine: the seed corpus uses short
// sensible values, and the fuzzer mutates bytes freely once the seed is
// decoded.
func encodeHeaders(headers []kgo.RecordHeader) []byte {
	out := make([]byte, 0, 64)

	for _, h := range headers {
		keyBytes := []byte(h.Key)

		if len(keyBytes) > 255 || len(h.Value) > 255 {
			// Seed too big for this encoding; skip. (In practice all
			// seeds above are short, so this branch never fires.)
			continue
		}

		out = append(out, byte(len(keyBytes)))
		out = append(out, keyBytes...)
		out = append(out, byte(len(h.Value)))
		out = append(out, h.Value...)
	}

	return out
}

// decodeHeaders is the inverse of encodeHeaders. On truncated / malformed
// input it bails out early and returns whatever it managed to parse —
// mirroring how a real parser should tolerate arbitrary byte inputs
// without panicking.
func decodeHeaders(raw []byte) []kgo.RecordHeader {
	var headers []kgo.RecordHeader

	i := 0
	for i < len(raw) {
		if i+1 > len(raw) {
			return headers
		}

		keyLen := int(raw[i])
		i++

		if i+keyLen > len(raw) {
			return headers
		}

		key := string(raw[i : i+keyLen])
		i += keyLen

		if i+1 > len(raw) {
			return headers
		}

		valLen := int(raw[i])
		i++

		if i+valLen > len(raw) {
			return headers
		}

		val := make([]byte, valLen)
		copy(val, raw[i:i+valLen])
		i += valLen

		headers = append(headers, kgo.RecordHeader{Key: key, Value: val})
	}

	return headers
}
