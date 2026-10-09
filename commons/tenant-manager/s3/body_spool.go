// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package s3

import (
	"bytes"
	"fmt"
	"io"
	"os"
)

// inMemoryBodyLimit caps how much of an unseekable body a write keeps in
// memory; anything larger is spooled to a temporary file.
const inMemoryBodyLimit = 1 << 20

// seekableBody returns body as an io.ReadSeeker: S3 refuses a PutObject without
// Content-Length (HTTP 411), and the SDK learns the length only by seeking.
// A seekable body passes through untouched. An unseekable one is buffered in
// memory up to inMemoryBodyLimit and spooled to a temporary file beyond it, so
// a large stream never sits whole in memory. The caller must always invoke
// release once PutObject returns; it closes and removes any spool file.
func seekableBody(body io.Reader) (io.Reader, func(), error) {
	if _, ok := body.(io.ReadSeeker); ok {
		return body, func() {}, nil
	}

	head, err := io.ReadAll(io.LimitReader(body, inMemoryBodyLimit+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read body: %w", err)
	}

	if len(head) <= inMemoryBodyLimit {
		return bytes.NewReader(head), func() {}, nil
	}

	return spoolToTempFile(io.MultiReader(bytes.NewReader(head), body))
}

func spoolToTempFile(body io.Reader) (io.Reader, func(), error) {
	file, err := os.CreateTemp("", "lib-commons-s3-body-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create body spool: %w", err)
	}

	release := func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}

	if _, err := io.Copy(file, body); err != nil {
		release()

		return nil, nil, fmt.Errorf("spool body: %w", err)
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		release()

		return nil, nil, fmt.Errorf("rewind body spool: %w", err)
	}

	return file, release, nil
}
