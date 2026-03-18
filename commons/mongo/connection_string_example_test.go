//go:build unit

package mongo_test

import (
	"fmt"
	"net/url"

	"github.com/LerianStudio/lib-commons/v4/commons/mongo"
)

func ExampleBuildURI() {
	query := url.Values{}
	query.Set("replicaSet", "rs0")

	uri, err := mongo.BuildURI(mongo.URIConfig{
		Scheme:   "mongodb",
		Username: "app",
		Password: "EXAMPLE_DO_NOT_USE",
		Host:     "db.internal",
		Port:     "27017",
		Database: "ledger",
		Query:    query,
	})

	fmt.Println(err == nil)
	fmt.Println(uri)

	// Output:
	// true
	// mongodb://app:EXAMPLE_DO_NOT_USE@db.internal:27017/ledger?authSource=admin&replicaSet=rs0
}
