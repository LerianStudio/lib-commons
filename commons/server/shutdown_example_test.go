//go:build unit

package server_test

import (
	"errors"
	"fmt"

	"github.com/LerianStudio/lib-commons/v4/commons/server"
)

func ExampleServerManager_StartWithGracefulShutdownWithError_validation() {
	sm := server.NewServerManager(nil, nil, nil)
	err := sm.StartWithGracefulShutdownWithError()

	fmt.Println(errors.Is(err, server.ErrNoServersConfigured))

	// Output:
	// true
}
