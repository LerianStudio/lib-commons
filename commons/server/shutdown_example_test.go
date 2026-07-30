//go:build unit

package server_test

import (
	"errors"
	"fmt"

	"github.com/LerianStudio/lib-commons/v6/commons/server"
	"github.com/gofiber/fiber/v3"
)

func ExampleServerManager_StartWithGracefulShutdownWithError_validation() {
	sm := server.NewServerManager(nil, nil, nil)
	err := sm.StartWithGracefulShutdownWithError()

	fmt.Println(errors.Is(err, server.ErrNoServersConfigured))

	// Output:
	// true
}

func ExampleServerManager_WithHTTPServerConfig() {
	app := fiber.New()

	sm := server.NewServerManager(nil, nil, nil).
		WithHTTPServerConfig(app, ":3000", fiber.ListenConfig{DisableStartupMessage: true})

	fmt.Println(sm != nil)

	// Output:
	// true
}
