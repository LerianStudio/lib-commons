//go:build unit

package redis_test

import (
	"fmt"

	"github.com/LerianStudio/lib-commons/v4/commons/redis"
)

func ExampleConfig() {
	cfg := redis.Config{
		Topology: redis.Topology{
			Standalone: &redis.StandaloneTopology{Address: "redis.internal:6379"},
		},
		Auth: redis.Auth{
			StaticPassword: &redis.StaticPasswordAuth{Password: "redacted"},
		},
	}

	fmt.Println(cfg.Topology.Standalone.Address)
	fmt.Println(cfg.Auth.StaticPassword != nil)

	// Output:
	// redis.internal:6379
	// true
}
