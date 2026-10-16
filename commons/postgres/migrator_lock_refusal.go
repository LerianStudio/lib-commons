package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/LerianStudio/lib-commons/v7/commons/backoff"
	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/source"
)

// refusalMigrator is the part of *migrate.Migrate that
// applyMigrationsRetrying drives; the seam lets it be unit-tested with a fake.
type refusalMigrator interface {
	migrationStepper
	migrationVersionReader
	Force(version int) error
}

// applyMigrationsRetrying is applyMigrations, run again up to lockRetries
// more times while a migration is refused by the session's lock_timeout
// (SQLSTATE 55P03): a table lock the application holds outlasted the bound.
//
// golang-migrate marks a migration dirty before running it and leaves the
// mark when it fails, so a refused migration would block every later run.
// When the refused migration provably ran as one transaction, the refusal
// undid all of it, and clearRefusedMigration sets the version back to the
// last applied migration before the pause; it does so after the last refusal
// too, so a run whose retries ran out leaves the database clean for the next
// one. A migration that may have committed part of itself is left dirty and
// returned at once. The migration lock stays held through the pause. A
// context that ends during the pause returns nil, and the caller reports the
// stop exactly as for a stop between migrations.
func applyMigrationsRetrying(ctx context.Context, mig refusalMigrator, src source.Driver, run migrationRun) error {
	for retry := 0; ctx.Err() == nil; retry++ {
		err := applyMigrations(ctx, mig)
		if err == nil || run.lockRetries == 0 || !refusedByLockTimeout(err) {
			return err
		}

		if !clearRefusedMigration(ctx, mig, src, run) {
			return err
		}

		if retry == run.lockRetries {
			migrationLogAtLevel(ctx, run.logger, obs.LevelError, "migration refused by lock_timeout; retries exhausted",
				"attempts", retry+1)

			return err
		}

		pause := migrationLockRetryPause(run.lockRetryBackoff, retry+1)

		migrationLogAtLevel(ctx, run.logger, obs.LevelWarn, "migration refused by lock_timeout; retrying",
			"retry", retry+1, "of", run.lockRetries, "pause", pause.String())

		// A pause cut by ctx ends the loop; the caller reports the stop.
		_ = backoff.WaitContext(ctx, pause)
	}

	return nil
}

// refusedByLockTimeout reports whether err is PostgreSQL's lock_timeout
// refusal, also when golang-migrate wrapped it in a database.Error, which
// does not unwrap.
func refusedByLockTimeout(err error) bool {
	if matchesSQLState(err, lockNotAvailable) {
		return true
	}

	var byValue database.Error
	if errors.As(err, &byValue) {
		return matchesSQLState(byValue.OrigErr, lockNotAvailable)
	}

	var byPointer *database.Error
	if errors.As(err, &byPointer) && byPointer != nil {
		return matchesSQLState(byPointer.OrigErr, lockNotAvailable)
	}

	return false
}

// clearRefusedMigration sets a refused migration's dirty version back to the
// version before it in the source (never version-1: sources have gaps), or
// to no version when it was the first, and reports whether the run may go
// on. A clean version (the refusal came before the migration was marked)
// needs nothing. The mark stays, and false is returned, when the migration
// may have committed part of itself: every statement commits on its own
// under AllowMultiStatements, and otherwise the file must pass
// runsAsOneTransaction.
func clearRefusedMigration(ctx context.Context, mig refusalMigrator, src source.Driver, run migrationRun) bool {
	version, dirty, err := mig.Version()
	if errors.Is(err, migrate.ErrNilVersion) || (err == nil && !dirty) {
		return true
	}

	if err != nil {
		migrationLogAtLevel(ctx, run.logger, obs.LevelError, "migration refused by lock_timeout; version unreadable, left as is",
			"error", run.redactor.redact(err.Error()))

		return false
	}

	label := strconv.FormatUint(uint64(version), 10)

	if reason := partialCommitRisk(src, version, run); reason != "" {
		migrationLogAtLevel(ctx, run.logger, obs.LevelError,
			"migration refused by lock_timeout is not provably one transaction; left dirty for an operator",
			"dirty_version", label, "reason", reason)

		return false
	}

	target, err := versionBefore(src, version)
	if err == nil {
		err = mig.Force(target)
	}

	if err != nil {
		migrationLogAtLevel(ctx, run.logger, obs.LevelError, "migration refused by lock_timeout; dirty mark not cleared",
			"dirty_version", label, "error", run.redactor.redact(err.Error()))

		return false
	}

	migrationLogAtLevel(ctx, run.logger, obs.LevelWarn, "migration refused by lock_timeout rolled back whole; version reset",
		"dirty_version", label, "version", strconv.Itoa(target))

	return true
}

// partialCommitRisk names why migration version may have committed part of
// itself, or returns "" when it ran as one transaction.
func partialCommitRisk(src source.Driver, version uint, run migrationRun) string {
	if run.allowMultiStatements {
		return "AllowMultiStatements commits each statement on its own"
	}

	body, err := readUpMigration(src, version)
	if err != nil {
		return "its source could not be read: " + run.redactor.redact(err.Error())
	}

	if !runsAsOneTransaction(body) {
		return "it commits on its own or runs outside a transaction"
	}

	return ""
}

func readUpMigration(src source.Driver, version uint) (string, error) {
	reader, _, err := src.ReadUp(version)
	if err != nil {
		return "", err
	}
	defer reader.Close()

	body, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// versionBefore is the source's migration before version, or
// database.NilVersion when version is the first.
func versionBefore(src source.Driver, version uint) (int, error) {
	prev, err := src.Prev(version)
	if errors.Is(err, fs.ErrNotExist) {
		return database.NilVersion, nil
	}

	if err != nil {
		return 0, err
	}

	if prev > math.MaxInt {
		return 0, fmt.Errorf("migration version %d out of range", prev)
	}

	return int(prev), nil
}

// runsAsOneTransaction reports whether a migration file, sent to PostgreSQL
// as one simple query (golang-migrate's single-statement mode), runs as one
// transaction, so a refusal anywhere in it undoes all of it. PostgreSQL runs
// such a query as one implicit transaction unless the file ends a
// transaction itself (COMMIT or END before its last statement, ROLLBACK,
// ABORT, PREPARE TRANSACTION). Some statements run outside any transaction
// and keep partial work when refused: CONCURRENTLY builds leave an invalid
// index, and a DO or CALL alone in its file may commit inside its body,
// which this check cannot see. Any of those makes it false. Statements are
// read outside comments, string literals, quoted identifiers and
// dollar-quoted bodies; a false positive only leaves a refused migration
// dirty, as without the retry.
func runsAsOneTransaction(sql string) bool {
	statements := topLevelStatements(sql)

	for i, words := range statements {
		if slices.Contains(words, "CONCURRENTLY") {
			return false
		}

		switch words[0] {
		case "ROLLBACK", "ABORT", "PREPARE":
			return false
		case "COMMIT", "END":
			if i != len(statements)-1 {
				return false
			}
		case "DO", "CALL":
			if len(statements) == 1 {
				return false
			}
		}
	}

	return true
}

// topLevelStatements splits sql at its top-level semicolons into statements,
// each the upper-cased words found outside comments, string literals, quoted
// identifiers and dollar-quoted bodies. Statements with no words are dropped.
func topLevelStatements(sql string) [][]string {
	var (
		statements [][]string
		words      []string
	)

	for i := 0; i < len(sql); {
		c := sql[i]

		switch {
		case strings.HasPrefix(sql[i:], "--"):
			i = skipLineComment(sql, i)
		case strings.HasPrefix(sql[i:], "/*"):
			i = skipBlockComment(sql, i)
		case c == '\'':
			i = skipQuoted(sql, i, '\'', false)
		case c == '"':
			i = skipQuoted(sql, i, '"', false)
		case c == '$':
			i = skipDollar(sql, i)
		case c == ';':
			if len(words) > 0 {
				statements = append(statements, words)
				words = nil
			}

			i++
		case isSQLWordStart(c):
			end := i
			for end < len(sql) && isSQLWordChar(sql[end]) {
				end++
			}

			word := strings.ToUpper(sql[i:end])

			// E'...' is an escape string: a backslash escapes its quote.
			if word == "E" && end < len(sql) && sql[end] == '\'' {
				i = skipQuoted(sql, end, '\'', true)

				continue
			}

			words = append(words, word)
			i = end
		default:
			i++
		}
	}

	if len(words) > 0 {
		statements = append(statements, words)
	}

	return statements
}

func skipLineComment(sql string, i int) int {
	if end := strings.IndexByte(sql[i:], '\n'); end >= 0 {
		return i + end + 1
	}

	return len(sql)
}

// skipBlockComment skips a /* */ comment; PostgreSQL nests them.
func skipBlockComment(sql string, i int) int {
	depth := 0

	for i < len(sql) {
		switch {
		case strings.HasPrefix(sql[i:], "/*"):
			depth++
			i += 2
		case strings.HasPrefix(sql[i:], "*/"):
			depth--
			i += 2

			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}

	return len(sql)
}

// skipQuoted skips a literal opened by quote at i; a doubled quote stays
// inside it, and so does a backslash-escaped one when backslashEscapes.
func skipQuoted(sql string, i int, quote byte, backslashEscapes bool) int {
	for i++; i < len(sql); i++ {
		switch {
		case backslashEscapes && sql[i] == '\\':
			i++
		case sql[i] == quote:
			if i+1 < len(sql) && sql[i+1] == quote {
				i++

				continue
			}

			return i + 1
		}
	}

	return len(sql)
}

// skipDollar skips a dollar-quoted body ($$...$$ or $tag$...$tag$) opened
// at i, or just the '$' of anything else (a positional parameter).
func skipDollar(sql string, i int) int {
	end := i + 1
	if end < len(sql) && isSQLWordStart(sql[end]) {
		for end < len(sql) && isSQLWordChar(sql[end]) && sql[end] != '$' {
			end++
		}
	}

	if end >= len(sql) || sql[end] != '$' {
		return i + 1
	}

	delimiter := sql[i : end+1]

	if closing := strings.Index(sql[end+1:], delimiter); closing >= 0 {
		return end + 1 + closing + len(delimiter)
	}

	return len(sql)
}

func isSQLWordStart(c byte) bool {
	return c == '_' || c >= 0x80 || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

func isSQLWordChar(c byte) bool {
	return isSQLWordStart(c) || c == '$' || ('0' <= c && c <= '9')
}
