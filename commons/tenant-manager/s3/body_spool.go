// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

// inMemoryBodyLimit caps how much of an unseekable body a write keeps in
// memory; anything larger is spooled to a temporary file.
const inMemoryBodyLimit = 1 << 20

// maxPutObjectSize is the largest object S3 accepts in a single PutObject.
const maxPutObjectSize int64 = 5 << 30

// maxBodySize caps an unseekable body; S3 would reject anything larger, so
// spooling past it only fills the disk. Tests shrink it.
var maxBodySize = maxPutObjectSize

var errBodyTooLarge = errors.New("body exceeds the S3 single PutObject maximum")

// seekableBody returns body as an io.ReadSeeker: S3 refuses a PutObject without
// Content-Length (HTTP 411), and the SDK learns the length only by seeking.
// A seekable body passes through untouched. An unseekable one is buffered in
// memory up to inMemoryBodyLimit and spooled to a temporary file beyond it, so
// a large stream never sits whole in memory; it is refused past maxBodySize.
// Cancelling ctx stops the read, closing body when it is an io.Closer so a
// blocked read returns. The caller must always invoke release once PutObject
// returns; it closes and removes any spool file.
func seekableBody(ctx context.Context, body io.Reader) (io.Reader, func(), error) {
	if _, ok := body.(io.ReadSeeker); ok {
		return body, func() {}, nil
	}

	if closer, ok := body.(io.Closer); ok {
		stop := context.AfterFunc(ctx, func() { _ = closer.Close() })
		defer stop()
	}

	source := io.LimitReader(contextReader{ctx: ctx, reader: body}, maxBodySize+1)

	head, err := io.ReadAll(io.LimitReader(source, inMemoryBodyLimit+1))
	if err != nil {
		return nil, nil, readFailure(ctx, "read body", err)
	}

	if int64(len(head)) > maxBodySize {
		return nil, nil, bodyTooLarge()
	}

	if len(head) <= inMemoryBodyLimit {
		return bytes.NewReader(head), func() {}, nil
	}

	return spoolToTempFile(ctx, io.MultiReader(bytes.NewReader(head), source))
}

func spoolToTempFile(ctx context.Context, body io.Reader) (io.Reader, func(), error) {
	file, err := os.CreateTemp("", "lib-commons-s3-body-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create body spool: %w", err)
	}

	release := func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}

	written, err := io.Copy(file, body)
	if err != nil {
		release()

		return nil, nil, readFailure(ctx, "spool body", err)
	}

	if written > maxBodySize {
		release()

		return nil, nil, bodyTooLarge()
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		release()

		return nil, nil, fmt.Errorf("rewind body spool: %w", err)
	}

	return file, release, nil
}

// readFailure reports a cancelled context as the cause: closing a blocked body
// surfaces as a read error that would otherwise hide the cancellation.
func readFailure(ctx context.Context, step string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%s: %w", step, ctxErr)
	}

	return fmt.Errorf("%s: %w", step, err)
}

func bodyTooLarge() error {
	return fmt.Errorf("%w of %d bytes", errBodyTooLarge, maxBodySize)
}

// contextReader stops a copy between chunks once ctx is done.
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}

	return r.reader.Read(p)
}
