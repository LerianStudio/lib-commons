package postgres

import (
	"regexp"

	"github.com/bxcodec/dbresolver/v2"
)

var (
	leadingSelectPattern = regexp.MustCompile(`(?is)^(?:\s+|--[^\n]*(?:\n|$)|/\*.*?\*/)*SELECT\b`)
	primaryOnlyPattern   = regexp.MustCompile(`(?i)\bFOR\s+(?:NO\s+KEY\s+UPDATE|KEY\s+SHARE|UPDATE|SHARE)\b` +
		`|\b(?:nextval|setval|set_config|pg_(?:try_)?advisory_(?:xact_)?lock(?:_shared)?|pg_advisory_unlock(?:_shared)?)\s*\(`)
)

// replicaReadChecker sends a query to the replica only when its first keyword is SELECT and it takes
// no row lock nor calls a sequence, set_config or advisory-lock function; the rest reaches the primary.
type replicaReadChecker struct{}

func (replicaReadChecker) Check(query string) dbresolver.QueryType {
	if leadingSelectPattern.MatchString(query) && !primaryOnlyPattern.MatchString(query) {
		return dbresolver.QueryTypeRead
	}

	return dbresolver.QueryTypeWrite
}
