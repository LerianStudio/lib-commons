package mongo

import (
	"maps"
	"regexp"

	"go.mongodb.org/mongo-driver/bson"
)

func mergeFilters(base bson.M, extras ...bson.M) bson.M {
	merged := make(bson.M, len(base)+len(extras)*2)
	maps.Copy(merged, base)

	for _, extra := range extras {
		maps.Copy(merged, extra)
	}

	return merged
}

func regexpIdentifier() *regexp.Regexp {
	return regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
}
