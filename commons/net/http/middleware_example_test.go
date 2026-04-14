//go:build unit

package http_test

import (
	"encoding/base64"
	"fmt"
	"net/http/httptest"

	uhttp "github.com/LerianStudio/lib-commons/v5/commons/net/http"
	"github.com/gofiber/fiber/v2"
)

func ExampleWithBasicAuth() {
	app := fiber.New()
	app.Use(uhttp.WithBasicAuth(uhttp.FixedBasicAuthFunc("fred", "secret"), "admin"))
	app.Get("/private", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusNoContent)
	})

	unauthorizedReq := httptest.NewRequest("GET", "/private", nil)
	unauthorizedResp, _ := app.Test(unauthorizedReq)

	authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte("fred:secret"))
	authorizedReq := httptest.NewRequest("GET", "/private", nil)
	authorizedReq.Header.Set("Authorization", authHeader)
	authorizedResp, _ := app.Test(authorizedReq)

	fmt.Println(unauthorizedResp.StatusCode)
	fmt.Println(authorizedResp.StatusCode)

	// Output:
	// 401
	// 204
}
