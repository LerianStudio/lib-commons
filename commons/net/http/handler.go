package http

import (
	"github.com/LerianStudio/lib-commons/commons"
	"github.com/gofiber/fiber/v2"
	"log"
	"time"
)

// Ping returns HTTP Status 200 with response "pong".
func Ping(c *fiber.Ctx) error {
	if err := c.SendString("healthy"); err != nil {
		log.Print(err.Error())
	}

	return nil
}

// Version returns HTTP Status 200 with given version.
func Version(c *fiber.Ctx) error {
	return OK(c, fiber.Map{
		"version":     commons.GetenvOrDefault("VERSION", "0.0.0"),
		"requestDate": time.Now().UTC(),
	})
}

// Welcome returns HTTP Status 200 with service info.
func Welcome(service string, description string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"service":     service,
			"description": description,
		})
	}
}

// NotImplementedEndpoint returns HTTP 501 with not implemented message.
func NotImplementedEndpoint(c *fiber.Ctx) error {
	return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "Not implemented yet"})
}

// File servers a specific file.
func File(filePath string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		return c.SendFile(filePath)
	}
}
