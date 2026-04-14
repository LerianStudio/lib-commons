//go:build unit

package http_test

import (
	"fmt"

	cn "github.com/LerianStudio/lib-commons/v5/commons/constants"
	uhttp "github.com/LerianStudio/lib-commons/v5/commons/net/http"
)

func ExampleEncodeCursor() {
	encoded, err := uhttp.EncodeCursor(uhttp.Cursor{ID: "acc_01", Direction: uhttp.CursorDirectionNext})
	if err != nil {
		fmt.Println("encode error")
		return
	}

	decoded, err := uhttp.DecodeCursor(encoded)
	if err != nil {
		fmt.Println("decode error")
		return
	}

	op, order, err := uhttp.CursorDirectionRules(cn.SortDirASC, decoded.Direction)

	fmt.Println(err == nil)
	fmt.Println(decoded.ID, decoded.Direction)
	fmt.Println(op, order)

	// Output:
	// true
	// acc_01 next
	// > ASC
}
