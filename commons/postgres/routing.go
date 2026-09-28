package postgres

import (
	"regexp"

	"github.com/bxcodec/dbresolver/v2"
)

var (
	leadingSelectPattern = regexp.MustCompile(`(?is)^(?:\s+|--[^\n]*(?:\n|$)|/\*.*?\*/)*SELECT\b`)
	rowLockPattern       = regexp.MustCompile(`(?i)\bFOR\s+(?:NO\s+KEY\s+UPDATE|KEY\s+SHARE|UPDATE|SHARE)\b`)
)

// replicaReadChecker sends a query to the replica only when its first keyword is SELECT and it
// takes no row lock; every other statement, a read opening with WITH included, reaches the primary.
type replicaReadChecker struct{}

func (replicaReadChecker) Check(query string) dbresolver.QueryType {
	if leadingSelectPattern.MatchString(query) && !rowLockPattern.MatchString(query) {
		return dbresolver.QueryTypeRead
	}

	return dbresolver.QueryTypeWrite
}
