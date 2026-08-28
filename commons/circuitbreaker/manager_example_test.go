//go:build unit

package circuitbreaker_test

import (
	"fmt"
	"github.com/LerianStudio/lib-commons/v6/commons/obs"

	"github.com/LerianStudio/lib-commons/v6/commons/circuitbreaker"
)

func ExampleManager_Execute() {
	mgr, err := circuitbreaker.NewManager(obs.Nop())
	if err != nil {
		return
	}

	_, err = mgr.GetOrCreate("ledger-db", circuitbreaker.DefaultConfig())
	if err != nil {
		return
	}

	result, err := mgr.Execute("ledger-db", func() (any, error) {
		return "ok", nil
	})

	fmt.Println(result, err == nil)
	fmt.Println(mgr.GetState("ledger-db"))

	// Output:
	// ok true
	// closed
}
