//go:build unit

package opentelemetry_test

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
)

func ExampleObfuscateStruct_customRules() {
	redactor, err := opentelemetry.NewRedactor([]opentelemetry.RedactionRule{
		{FieldPattern: `(?i)^password$`, Action: opentelemetry.RedactionMask},
		{FieldPattern: `(?i)^email$`, Action: opentelemetry.RedactionHash},
	}, "***")
	if err != nil {
		fmt.Println("invalid rules")
		return
	}

	masked, err := opentelemetry.ObfuscateStruct(map[string]any{
		"name":     "alice",
		"email":    "a@b.com",
		"password": "secret",
	}, redactor)
	if err != nil {
		fmt.Println("obfuscation failed")
		return
	}

	m := masked.(map[string]any)

	// password is masked, name is unchanged, email is HMAC-hashed with sha256: prefix
	fmt.Println("name:", m["name"])
	fmt.Println("password:", m["password"])
	fmt.Println("email_prefix:", strings.HasPrefix(m["email"].(string), "sha256:"))

	// Verify the JSON round-trips cleanly
	b, _ := json.Marshal(masked)
	fmt.Println("json_ok:", len(b) > 0)

	// Output:
	// name: alice
	// password: ***
	// email_prefix: true
	// json_ok: true
}
