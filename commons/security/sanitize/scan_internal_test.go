//go:build unit

package sanitize

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// legacyRedactCardInsideRun is the implementation redactCardInsideRun replaced,
// kept verbatim so the rewrite can be held to it input for input.
//
// The rewrite is four mechanical transformations — group the run once, test the
// window length from prefix sums, build the result in one pass, and drop the
// recursion — none of which is allowed to change which spans are redacted. That
// claim is worth nothing asserted; this is where it is measured.
func legacyRedactCardInsideRun(run string) string {
	groups := digitGroupPattern.FindAllStringIndex(run, -1)

	for width := min(len(groups), maxCardDigits); width >= 2; width-- {
		for i := 0; i+width <= len(groups); i++ {
			start, end := groups[i][0], groups[i+width-1][1]

			digits := cardSeparators.Replace(run[start:end])
			if len(digits) < minCardDigits || len(digits) > maxCardDigits || !passesLuhn(digits) {
				continue
			}

			return run[:start] + SecretRedactionMarker + legacyRedactCardInsideRun(run[end:])
		}
	}

	return run
}

// literalsFromTestSources returns every string literal written in this package's
// test files. Enumerating the inputs the suite already exercises by hand would
// go stale the first time someone adds a case; the parser cannot.
func literalsFromTestSources(t *testing.T) []string {
	t.Helper()

	var out []string

	for _, name := range []string{"sanitize_test.go", "sanitize_example_test.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}

			if value, err := strconv.Unquote(lit.Value); err == nil {
				out = append(out, value)
			}

			return true
		})
	}

	return out
}

// corpusEntries returns every string stored in the committed fuzz corpus.
func corpusEntries(t *testing.T) []string {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join("testdata", "fuzz", "FuzzString", "*"))
	if err != nil {
		t.Fatalf("glob corpus: %v", err)
	}

	var out []string

	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "string(") || !strings.HasSuffix(line, ")") {
				continue
			}

			if value, err := strconv.Unquote(strings.TrimSuffix(strings.TrimPrefix(line, "string("), ")")); err == nil {
				out = append(out, value)
			}
		}
	}

	return out
}

// measurementShapes are the six shapes the package's cost is reported against,
// at 1 KiB so the legacy implementation stays fast enough to compare against.
func measurementShapes(size int) []string {
	units := []string{
		"1234 ",
		"1 ",
		"ref 1234 56 789 4111 1111 1111 1111 x ",
		"4111 1111 1111 1111 ",
		"1234567890123456789 ",
		"3782 822463 10005 ",
	}

	out := make([]string, 0, len(units))

	for _, unit := range units {
		filled := strings.Repeat(unit, size/len(unit)+1)
		out = append(out, filled[:size])
	}

	return out
}

// randomRuns builds runs of digit groups of mixed width and mixed separators,
// salted with real PANs and with near-misses that differ from one by a digit.
//
// THE SEPARATOR SET INCLUDES CHARACTERS cardSeparators DOES NOT STRIP ('/', a
// letter, the empty string). That is the one axis on which the prefix-sum length
// test and the old one can disagree: the old test measured the window AFTER
// stripping, so an unstrippable character counted toward the 19-digit ceiling,
// and the new one counts digits only. Every window where they disagree holds a
// character passesLuhn rejects, so the verdict is the same either way — and this
// is what proves it rather than asserting it.
func randomRuns(t *testing.T, count int) []string {
	t.Helper()

	rng := rand.New(rand.NewSource(1))
	separators := []string{" ", "-", ".", "", "/", "a", "  "}
	pans := []string{"4111111111111111", "4741852963074182", "378282246310005", "5500005555555559"}

	out := make([]string, 0, count)

	for range count {
		var b strings.Builder

		for groups := rng.Intn(24) + 2; groups > 0; groups-- {
			switch rng.Intn(10) {
			case 0:
				b.WriteString(pans[rng.Intn(len(pans))])
			case 1:
				pan := []byte(pans[rng.Intn(len(pans))])
				pan[rng.Intn(len(pan))] = byte('0' + rng.Intn(10))
				b.WriteString(string(pan))
			case 2:
				b.WriteString("3782 822463 10005")
			default:
				for digits := rng.Intn(8) + 1; digits > 0; digits-- {
					b.WriteByte(byte('0' + rng.Intn(10)))
				}
			}

			b.WriteString(separators[rng.Intn(len(separators))])
		}

		out = append(out, b.String())
	}

	return out
}

func TestRedactCardInsideRunMatchesTheImplementationItReplaced(t *testing.T) {
	t.Parallel()

	corpus := corpusEntries(t)
	literals := literalsFromTestSources(t)
	shapes := measurementShapes(1024)
	random := randomRuns(t, 5000)

	groups := []struct {
		name   string
		inputs []string
	}{
		{"committed fuzz corpus", corpus},
		{"every string literal in the test sources", literals},
		{"the six measurement shapes at 1 KiB", shapes},
		{"seeded random runs of mixed group widths", random},
	}

	total := 0

	for _, g := range groups {
		if len(g.inputs) == 0 {
			t.Fatalf("%s contributed no inputs; the differential would be vacuous", g.name)
		}

		for _, in := range g.inputs {
			if got, want := redactCardInsideRun(in), legacyRedactCardInsideRun(in); got != want {
				t.Fatalf("%s: redactCardInsideRun(%q)\n got  %q\n want %q", g.name, in, got, want)
			}

			total++
		}

		t.Logf("DIFFERENTIAL %-44s %5d inputs, 0 diffs", g.name, len(g.inputs))
	}

	t.Logf("DIFFERENTIAL TOTAL %d inputs, 0 diffs", total)
}

// legacyCardCandidatePatterns are the candidate shapes before the three-groups-
// plus-short-tail reading was added, kept so the widening can be measured rather
// than asserted: every span the new set redacts that the old set did not is a
// span a reader of a log line will no longer see.
var legacyCardCandidatePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b\d{4}(?:[ .-]\d{4}){2,}\b`),
	regexp.MustCompile(`\b\d{4}[ .-]\d{6}[ .-]\d{4,5}\b`),
	regexp.MustCompile(`\b\d{12,19}\b`),
}

// redactCardNumbersWith is redactCardNumbers with the candidate list as a
// parameter. It is a copy rather than a refactor of the production function
// because a test has no business reshaping the signature it is checking; the
// first assertion below is that the copy is faithful.
func redactCardNumbersWith(patterns []*regexp.Regexp, s string) string {
	for {
		next := s

		for _, pattern := range patterns {
			next = pattern.ReplaceAllStringFunc(next, redactCardCandidate)
		}

		if next == s {
			return s
		}

		s = next
	}
}

// luhnCards builds Luhn-valid cards of every accepted length, grouped as they
// are printed, half of them followed by a one-to-three digit tail.
//
// THE TAIL IS THE POINT. An acquirer prints a response code after the PAN, and
// that shape is what made the 4-4-4-N branch swallow the twelve-digit reading
// underneath it. A corpus of bare cards would have missed it, and did.
func luhnCards(t *testing.T, n int) []string {
	t.Helper()

	rng := rand.New(rand.NewSource(20260912))
	heads := []string{"4", "51", "55", "34", "37", "6011", "3056"}
	separators := []string{" ", "-", "."}

	out := make([]string, 0, n)

	for range n {
		length := 12 + rng.Intn(8)

		body := []byte(heads[rng.Intn(len(heads))])
		for len(body) < length-1 {
			body = append(body, byte('0'+rng.Intn(10)))
		}

		body = body[:length-1]

		sum, alt := 0, true
		for i := len(body) - 1; i >= 0; i-- {
			d := int(body[i] - '0')
			if alt {
				if d *= 2; d > 9 {
					d -= 9
				}
			}

			sum += d
			alt = !alt
		}

		pan := string(body) + string(rune('0'+(10-sum%10)%10))
		sep := separators[rng.Intn(len(separators))]

		groups := []string{pan[0:4], pan[4:8], pan[8:12]}
		if len(pan) > 12 {
			groups = append(groups, pan[12:])
		}

		card := strings.Join(groups, sep)

		if rng.Intn(2) == 0 {
			tail := make([]byte, rng.Intn(3)+1)
			for i := range tail {
				tail[i] = byte('0' + rng.Intn(10))
			}

			card += sep + string(tail)
		}

		out = append(out, card)
	}

	return out
}

// leakedDigitRuns returns the four-or-more digit runs that survive in b and do
// not survive in a — the digits one reading removed and the other kept.
func leakedDigitRuns(a, b string) []string {
	strip := func(s string) string {
		return cardSeparators.Replace(s)
	}

	kept := map[string]bool{}
	for _, run := range digitRunPattern.FindAllString(strip(a), -1) {
		kept[run] = true
	}

	var leaked []string

	for _, run := range digitRunPattern.FindAllString(strip(b), -1) {
		if !kept[run] {
			leaked = append(leaked, run)
		}
	}

	return leaked
}

var digitRunPattern = regexp.MustCompile(`\d{4,}`)

// TestTheShortTailShapeRedactsNothingElse holds the widened candidate shape to
// being strictly a widening.
//
// IT USED TO ASSERT NOTHING. Every difference, in either direction, went into a
// slice named "widened" and was printed with t.Logf, so the test passed on any
// result at all — and it duly reported the regression that shipped in ac0c905
// as four spans "newly redacted" when one of them was a twelve-digit card that
// had STOPPED being redacted. A harness that cannot fail is not evidence, and
// this one actively laundered the defect it existed to catch. Direction is now
// the assertion.
func TestTheShortTailShapeRedactsNothingElse(t *testing.T) {
	t.Parallel()

	type change struct{ in, before, after string }

	// The generated cards are kept separate because they are seeded and so their
	// count is stable, which lets the widening be pinned to a number. The other
	// corpora grow whenever a test string is added, and pinning a count against
	// them would fail on every unrelated test.
	cards := luhnCards(t, 4000)
	mixed := append(corpusEntries(t), literalsFromTestSources(t)...)
	mixed = append(mixed, randomRuns(t, 5000)...)

	for _, corpus := range []struct {
		name   string
		inputs []string
		pin    int
	}{
		{name: "generated Luhn-valid cards, lengths 12-19", inputs: cards, pin: 1492},
		{name: "committed corpus, test literals and random runs", inputs: mixed, pin: -1},
	} {
		var narrowed, widened []change

		for _, in := range corpus.inputs {
			now := redactCardNumbersWith(cardCandidatePatterns, in)

			// The copy must be the production function, or everything below
			// measures something that does not ship.
			if got := redactCardNumbers(in); got != now {
				t.Fatalf("the local loop is not faithful on %q: %q vs %q", in, now, got)
			}

			was := redactCardNumbersWith(legacyCardCandidatePatterns, in)
			if was == now {
				continue
			}

			// DIRECTION, NOT DIFFERENCE. A span that stopped being redacted is a
			// leak; one that started is the win the shape was widened for. Where
			// both readings redact something, the verdict is which digits
			// survive, not which output is longer.
			switch {
			case was != in && now == in:
				narrowed = append(narrowed, change{in, was, now})
			case was == in && now != in:
				widened = append(widened, change{in, was, now})
			case len(leakedDigitRuns(was, now)) > 0:
				narrowed = append(narrowed, change{in, was, now})
			default:
				widened = append(widened, change{in, was, now})
			}
		}

		for _, c := range narrowed {
			t.Errorf("NARROWED in=%q\n  was=%q\n  now=%q", c.in, c.before, c.after)
		}

		require.Empty(t, narrowed,
			"%s: the short-tail shape must only ever redact MORE; these stopped being redacted",
			corpus.name)

		if corpus.pin >= 0 {
			require.Len(t, widened, corpus.pin,
				"%s: the widening is pinned; re-measure deliberately if the shape changes",
				corpus.name)
		}

		t.Logf("OVER-REACH %s: %d inputs, %d narrowed, %d newly redacted",
			corpus.name, len(corpus.inputs), len(narrowed), len(widened))
	}
}

// TestQueryValueClassAgreesWithItsByteTest pins the compiled pattern's value
// class against isQueryValueByte for every byte there is.
//
// The two were separate hand-written lists once, and they drifted on exactly one
// byte: the helper called '\v' a terminator, RE2's \s does not contain it, and a
// sanitizer that changed its own output on a second run was the result. They are
// built from one constant now; this is what says so out loud, and what fails if
// anyone writes the set out a second time.
func TestQueryValueClassAgreesWithItsByteTest(t *testing.T) {
	t.Parallel()

	// The class as the CONSTANT describes it, re-derived here so a mistake in
	// how the pattern is assembled from the constant shows up as a disagreement.
	oneValueByte := regexp.MustCompile("^[^" + regexp.QuoteMeta(queryValueTerminators) + "]$")

	for b := range 256 {
		raw := string([]byte{byte(b)})

		// What the PRODUCTION pattern does with this byte in a value position:
		// "1<b>2" survives as one value only if the class admits b.
		match := queryParameterPattern.FindStringSubmatch("?k=1" + raw + "2")
		patternTakesIt := match != nil && len(match[3]) > 1

		require.Equal(t, isQueryValueByte(byte(b)), patternTakesIt,
			"byte %#02x (%q): the compiled pattern and isQueryValueByte disagree", b, raw)

		if b < 0x80 {
			require.Equal(t, isQueryValueByte(byte(b)), oneValueByte.MatchString(string(rune(b))),
				"byte %#02x (%q): the constant and isQueryValueByte disagree", b, raw)

			continue
		}

		// 0x80-0xFF is never a rune of its own, so the class admits whatever it
		// decodes to and the helper must agree that it is value material.
		require.True(t, isQueryValueByte(byte(b)), "byte %#02x must be value material", b)
	}
}
