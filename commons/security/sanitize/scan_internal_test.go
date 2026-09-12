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

	for _, name := range []string{"sanitize_test.go", "sanitize_example_test.go", "scan_internal_test.go"} {
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
// urlAndPairLines builds the two shapes that produced every idempotence defect:
// URLs whose authority holds a boundary byte a later pass can rewrite, and
// chains of "<token> =<key> =<value>" where a token can steal the key slot.
func urlAndPairLines(t *testing.T, n int) []string {
	t.Helper()

	rng := rand.New(rand.NewSource(1010))

	schemes := []string{"http", "https", "postgres", "A", "amqp"}
	hosts := []string{"h", "host", "db.internal:5432", "example.com"}
	users := []string{"u", "u:p", "keY=#&", "keY=/&", "keY=?&", "key=v&", "x=****&", "tok#en"}
	tails := []string{"", "/", "/p", "/v1/charge?a=1", "#frag", "#frag@x", "?x=@y"}

	tokens := []string{"AKIAIOSFODNN7EXAMPLE", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", "plainword", "0"}
	keys := []string{"rg", "cpf", "password", "token", "ref", "status"}
	// '\v' is in [[:space:]] and NOT in RE2's \s, so it is the one byte the
	// separator class and the value class disagree about; without it here the
	// generators cannot reach the shape at all. A value that is a field NAME and
	// the separator, and a value that ENDS in the separator with nothing
	// claimable behind it, are the two halves of "a value never ends in the
	// separator" — both unreachable from the sets this list held.
	seps := []string{" =", "\t=", "\n=", "\v=", "\f=", "\r=", "=", "= "}
	values := []string{"0", "abc", "hunter2", "4111 1111 1111 1111", "password=", "cpf=", "abc_token=", "!password=", "\v"}

	out := make([]string, 0, n)

	for i := range n {
		if i%2 == 0 {
			out = append(out, schemes[rng.Intn(len(schemes))]+"://"+
				users[rng.Intn(len(users))]+"@"+
				hosts[rng.Intn(len(hosts))]+
				tails[rng.Intn(len(tails))])

			continue
		}

		out = append(out, tokens[rng.Intn(len(tokens))]+
			seps[rng.Intn(len(seps))]+keys[rng.Intn(len(keys))]+
			seps[rng.Intn(len(seps))]+values[rng.Intn(len(values))])
	}

	return out
}

// nameShapedValueLines builds the shape pass 13 exists for: a value that is
// itself a field name, under a sensitive key and under a harmless one, in every
// position from which that name could introduce the credential instead of being
// it — plus the credential sitting behind the separator's own whitespace.
//
// THE GENERATORS COULD NOT REACH THE FAMILY AT ALL. urlAndPairLines draws its
// values from a fixed list of tokens, none of which parses as a field name in
// the value slot, and inputs.txt held two lines of "=<whitespace>" with no
// credential behind either. So a rule that handed a name-shaped credential back
// to the scanner, and a separator class that made a lone '\v' the value, both
// shipped under a green direction harness.
//
// It is a full enumeration rather than a seeded draw: the axes are small, and a
// draw over them leaves the same holes to luck.
//
// THE NAMES CARRY PUNCTUATION AND VENDOR WORDS BECAUSE A WHOLE-NAME TEST AND A
// SUBSTRING TEST AGREE ON EVERY CLEAN NAME. A value of "[cpf" or "hunter2@CVC"
// is the only shape that tells the two apart, and the corpus does not hold one:
// no input file line matches "=<non-word><name> =" at all, so a rule that
// demanded the whole value be a field name sat under a green harness while it
// printed the credential behind the spaced separator.
func nameShapedValueLines() []string {
	spaces := []string{" ", "\t", "\n", "\v", "\f", "\r"}
	names := []string{
		"password", "secret", "token", "cpf", "rg", "my-password", "myPassword",
		"myKey", "hunter2.rg", "s3cr3t.pin", "aGVsbG8.cvc", "xY9_key", "abc_token",
		// PUNCTUATION-LED AND VENDOR-WORD NAMES, which the thirteen above are
		// not: every one of them is a WHOLE field name, so a rule that asks
		// whether the value IS a name and a rule that asks whether it HOLDS one
		// answer the same on all thirteen. A driver, a validator or a
		// constraint prints the bracket, the quote or the bang in front of the
		// name routinely, and no corpus line reaches the spelling at all.
		"[cpf", "!password", `"cpf"`, "(cpf", "<cvc", "{secret", "hunter2@CVC",
	}
	keys := []string{"password", "opt"}

	out := make([]string, 0, 1024)

	// The credential behind the separator's own whitespace, one byte of it and
	// two: "password=\v hunter2" is the shape that made a lone '\v' a value.
	for _, key := range []string{"password", "cpf", "token"} {
		for _, first := range spaces {
			out = append(out, key+"="+first+"hunter2")

			for _, second := range spaces {
				out = append(out, key+"="+first+second+"hunter2")
			}
		}
	}

	// The name in the value slot: at end of line, introducing a bare value
	// across a spaced separator, introducing one behind its own separator, and
	// followed by a whole pair instead.
	for _, key := range keys {
		for _, name := range names {
			out = append(out, key+"="+name+"=")

			for _, space := range spaces {
				out = append(out,
					key+"="+name+space+"=hunter2",
					key+"="+name+"="+space+"hunter2",
					key+"="+name+"="+space+"rc=200",
					// THE SECOND SHAPE WITH A PAIR BEHIND IT, which the three
					// above do not build: the separator is outside the value
					// here, so what follows is that value whether it is
					// pair-shaped or not, and one diagnostic pair goes under
					// the marker. That is a decision, so it is generated.
					key+"="+name+space+"=rc=200")
			}
		}
	}

	// Chains of names to depth three, which is where a rule that stops at the
	// first one leaves the credential at the end of the chain in the clear.
	for _, key := range keys {
		for _, name := range names[:5] {
			chain := key + "="

			for range 3 {
				chain += name + " ="

				out = append(out, chain+"0")
			}
		}
	}

	return out
}

// roundsNeeded counts the sanitizeOnce calls String would make: one that finds
// nothing to change is still a round, so an input that is already settled costs
// one and an input that changes once costs two.
func roundsNeeded(s string) int {
	for i := 1; i <= 32; i++ {
		next := sanitizeOnce(s)
		if next == s {
			return i
		}

		s = next
	}

	return 33
}

// TestEveryCorpusInputSettlesWithinThreeRounds is the measured half of the
// termination argument in String.
//
// THERE IS NO SHRINKING MEASURE TO APPEAL TO. A round can make the string
// longer ("keY=#" becomes "keY=****"), so the loop is not bounded by descent; it
// is bounded by maxSanitizeRounds. What makes that cap a fact rather than a hope
// is measurement, and the measurement is THREE, not two: "A://keY=/&@" needs the
// key=value pass to redact the value and delete the '/' (round one), the URL
// pass to then see an '@' inside the authority (round two), and a third round to
// confirm nothing more changes. Two changing rounds plus its confirmation.
//
// Across the committed fuzz corpus, every string literal in the test sources,
// the generated card corpus, the random grouped runs and the URL and key=value
// chains that produced all four defects, nothing has ever needed a fourth. Most
// inputs need one or two; the three-round shapes are roughly one in seven of the
// URL and pair chains and rarer everywhere else.
//
// If this ever fails, the cap is still correct — the fuzz harness asserts
// idempotence under it, so an input needing more rounds surfaces there — but the
// number in the name is then wrong and the cost of String has changed.
func TestEveryCorpusInputSettlesWithinThreeRounds(t *testing.T) {
	t.Parallel()

	groups := []struct {
		name   string
		inputs []string
	}{
		{"committed fuzz corpus", corpusEntries(t)},
		{"every string literal in the test sources", literalsFromTestSources(t)},
		{"generated Luhn-valid cards", luhnCards(t, 4000)},
		{"seeded random grouped runs", randomRuns(t, 5000)},
		{"URL authorities and key=value chains", urlAndPairLines(t, 5000)},
	}

	for _, g := range groups {
		require.NotEmpty(t, g.inputs, "%s contributed no inputs", g.name)

		worst, worstIn, total := 0, "", 0
		dist := map[int]int{}

		for _, in := range g.inputs {
			if in == "" || len(in) > MaxInputLen {
				continue
			}

			total++

			n := roundsNeeded(in)
			dist[n]++

			if n > worst {
				worst, worstIn = n, in
			}
		}

		require.LessOrEqual(t, worst, 3,
			"%s: %q needed %d rounds; the cap is %d and the cost of String has changed",
			g.name, worstIn, worst, maxSanitizeRounds)

		t.Logf("ROUNDS %-42s %5d inputs, worst %d, rounds->count %v", g.name, total, worst, dist)
	}
}

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

// operatorDiagnosticLines are the log and error shapes the reviewers of this
// package ran by hand: the ones a driver, an acquirer, a broker client or a
// validator actually prints, rather than the ones a pattern was written for.
//
// EVERY DEFECT IN THIS PACKAGE SINCE ROUND FOUR WAS FOUND ON A LINE OF THIS
// KIND AND ON NO GENERATED ONE. The generators build the shapes a defect was
// already known to live in; these are the shapes an operator sees. They are
// listed here so the direction harness below carries them permanently.
var operatorDiagnosticLines = []string{
	// A key pasted out of a kubectl output with its END line lost and a
	// vertical tab where the newline was: the one whitespace byte RE2's \s does
	// not hold, in the position a headless body starts at.
	"loading signer: private_key=-----BEGIN RSA PRIVATE KEY-----\vMIIBAgEAAoIBAQDLVtBjTm3x",

	// Connection strings, as the drivers echo them back on failure.
	"pgx: host=db =password= hunter2 sslmode=require",
	"host=db password=s3cr3t sslmode=require",
	"host=db port=5432 user=svc password=s3cr3t dbname=ledger sslmode=verify-full",
	"failed to connect to `host=db user=svc database=ledger`: server error",
	"postgres://svc:hunter2@db.internal:5432/ledger?sslmode=require",
	"mongodb://svc:hunter2@mongo-0:27017,mongo-1:27017/ledger?replicaSet=rs0",
	"amqp://svc:hunter2@rabbit:5672/%2f",
	"redis://default:hunter2@valkey:6379/0",
	"dsn = postgres://svc:hunter2@db:5432/ledger sslpassword=keypass",

	// Acquirer and card-network responses.
	"op=charge =cpf= 12345678901 rc=200",
	"POST /charge =cvc=999 rc=05",
	"auth denied pan=4111 1111 1111 1111 rc=51",
	"capture id=abc123 =card= 4111111111111111 amount=1000",
	"settlement row rejected: conta=12345-6 agencia=0001 valor=100",

	// Broker and cloud SDK option dumps.
	"kafka: acks=all =aws_secret_access_key= wJalrXUtnFEMI",
	"kafka: bootstrap.servers=b1:9092 sasl_password=hunter2 acks=all",
	"sqs: region=us-east-1 aws_access_key_id=AKIAIOSFODNN7EXAMPLE aws_secret_access_key=wJalrXUtnFEMI",
	"rabbit consumer opts: prefetch=10 =password= hunter2 heartbeat=60",

	// Validator and constraint messages, which name the field twice.
	"validator: field=cpf =cpf= 12345678901",
	"validation failed on field=cpf value=12345678901",
	`ERROR: duplicate key value violates unique constraint "accounts_document_key" (SQLSTATE 23505) document=12345678901`,
	"k=v =cpf= hunter2 refused",
	"tok =rg =0",

	// Headers and tokens as a proxy or a gateway logs them.
	"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW",
	"x-api-key: sk_live_1234567890abcdefghij upstream=502",
	`{"password":"hunter2","host":"db"}`,
	`{\"aws_secret_access_key\": \"wJalrXUtnFEMI\"}`,
	"endpoint=https://api.internal/v1?apikey=s3cr3t&page=2",
	"webhook=https://hooks.example.com/t/abc?sig=Zm9vYmFy&ts=1",

	// A value that IS a field name and the separator, with nothing claimable
	// behind it: the rewind that reads it as a pair boundary has nothing to hand
	// the scanner, so the value was copied across in the clear.
	"password=abc_token=",
	"accesskey=access_key=,next=1",
	"password=abc_token= rc=200",
	"a=accesskey=access_key=",
	"authorization=Bearer_token=",

	// The field name in the value slot with a non-word byte in front of it, and
	// with the credential a spaced separator further on.
	"token=!password= hunter2",
	`pwd="password= hunter2"`,
	"secret=[cpf= 12345678901",
	"password= =cpf= 12345678901",
	"a=password= rg =hunter2",
	"pgx: opt=password= cpf\t=12345678901 sslmode=require",

	// '\v' as the separator's trailing whitespace, which the value class admits
	// and the separator class does not.
	"k=k=pwd=\v",
	"a=b=password=\v hunter2",
	"t=b=key=\v",
	"b=s=secret=\v",

	// Base64 padding in a value, which is NOT a pair boundary.
	"token=aGVsbG8= more",
	"token=aGVsbG8=",
}

// directionInputPath and directionBasePath hold the pinned pair the direction
// harness runs on: the inputs, and String's output for each of them at the
// commit named in the file name.
const (
	directionInputPath = "testdata/direction/inputs.txt"
	directionBasePath  = "testdata/direction/base-714ca9e.txt"
)

// directionInputs assembles the input set for the direction harness.
//
// It is DETERMINISTIC: the corpus glob is sorted, the AST walk follows file
// order, and every generator is seeded, so regenerating produces the same file
// byte for byte. Duplicates are dropped in first-seen order, which is what lets
// an operator line also live in this file's literals without being run twice.
func directionInputs(t *testing.T) []string {
	t.Helper()

	groups := [][]string{
		corpusEntries(t),
		operatorDiagnosticLines,
		literalsFromTestSources(t),
		urlAndPairLines(t, 2000),
		nameShapedValueLines(),
		luhnCards(t, 250),
		randomRuns(t, 250),
	}

	seen := map[string]bool{}
	out := make([]string, 0, 4096)

	for _, group := range groups {
		for _, in := range group {
			if in == "" || len(in) > MaxInputLen || seen[in] {
				continue
			}

			seen[in] = true

			out = append(out, in)
		}
	}

	return out
}

// readQuotedLines reads a file of strconv.Quote'd strings, one per line.
func readQuotedLines(t *testing.T, path string) []string {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var out []string

	for _, line := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
		value, err := strconv.Unquote(line)
		if err != nil {
			t.Fatalf("%s: unquote %q: %v", path, line, err)
		}

		out = append(out, value)
	}

	return out
}

// redactionResidue is the clear text an output still carries: the output with
// every marker replaced by a SPACE. Direction is measured on it rather than on
// the output, because a redaction that grows the string is still a redaction.
//
// A SPACE AND NOT AN EMPTY STRING, because the residue is compared token by
// token and removing material cannot join two tokens that were written apart.
// Deleting the marker instead made "&cpf=**** 5678&9012" (base) and
// "&cpf=****&9012" (head, which redacts the whole grouped run) look like a head
// token spanning two base tokens, which is what a LEAK looks like — the head
// there redacts strictly more.
func redactionResidue(s string) string {
	return strings.ReplaceAll(s, SecretRedactionMarker, " ")
}

// isTokenSubsequence reports whether every whitespace-delimited token of a can
// be read out of the tokens of b, in order, one token inside one token.
//
// BYTE-LEVEL IS TOO WEAK TO BE THE MEASURE OF A CREDENTIAL, AND THE HOLE IS
// NARROWER THAN IT LOOKS. A head that ONLY leaks returns a longer residue than
// the base, and a longer string is never a subsequence of a shorter one, so a
// pure leak-against-redaction pair is caught either way. The hole opens when
// the same output also redacts MORE somewhere else on the line — the ordinary
// case here, since every marker becomes a space and a grouped run the head
// takes whole gives several of them back. The residue is then short enough to
// fit, and "hunter2" reads out of an ordinary operator line byte by byte:
// h-u-n-t-e-r from "user=hunter" and the 2 from "ledger=2".
//
// Whitespace is where a credential ends, so requiring each head token to sit
// inside ONE base token keeps the widenings that matter ("password=" inside the
// base's "password=hunter2") and refuses the ones assembled out of unrelated
// words.
func isTokenSubsequence(a, b string) bool {
	base := strings.Fields(b)
	next := 0

	for _, token := range strings.Fields(a) {
		for next < len(base) && !isSubsequence(token, base[next]) {
			next++
		}

		if next == len(base) {
			return false
		}
	}

	return true
}

// isSubsequence reports whether a can be read out of b in order.
func isSubsequence(a, b string) bool {
	i := 0

	for j := 0; i < len(a) && j < len(b); j++ {
		if a[i] == b[j] {
			i++
		}
	}

	return i == len(a)
}

// directionBaseOverReach lists the pinned-base lines the head redacts LESS of,
// deliberately, and it is the only exemption the direction harness grants.
//
// Every one of them is the same shape: a URL whose userinfo carries a '#'.
// 714ca9e read that '#' as the start of a fragment, so the authority ended at
// "tok" and the credential was never treated as userinfo at all — what removed
// "en@db.internal" from the output was the bare-EMAIL pass, which saw an address
// spanning the rest of the credential and the HOST. The base therefore printed
// "postgres://tok#****:5432/..." : half the credential in the clear, and the
// hostname gone.
//
// a0f664a ("a hash before the at-sign is userinfo, not a fragment") reads the
// authority correctly, so the head prints "postgres://****@db.internal:5432/...":
// the whole credential redacted, the host kept, which is what this package
// promises for every other URL. The residue rule sees only that a hostname came
// back and calls it a narrowing.
//
// THE EXEMPTION IS NOT A PASS. Each row names the credential the head must still
// remove, and the harness asserts it — so a future change that leaks the
// userinfo on these lines fails here rather than being covered by this list.
var directionBaseOverReach = []struct{ input, credential string }{
	{input: "A://tok#en@db.internal:5432/", credential: "tok#en"},
	{input: "A://tok#en@db.internal:5432/v1/charge?a=1", credential: "tok#en"},
	{input: "A://tok#en@db.internal:5432?x=@y", credential: "tok#en"},
	{input: "A://tok#en@example.com#frag", credential: "tok#en"},
	{input: "A://tok#en@example.com/", credential: "tok#en"},
	{input: "A://tok#en@example.com/p", credential: "tok#en"},
	{input: "A://tok#en@example.com/v1/charge?a=1", credential: "tok#en"},
	{input: "A://tok#en@example.com?x=@y", credential: "tok#en"},
	{input: "amqp://tok#en@db.internal:5432", credential: "tok#en"},
	{input: "amqp://tok#en@db.internal:5432#frag", credential: "tok#en"},
	{input: "amqp://tok#en@db.internal:5432/", credential: "tok#en"},
	{input: "amqp://tok#en@db.internal:5432/p", credential: "tok#en"},
	{input: "amqp://tok#en@example.com#frag", credential: "tok#en"},
	{input: "amqp://tok#en@example.com/", credential: "tok#en"},
	{input: "amqp://tok#en@example.com/p", credential: "tok#en"},
	{input: "amqp://tok#en@example.com?x=@y", credential: "tok#en"},
	{input: "http://tok#en@db.internal:5432/p", credential: "tok#en"},
	{input: "http://tok#en@example.com#frag", credential: "tok#en"},
	{input: "http://tok#en@example.com/", credential: "tok#en"},
	{input: "http://tok#en@example.com/v1/charge?a=1", credential: "tok#en"},
	{input: "http://tok#en@example.com?x=@y", credential: "tok#en"},
	{input: "https://tok#en@db.internal:5432#frag", credential: "tok#en"},
	{input: "https://tok#en@db.internal:5432/v1/charge?a=1", credential: "tok#en"},
	{input: "https://tok#en@db.internal:5432?x=@y", credential: "tok#en"},
	{input: "https://tok#en@example.com#frag", credential: "tok#en"},
	{input: "https://tok#en@example.com/p", credential: "tok#en"},
	{input: "https://tok#en@example.com/v1/charge?a=1", credential: "tok#en"},
	{input: "postgres://tok#en@db.internal:5432/", credential: "tok#en"},
	{input: "postgres://tok#en@db.internal:5432/p", credential: "tok#en"},
	{input: "postgres://tok#en@db.internal:5432/v1/charge?a=1", credential: "tok#en"},
	{input: "postgres://tok#en@db.internal:5432?x=@y", credential: "tok#en"},
	{input: "postgres://tok#en@example.com", credential: "tok#en"},
	{input: "postgres://tok#en@example.com#frag", credential: "tok#en"},
	{input: "postgres://tok#en@example.com/p", credential: "tok#en"},
	{input: "postgres://tok#en@example.com/v1/charge?a=1", credential: "tok#en"},
}

// directionBaseKept lists the pinned-base lines where the base removed text the
// head prints, for a reason that is not the head redacting less of a credential.
//
// TWENTY OF THE TWENTY-TWO ARE "<sensitive key>=<name>=\v<pair>", one is the
// FuzzString seed for the query pass's own vertical tab, and one is the known
// gap named below. 714ca9e admitted '\v'
// as an ordinary value byte, so the whole tail was ONE value and went under the
// marker — the response code with it. '\v' is whitespace on both sides of the
// separator now, which makes "rc=200" the next pair, exactly as it is in
// "password=abc_token= rc=200": a value that ends in the separator is a
// complete value, and the pair behind it stays diagnosable.
//
// ONE ROW IS A KNOWN GAP RATHER THAN A NON-CREDENTIAL, and it is named as one
// because an exemption list that blurs the two is how a leak gets a green
// harness. "password=\"cpf\"=\vhunter2" prints hunter2, and the yardstick for a
// '\v' row is the SPACE analogue, not the pinned base: "password=\"cpf\"= hunter2"
// prints it too, on this head and on 714ca9e and b84b1e7 alike. The value
// '"cpf"=' ends in the separator but holds no field name ABUTTING it — the
// closing quote sits between — so neither shape of introducesAValue reaches it,
// and the base removed the credential only by the same accident that removed
// the response codes above. Closing it means believing a name anywhere inside a
// value that ends in '=', which is a choice between readings of the line and
// not this pass's to make.
//
// THE ROW ASSERTS THE CLEAR TEXT IS STILL THERE, which is the opposite
// assertion to directionBaseOverReach and exists for the same reason — an
// exemption that asserts nothing is a hole. A later pass that redacts the pair,
// or closes the gap, turns these red, and they are deleted rather than left to
// rot.
var directionBaseKept = []struct{ input, want string }{
	{input: "password=password=\vrc=200", want: "password=" + SecretRedactionMarker + "\vrc=200"},
	{input: "password=secret=\vrc=200", want: "password=" + SecretRedactionMarker + "\vrc=200"},
	{input: "password=token=\vrc=200", want: "password=" + SecretRedactionMarker + "\vrc=200"},
	{input: "password=cpf=\vrc=200", want: "password=" + SecretRedactionMarker + "\vrc=200"},
	{input: "password=rg=\vrc=200", want: "password=" + SecretRedactionMarker + "\vrc=200"},
	{input: "password=my-password=\vrc=200", want: "password=" + SecretRedactionMarker + "\vrc=200"},
	{input: "password=myPassword=\vrc=200", want: "password=" + SecretRedactionMarker + "\vrc=200"},
	{input: "password=myKey=\vrc=200", want: "password=" + SecretRedactionMarker + "\vrc=200"},
	{input: "password=hunter2.rg=\vrc=200", want: "password=" + SecretRedactionMarker + "\vrc=200"},
	{input: "password=s3cr3t.pin=\vrc=200", want: "password=" + SecretRedactionMarker + "\vrc=200"},
	{input: "password=aGVsbG8.cvc=\vrc=200", want: "password=" + SecretRedactionMarker + "\vrc=200"},
	{input: "password=xY9_key=\vrc=200", want: "password=" + SecretRedactionMarker + "\vrc=200"},
	{input: "password=abc_token=\vrc=200", want: "password=" + SecretRedactionMarker + "\vrc=200"},

	// The punctuation-led and vendor-word names, same shape and same reason.
	{input: "password=[cpf=\vrc=200", want: "password=" + SecretRedactionMarker + "\vrc=200"},
	{input: "password=!password=\vrc=200", want: "password=" + SecretRedactionMarker + "\vrc=200"},
	{input: `password="cpf"=` + "\vrc=200", want: "password=" + SecretRedactionMarker + "\vrc=200"},
	{input: "password=(cpf=\vrc=200", want: "password=" + SecretRedactionMarker + "\vrc=200"},
	{input: "password=<cvc=\vrc=200", want: "password=" + SecretRedactionMarker + "\vrc=200"},
	{input: "password={secret=\vrc=200", want: "password=" + SecretRedactionMarker + "\vrc=200"},
	{input: "password=hunter2@CVC=\vrc=200", want: "password=" + SecretRedactionMarker + "\vrc=200"},

	// THE KNOWN GAP, spelled out above: a credential the head prints and the
	// space analogue prints on every head.
	{input: `password="cpf"=` + "\vhunter2", want: "password=" + SecretRedactionMarker + "\vhunter2"},

	// The FuzzString seed for the query pass's own vertical tab, which is a
	// direction input because the corpus file is a test source. There is no
	// credential on this line at all: 714ca9e read the whole run of quotes and
	// vertical tabs as ONE value and put a marker over it, and the head stops
	// at the first '\v' like every other whitespace byte.
	{input: "&Cpf=\"\v\"\v\"\v\"\v", want: "&Cpf=" + SecretRedactionMarker + "\v\"\v\"\v\"\v"},
}

// TestStringNeverNarrowsAgainstThePinnedBase IS THE GATE THAT WOULD HAVE CAUGHT
// THE REGRESSION IN 340fb0d, AND THE ONE THIS PACKAGE DID NOT HAVE.
//
// The only direction assertion here before it compared redactCardNumbers
// against an in-file copy of the older card patterns. It never calls String, so
// "narrowed == 0" said nothing about the URL pass, the key=value walker or the
// round loop — and 340fb0d duly shipped five operator shapes that went from
// redacted to printed in the clear, under a green suite, a green fuzzer and a
// stable fixed point.
//
// The reference is 714ca9e, the last head with no known regression. String's
// output for every input is pinned in a file produced from a PRISTINE COPY of
// that commit, never from this worktree:
//
//	d=/tmp/dir-base
//	GIT_INDEX_FILE=$d.idx git read-tree 714ca9e
//	GIT_INDEX_FILE=$d.idx git checkout-index -a --prefix=$d/
//	cp commons/security/sanitize/testdata/direction/direction_base_main.go \
//	   $d/commons/security/sanitize/
//	(cd $d && go run ./commons/security/sanitize/direction_base_main.go \
//	   <inputs.txt> <base-714ca9e.txt>)
//	rm -rf $d $d.idx
//
// To regenerate the inputs after adding a test literal or an operator line, run
// this test once with SANITIZE_DIRECTION_REGEN=1, then redo the command above.
// Both files are regenerated in full; nothing is edited by hand.
//
// THE ASSERTION IS DIRECTION, NOT EQUALITY. Later passes are meant to redact
// more, so an output that differs from the base is only a defect when clear text
// the base had removed comes back — that is, when the head's residue is not a
// subsequence of the base's.
func TestStringNeverNarrowsAgainstThePinnedBase(t *testing.T) {
	t.Parallel()

	generated := directionInputs(t)
	require.NotEmpty(t, generated, "the input generators contributed nothing")

	if os.Getenv("SANITIZE_DIRECTION_REGEN") != "" {
		var b strings.Builder
		for _, in := range generated {
			b.WriteString(strconv.Quote(in))
			b.WriteByte('\n')
		}

		if err := os.WriteFile(directionInputPath, []byte(b.String()), 0o600); err != nil {
			t.Fatalf("write %s: %v", directionInputPath, err)
		}

		t.Fatalf("regenerated %s with %d inputs; now redo the base command in this test's comment",
			directionInputPath, len(generated))
	}

	inputs := readQuotedLines(t, directionInputPath)
	base := readQuotedLines(t, directionBasePath)

	require.Len(t, base, len(inputs),
		"the pinned base has one output per input; regenerate both files together")

	// THE PINNED SET MUST STILL COVER THE SOURCES IT WAS BUILT FROM. A harness
	// that quietly stops seeing the shapes a test file added is the failure this
	// package has already shipped once, under the name "a harness that cannot
	// fail is not evidence".
	pinned := make(map[string]bool, len(inputs))
	for _, in := range inputs {
		pinned[in] = true
	}

	var missing []string

	for _, in := range generated {
		if !pinned[in] {
			missing = append(missing, in)
		}
	}

	require.Empty(t, missing,
		"%d generated input(s) are not in the pinned set, first %q; re-run with SANITIZE_DIRECTION_REGEN=1 and redo the base command",
		len(missing), firstOrEmpty(missing))

	exempt := make(map[string]string, len(directionBaseOverReach))
	for _, row := range directionBaseOverReach {
		exempt[row.input] = row.credential
	}

	keep := make(map[string]string, len(directionBaseKept))
	for _, row := range directionBaseKept {
		keep[row.input] = row.want
	}

	narrowed, widened, exempted, keptClear := 0, 0, 0, 0

	for i, in := range inputs {
		got := String(in)
		if got == base[i] {
			continue
		}

		if isTokenSubsequence(redactionResidue(got), redactionResidue(base[i])) {
			widened++

			continue
		}

		if want, ok := keep[in]; ok {
			keptClear++

			// THE ROW CARRIES THE WHOLE OUTPUT, NOT THE TEXT IT KEEPS.
			// "contains" was the first form and it was a hole: a mutant that
			// printed a credential-shaped value WHOLE on this family left all
			// thirteen rows of the day still containing their response code,
			// and the exemption absorbed the leak it was written beside. An
			// equality fails on one byte too few and on one byte too many, and
			// these are fixed inputs, so it costs nothing to demand it.
			require.Equal(t, want, got,
				"%q is exempt only because the head's output is this exact string; it changed, so re-read the row and delete or update it",
				in)

			continue
		}

		if credential, ok := exempt[in]; ok {
			exempted++

			// THE EXEMPTION ASSERTS CREDENTIAL ABSENCE, NOT OUTPUT EQUALITY.
			// These are the rows where the base removed different bytes than
			// the head does, so neither residue contains the other; what the
			// exemption is worth is that the credential is not in the head's
			// output at all.
			require.NotContains(t, got, credential,
				"%q is exempt from the direction rule only because the head still redacts the credential; it no longer does",
				in)

			continue
		}

		narrowed++

		t.Errorf("NARROWED in=%q\n  base=%q\n  head=%q", in, base[i], got)
	}

	require.Zero(t, narrowed, "String must never leave clear text the pinned base removed")
	require.Len(t, directionBaseOverReach, exempted,
		"every exemption must still be a live difference; a stale row hides nothing and must be deleted")
	require.Len(t, directionBaseKept, keptClear,
		"every kept-clear row must still be a live difference; a stale row hides nothing and must be deleted")

	t.Logf("DIRECTION %d inputs against the pinned 714ca9e base: %d narrowed, %d widened, %d exempt, %d kept-clear",
		len(inputs), narrowed, widened, exempted, keptClear)
}

func firstOrEmpty(s []string) string {
	if len(s) == 0 {
		return ""
	}

	return s[0]
}

// legacyRedactKeyValuePairs and legacyRedactKeyValuePair are the SAME DECISIONS
// the production pair makes, expressed the way they were expressed before the
// chain walk: rewind to the value and re-run the full pattern, recurse into it
// with ReplaceAllStringFunc.
//
// The rewrite is two transformations of one idea — carry the value's end along
// instead of re-deriving it, and walk the nested keys instead of rewinding or
// recursing into them — and neither is allowed to change which span is redacted.
// That claim is worth nothing asserted; this is where it is measured.
//
// THE EXTENSION IS CALLED, NOT COPIED. sensitiveValueEnd decides how far a
// sensitive key's redaction reaches, and it is a decision rather than a shape,
// so both sides ask it the same question. What this test measures is the walk:
// which span each shape redacts, given the same answers.
//
// WHICH IS WHY THE DECISIONS ARE COPIED, NOT FROZEN, AND WHY THE NAME SAYS
// "THE SAME SPANS" RATHER THAN "THE IMPLEMENTATION IT REPLACED". This is a
// differential on the WALK — it proves the two shapes agree GIVEN the same
// decisions, and nothing more. Freezing the old decisions instead would make
// this test pin the defects they have since been corrected for, and it duly
// went red the moment one was: "b=aGVsbG8 = rg=x.y=password=" kept its value
// under the frozen version. What guards the decisions themselves is the pinned
// direction harness, which compares String's output against a base this
// worktree cannot edit.
func legacyRedactKeyValuePairs(s string) string {
	var out strings.Builder

	pos := 0

	for pos < len(s) {
		loc := keyValuePattern.FindStringSubmatchIndex(s[pos:])
		if loc == nil {
			break
		}

		for i := range loc {
			if loc[i] >= 0 {
				loc[i] += pos
			}
		}

		key, valueStart, valueEnd := s[loc[2]:loc[3]], loc[6], loc[7]
		sensitive := isSensitiveFieldName(key)

		// Nothing is handed back from under a sensitive key: its value is the
		// credential under one reading of the line and the next pair's key
		// under the other, and both go under the marker.
		rewind := !sensitive &&
			((s[valueEnd-1] == '=' && keyPrefixPattern.MatchString(s[valueStart:valueEnd])) ||
				nextPairSeparatorPattern.MatchString(s[valueEnd:]))

		if rewind {
			out.WriteString(s[pos:valueStart])

			pos = valueStart

			continue
		}

		end := valueEnd

		replacement := SecretRedactionMarker
		if sensitive {
			end = sensitiveValueEnd(s, valueStart, valueEnd)
		} else {
			replacement = keyValuePattern.ReplaceAllStringFunc(s[valueStart:valueEnd], legacyRedactKeyValuePair)
		}

		out.WriteString(s[pos:valueStart])
		out.WriteString(replacement)

		pos = end
	}

	out.WriteString(s[pos:])

	return out.String()
}

func legacyRedactKeyValuePair(match string) string {
	loc := keyValuePattern.FindStringSubmatchIndex(match)
	if loc == nil {
		return match
	}

	key, value := match[loc[2]:loc[3]], match[loc[6]:loc[7]]

	replacement := SecretRedactionMarker
	if !isSensitiveFieldName(key) {
		replacement = keyValuePattern.ReplaceAllStringFunc(value, legacyRedactKeyValuePair)
	}

	return match[:loc[6]] + replacement + match[loc[7]:]
}

// keyChainLines builds the shape the rewrite exists for: a separator-free run of
// nested keys, with and without a field name somewhere down the chain.
//
// THE CHAIN IS THE AXIS THE OTHER GENERATORS DO NOT HAVE. urlAndPairLines
// produces at most two keys in a row, so it never reaches the level where the
// old code re-measured the same tail; every difference between the two
// implementations, if there is one, lives here.
func keyChainLines(t *testing.T, n int) []string {
	t.Helper()

	rng := rand.New(rand.NewSource(20260912))

	names := []string{"a", "b", "opt", "ref", "cpf", "password", "rg", "token", "x.y", "k-1", "aGVsbG8", "abc_token"}
	tails := []string{
		"", "=", " hunter2", "hunter2", " ", "= 0", "&x", ",y", ";z", " =0", "\t",
		// The chain ending in the separator, in the separator plus one of the
		// whitespace bytes the two patterns used to disagree about, and with a
		// non-word byte in front of the field name in the value slot. The tab
		// is here because the shipped operator line "pgx: opt=password= cpf\t="
		// was unreachable from this generator's separator set.
		"\v", "=\v", "!password= hunter2", "= hunter2", "=,next=1", "= rc=200",
		// A value that is nothing but the separator twice, and one with a byte
		// in front of it. The chain step asks whether the nested key's match
		// ends BEFORE the value ends, and with no "k=a==" shape anywhere in the
		// corpus an off-by-one there survived all of this differential.
		"==", "a==",
	}

	out := make([]string, 0, n)

	for range n {
		var b strings.Builder

		for links := rng.Intn(12) + 1; links > 0; links-- {
			b.WriteString(names[rng.Intn(len(names))])
			separators := []string{"=", " =", "= ", " = ", "=\v", "\v=", "=\t", "\t=", " \t="}

			b.WriteString(separators[rng.Intn(len(separators))])
		}

		b.WriteString(tails[rng.Intn(len(tails))])

		out = append(out, b.String())
	}

	return out
}

func TestTheWalkRedactsTheSameSpansTheRecursionDid(t *testing.T) {
	t.Parallel()

	groups := []struct {
		name   string
		inputs []string
	}{
		{"committed fuzz corpus", corpusEntries(t)},
		{"every string literal in the test sources", literalsFromTestSources(t)},
		{"the pinned direction inputs", readQuotedLines(t, directionInputPath)},
		{"URL authorities and key=value chains", urlAndPairLines(t, 5000)},
		{"seeded nested key chains", keyChainLines(t, 5000)},
		{"the six measurement shapes at 1 KiB", measurementShapes(1024)},
	}

	total := 0

	for _, g := range groups {
		require.NotEmpty(t, g.inputs, "%s contributed no inputs; the differential would be vacuous", g.name)

		for _, in := range g.inputs {
			if got, want := redactKeyValuePairs(in), legacyRedactKeyValuePairs(in); got != want {
				t.Fatalf("%s: redactKeyValuePairs(%q)\n got  %q\n want %q", g.name, in, got, want)
			}

			total++
		}

		t.Logf("DIFFERENTIAL %-44s %5d inputs, 0 diffs", g.name, len(g.inputs))
	}

	t.Logf("DIFFERENTIAL TOTAL %d inputs, 0 diffs", total)
}

// TestTheTwoKeyPatternsAgreeOnWhereTheValueStarts pins the invariant the walk
// RESTS ON and that nothing else measures: keyPrefixPattern ends exactly where
// keyValuePattern's value begins.
//
// The walker carries a value's end along and asks keyPrefixPattern for the next
// key inside it, comparing that match's END against that end. The two patterns
// are built from one separator constant so they cannot disagree about the
// separator — but the full pattern's value class is what forces the separator to
// give back a trailing whitespace byte, and keyPrefixPattern has nothing to
// force it with. One byte of drift there ('\v', which [[:space:]] holds and
// RE2's \s does not) made every comparison off by one and left
// "k=k=pwd=\v" in the log in full.
//
// IT ALSO PINS THE WORD BOUNDARY. Remove \b from keyPrefixPattern and it finds
// "password=" inside "0password=hunter2", where the full pattern finds no pair
// at all — the walker would then step onto a key that is not a key and redact a
// value nothing owns. That mutant survives every other test in this package.
//
// AND IT PINS THE NAME CLASS TEXTUALLY, which the inputs cannot. Both patterns
// are built from keyValueName now, and a behavioural row can only catch a copy
// that has already drifted from it — by which time the drift is the defect. The
// assertion below is what goes red at the moment someone widens the constant
// (admitting ':', say) and leaves a hand-spelled copy behind: the copy no
// longer contains the constant's text. Spelling today's class out by hand is
// behaviourally identical and nothing can catch it, which is the honest limit
// of this guard.
func TestTheTwoKeyPatternsAgreeOnWhereTheValueStarts(t *testing.T) {
	t.Parallel()

	require.Contains(t, keyPrefixPattern.String(), keyValueName,
		"keyPrefixPattern must be built from keyValueName, not from a copy of it")
	require.Contains(t, keyValuePattern.String(), keyValueName,
		"keyValuePattern must be built from keyValueName, not from a copy of it")

	// A key must start on a word boundary, so a word byte in front of a name
	// means there is no pair. The long s and the Kelvin sign fold to ASCII under
	// (?i) while \b, an ASCII rule, does not count them as word characters.
	boundary := []string{
		"0password=", "0password=hunter2", "1cpf=", "1cpf=12345678901",
		"00\u017f00\u017f=", "00\u017f00\u017f=0", "\u212a=", "\u212a=hunter2",
		"pwd=\v", "pwd=\v ", "a=b=pwd=\v", "password=\vhunter2",

		// One row per byte the shared name class admits, so a narrowed
		// keyValueName moves both patterns or fails here.
		"a9=0", "a.b=0", "a_b=0", "a-b=0", "k=a_b=0", "k=a.b=hunter2",
	}

	groups := [][]string{
		boundary,
		corpusEntries(t),
		literalsFromTestSources(t),
		readQuotedLines(t, directionInputPath),
		urlAndPairLines(t, 2000),
		keyChainLines(t, 2000),
	}

	valueByte := regexp.MustCompile(`^` + keyValueValue)
	checked := 0

	for _, group := range groups {
		for _, in := range group {
			// Every value is strictly shorter than the text holding it — a key
			// and a separator are at least two bytes — so the walk terminates.
			queue := []string{in}

			for len(queue) > 0 {
				text := queue[0]
				queue = queue[1:]

				prefix := keyPrefixPattern.FindStringSubmatchIndex(text)
				if prefix == nil {
					continue
				}

				checked++

				pair := keyValuePattern.FindStringSubmatchIndex(text)
				if pair != nil && pair[0] == prefix[0] {
					require.Equal(t, pair[6], prefix[1],
						"%q: keyPrefixPattern ends at %d, the value starts at %d",
						text, prefix[1], pair[6])
					require.Equal(t, text[pair[2]:pair[3]], text[prefix[2]:prefix[3]],
						"%q: the two patterns read a different key", text)
				} else {
					require.False(t, valueByte.MatchString(text[prefix[1]:]),
						"%q: keyPrefixPattern found a key at %d where the full pattern finds no pair",
						text, prefix[0])
				}

				if pair != nil {
					queue = append(queue, text[pair[6]:pair[7]])
				}
			}
		}
	}

	t.Logf("AGREEMENT %d texts carrying a key", checked)
}

// legacyPemBlockPattern is the single pattern redactPemBlocks replaced: one
// block, with the well-formed reading preferred as a lazy run to the first END
// line and the headless body as the fallback.
var legacyPemBlockPattern = regexp.MustCompile(
	`(?s)-----(?i:BEGIN [A-Z0-9 ]+)-----(?:.*?-----(?i:END [A-Z0-9 ]+)-----|[\sA-Za-z0-9+/=]*)`)

// legacyPemBlockPatternVerticalTab is the same pattern with the headless body's
// whitespace corrected from RE2's \s to the whole of [[:space:]], and it is the
// ONE deliberate difference between redactPemBlocks and the pattern it replaced.
//
// THE DIFFERENTIAL MEASURES THE WALK, NOT THE CLASS. Its claim is that finding
// both armor lines once and pairing them is the same READING as the lazy run —
// and on a headless body that begins after a vertical tab the old reading
// stopped at the armor line and printed the base64, which is the defect the
// production class was corrected for. Comparing those inputs against the old
// class would pin the leak; comparing them against a lazy run that reads the
// body the corrected way keeps the differential asking about the walk.
//
// Inputs where the two agree are compared against the original, so the
// exemption cannot spread past the class it names, and the count below goes red
// if the shape ever stops being reachable.
var legacyPemBlockPatternVerticalTab = regexp.MustCompile(
	`(?s)-----(?i:BEGIN [A-Z0-9 ]+)-----(?:.*?-----(?i:END [A-Z0-9 ]+)-----|[[:space:]A-Za-z0-9+/=]*)`)

// TestRedactPemBlocksMatchesThePatternItReplaced measures the claim that finding
// both armor lines once and pairing them is the SAME reading as the lazy run,
// only without its cost. The generated shapes are the ones where a merge walk
// and a per-block search could plausibly differ: a block with no END line, an
// END line before any BEGIN, a BEGIN nested inside a block, two blocks on one
// line, RFC 1421 headers whose '-' the body class refuses, and lowercase armor.
func TestRedactPemBlocksMatchesThePatternItReplaced(t *testing.T) {
	t.Parallel()

	const (
		begin = "-----BEGIN RSA PRIVATE KEY-----"
		end   = "-----END RSA PRIVATE KEY-----"
		body  = "MIIEpAIBAAKCAQEA7Zx+\nQ2l0Zg==\n"
	)

	generated := []string{
		begin, end, begin + body + end, begin + body, body + end,
		end + begin, end + begin + body, begin + begin + body + end,
		begin + body + end + " rc=200 " + begin + body + end,
		begin + body + end + begin + body,
		begin + "Proc-Type: 4,ENCRYPTED\nDEK-Info: DES-EDE3-CBC,0A1B\n\n" + body + end,
		begin + "Proc-Type: 4,ENCRYPTED\n" + body,
		strings.ToLower(begin) + body + strings.ToLower(end),
		strings.ToLower(begin) + body,
		"-----BEGIN A-----", "-----BEGIN A----------END A-----",
		"-----BEGIN A-----END A-----", "-----END A----------BEGIN B-----",
		// The two shapes the cost fix was measured on and its commit body did
		// not name: a stray END in front of a run of BEGIN lines, and armor as
		// the value of a key=value pair, repeated. Both cost 6.3 s before the
		// pairing walk and 0.015 s after, and a row here is cheaper than two
		// more wall-clock bound tests.
		"-----END A-----" + strings.Repeat("-----BEGIN A-----", 8),
		strings.Repeat("k=-----BEGIN A----- ", 8),
		"host=db " + begin + body + " password=hunter2",
		strings.Repeat("-----BEGIN A-----", 8),
		strings.Repeat(begin+body+end, 4),
	}

	groups := [][]string{
		generated,
		corpusEntries(t),
		literalsFromTestSources(t),
		readQuotedLines(t, directionInputPath),
	}

	total, bodyClass := 0, 0

	for _, group := range groups {
		for _, in := range group {
			if !strings.Contains(in, "-----") {
				continue
			}

			want := legacyPemBlockPattern.ReplaceAllString(in, SecretRedactionMarker)

			if corrected := legacyPemBlockPatternVerticalTab.ReplaceAllString(in, SecretRedactionMarker); corrected != want {
				want = corrected
				bodyClass++
			}

			if got := redactPemBlocks(in); got != want {
				t.Fatalf("redactPemBlocks(%q)\n got  %q\n want %q", in, got, want)
			}

			total++
		}
	}

	require.NotZero(t, bodyClass,
		"no input reaches a headless body behind a vertical tab any more; the exemption is stale and must be deleted")

	t.Logf("PEM DIFFERENTIAL %d armor-shaped inputs, 0 diffs, %d compared against the corrected body class",
		total, bodyClass)
}
