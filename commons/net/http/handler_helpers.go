package http

import "github.com/gofiber/fiber/v2"

func requireFiberContext(c *fiber.Ctx) error {
	if c == nil {
		return ErrContextNotFound
	}

	return nil
}

func respondJSONMap(c *fiber.Ctx, status int, payload fiber.Map) error {
	if err := requireFiberContext(c); err != nil {
		return err
	}

	return Respond(c, status, payload)
}
