//go:build unit

package signedcursor_test

import (
	"errors"
	"fmt"

	"github.com/LerianStudio/lib-commons/v7/commons/net/http/signedcursor"
)

// A list endpoint pages a tenant-scoped aggregate. The cursor carries the
// ordering tuple of the last row served, bound to the identity the page was read
// as and the window it was read over.
//
// The handler re-resolves both from the validated JWT and the request on every
// page and only COMPARES them; it never reads either out of the cursor.
func ExampleCodec() {
	// In production the key comes from the secret manager, 32 raw bytes.
	key := make([]byte, signedcursor.KeySize)
	for i := range key {
		key[i] = byte(i)
	}

	codec, err := signedcursor.New(key)
	if err != nil {
		fmt.Println("codec:", err)

		return
	}

	// Resolved from the validated JWT and the request, never from the cursor.
	binding := signedcursor.Binding{
		Identity: []string{"tenant-a", "scope-reader"},
		Context:  []string{"2026-09-01T00:00:00Z", "2026-09-11T00:00:00Z"},
	}

	token, err := codec.Encode([]byte(`{"rank":42,"id":"sub_01"}`), binding)
	if err != nil {
		fmt.Println("encode:", err)

		return
	}

	payload, err := codec.Decode(token, binding)
	fmt.Println("same identity and window:", err == nil, string(payload))

	// The same token presented by another tenant is refused, not re-based onto
	// the new identity.
	replayed := signedcursor.Binding{
		Identity: []string{"tenant-b", "scope-reader"},
		Context:  binding.Context,
	}

	_, err = codec.Decode(token, replayed)
	fmt.Println("replayed across tenants:", errors.Is(err, signedcursor.ErrIdentityMismatch))

	// So is the same token presented over a different window.
	rewindowed := signedcursor.Binding{
		Identity: binding.Identity,
		Context:  []string{"2026-08-01T00:00:00Z", "2026-09-11T00:00:00Z"},
	}

	_, err = codec.Decode(token, rewindowed)
	fmt.Println("different window:", errors.Is(err, signedcursor.ErrContextMismatch))

	// Every rejection above wraps one parent, so a handler maps the family once.
	fmt.Println("maps to one parent:", errors.Is(err, signedcursor.ErrInvalidCursor))

	// Output:
	// same identity and window: true {"rank":42,"id":"sub_01"}
	// replayed across tenants: true
	// different window: true
	// maps to one parent: true
}

// A token edited in flight fails verification before any byte of its body is
// parsed.
func ExampleCodec_Decode_tampered() {
	codec, err := signedcursor.New(make([]byte, signedcursor.KeySize))
	if err != nil {
		fmt.Println("codec:", err)

		return
	}

	binding := signedcursor.Binding{Identity: []string{"tenant-a"}}

	token, err := codec.Encode([]byte(`{"rank":1}`), binding)
	if err != nil {
		fmt.Println("encode:", err)

		return
	}

	// A caller trying to steer the query's ORDER BY by editing the token.
	edited := "x" + token[1:]

	_, err = codec.Decode(edited, binding)
	fmt.Println("refused:", errors.Is(err, signedcursor.ErrInvalidCursor))

	// Output:
	// refused: true
}
