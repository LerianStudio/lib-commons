//go:build unit

package redis_test

import (
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/redis"
)

func ExampleConfig_gcpIAM() {
	cfg := redis.Config{
		Topology: redis.Topology{
			Standalone: &redis.StandaloneTopology{Address: "redis.internal:6379"},
		},
		Auth: redis.Auth{
			GCPIAM: &redis.GCPIAMAuth{
				CredentialsBase64: "BASE64_JSON",
				ServiceAccount:    "svc-redis@project.iam.gserviceaccount.com",
				RefreshEvery:      50 * time.Minute,
			},
		},
	}

	fmt.Println(cfg.Auth.GCPIAM != nil)
	fmt.Println(cfg.Auth.GCPIAM.ServiceAccount)

	// Output:
	// true
	// svc-redis@project.iam.gserviceaccount.com
}
