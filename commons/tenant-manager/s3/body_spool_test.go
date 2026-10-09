//go:build unit

package s3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// largeStreamSize is far beyond any in-memory buffer a write may keep for an
// unseekable body; such a body must reach PutObject spooled to disk.
const largeStreamSize = 8 << 20

func largePayload() []byte {
	payload := make([]byte, largeStreamSize)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	return payload
}

// requireSpooledAndRemoved asserts that PutObject received a temporary file
// rather than an in-memory copy, and that the file is gone after the write.
func requireSpooledAndRemoved(t *testing.T, body io.Reader) {
	t.Helper()

	file, ok := body.(*os.File)
	require.True(t, ok, "an unseekable body beyond the in-memory cap must reach PutObject as a temp file, got %T", body)

	_, err := os.Stat(file.Name())
	assert.True(t, errors.Is(err, os.ErrNotExist), "the spool file %q must be removed after the write, stat err: %v", file.Name(), err)
}

func storageWrites() map[string]func(Storage, context.Context, string, io.Reader, string) error {
	return map[string]func(Storage, context.Context, string, io.Reader, string) error{
		"Upload": Storage.Upload,
		"Create": Storage.Create,
	}
}

func TestStorage_Writes_SpoolALargeUnseekableBodyToDisk(t *testing.T) {
	t.Parallel()

	for name, write := range storageWrites() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fake := newFakeObjectAPI()
			storage, err := NewStorage(fake, testBucket)
			require.NoError(t, err)

			payload := largePayload()
			stream := io.MultiReader(bytes.NewReader(payload))
			require.NoError(t, write(storage, context.Background(), "k", stream, "application/octet-stream"))

			requireSpooledAndRemoved(t, fake.lastPutBody)
			assert.True(t, bytes.Equal(payload, fake.objects["k"]), "spooled content must round-trip byte-identical")
		})
	}
}

func TestStorage_Writes_RemoveTheSpoolWhenPutObjectFails(t *testing.T) {
	t.Parallel()

	for name, write := range storageWrites() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fake := newFakeObjectAPI()
			fake.putErr = errors.New("s3 down")
			storage, err := NewStorage(fake, testBucket)
			require.NoError(t, err)

			stream := io.MultiReader(bytes.NewReader(largePayload()))
			require.ErrorIs(t, write(storage, context.Background(), "k", stream, "application/octet-stream"), fake.putErr)

			requireSpooledAndRemoved(t, fake.lastPutBody)
		})
	}
}

func TestStorage_Writes_PassASeekableBodyThroughUntouched(t *testing.T) {
	t.Parallel()

	for name, write := range storageWrites() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fake := newFakeObjectAPI()
			storage, err := NewStorage(fake, testBucket)
			require.NoError(t, err)

			body := bytes.NewReader(largePayload())
			require.NoError(t, write(storage, context.Background(), "k", body, "application/octet-stream"))

			assert.Same(t, body, fake.lastPutBody, "a seekable body must reach PutObject as given")
		})
	}
}

func TestStorage_Writes_ReadErrorWhileSpoolingIsReturned(t *testing.T) {
	t.Parallel()

	for name, write := range storageWrites() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fake := newFakeObjectAPI()
			storage, err := NewStorage(fake, testBucket)
			require.NoError(t, err)

			readErr := errors.New("stream broke")
			stream := io.MultiReader(bytes.NewReader(largePayload()), iotestErrReader{err: readErr})
			require.ErrorIs(t, write(storage, context.Background(), "k", stream, "application/octet-stream"), readErr)

			assert.Nil(t, fake.lastPutBody, "PutObject must not run when the body cannot be read")
		})
	}
}

type iotestErrReader struct{ err error }

func (r iotestErrReader) Read([]byte) (int, error) { return 0, r.err }

func TestRetainedStorage_CreateRetained_SpoolsALargeUnseekableBodyToDisk(t *testing.T) {
	t.Parallel()

	for name, putErr := range map[string]error{"success": nil, "put fails": errors.New("s3 down")} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeRetainedObjectAPI{
				putOutput:  &awss3.PutObjectOutput{VersionId: aws.String("version-1")},
				headOutput: &awss3.HeadObjectOutput{VersionId: aws.String("version-1")},
				putErr:     putErr,
			}
			store, err := NewRetainedStorage(fake, "retained-bucket")
			require.NoError(t, err)

			_, _ = store.CreateRetained(
				context.Background(),
				"artifact",
				io.MultiReader(bytes.NewReader(largePayload())),
				"application/octet-stream",
				Retention{Mode: RetentionModeCompliance, RetainUntil: time.Now().Add(time.Hour)},
			)

			require.NotNil(t, fake.putInput)
			requireSpooledAndRemoved(t, fake.putInput.Body)
		})
	}
}

func TestSeekableBody_KeepsInMemoryUpToTheCapAndSpoolsBeyondIt(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		size    int
		spooled bool
	}{
		"empty":        {size: 0},
		"at the cap":   {size: inMemoryBodyLimit},
		"one past cap": {size: inMemoryBodyLimit + 1, spooled: true},
		"far past cap": {size: largeStreamSize, spooled: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			payload := bytes.Repeat([]byte{0xA5}, tc.size)
			body, release, err := seekableBody(context.Background(), io.MultiReader(bytes.NewReader(payload)))
			require.NoError(t, err)

			file, isFile := body.(*os.File)
			assert.Equal(t, tc.spooled, isFile, "got %T", body)

			got, err := io.ReadAll(body)
			require.NoError(t, err)
			assert.True(t, bytes.Equal(payload, got), "content must round-trip byte-identical")

			release()

			if isFile {
				_, err := os.Stat(file.Name())
				assert.True(t, errors.Is(err, os.ErrNotExist), "release must remove the spool file")
			}
		})
	}
}

// isolateSpoolDir points temporary files at a fresh directory so a test can
// prove no spool file outlives a failed write. t.Setenv forbids t.Parallel.
func isolateSpoolDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)

	return dir
}

func requireNoSpoolLeft(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "a failed write must remove its spool file")
}

// overrideMaxBodySize shrinks the single-PUT cap for one test; tests using it
// must not run in parallel.
func overrideMaxBodySize(t *testing.T, limit int64) {
	t.Helper()

	previous := maxBodySize
	maxBodySize = limit

	t.Cleanup(func() { maxBodySize = previous })
}

//nolint:paralleltest // isolates TMPDIR with t.Setenv.
func TestStorage_Writes_CancellationUnblocksABlockedBodyRead(t *testing.T) {
	cases := map[string]int{
		"blocked while buffering in memory": 16,
		"blocked while spooling to disk":    inMemoryBodyLimit + 4096,
	}
	for phase, delivered := range cases {
		for name, write := range storageWrites() {
			t.Run(phase+"/"+name, func(t *testing.T) {
				dir := isolateSpoolDir(t)

				fake := newFakeObjectAPI()
				storage, err := NewStorage(fake, testBucket)
				require.NoError(t, err)

				pipeReader, writer := io.Pipe()
				t.Cleanup(func() { _ = writer.Close() })

				reader := &blockingPipe{PipeReader: pipeReader, delivered: delivered, blocked: make(chan struct{})}

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				go func() { _, _ = writer.Write(bytes.Repeat([]byte{0x5A}, delivered)) }()

				go func() {
					// Cancel only once the write sits in a pipe read no one will
					// ever satisfy, so nothing but closing the body can end it.
					<-reader.blocked
					cancel()
				}()

				done := make(chan error, 1)
				go func() { done <- write(storage, ctx, "k", reader, "application/octet-stream") }()

				select {
				case err := <-done:
					require.ErrorIs(t, err, context.Canceled)
				case <-time.After(5 * time.Second):
					t.Fatal("a cancelled write must not stay blocked reading its body")
				}

				assert.Nil(t, fake.lastPutBody, "PutObject must not run when the context is cancelled")
				requireNoSpoolLeft(t, dir)
			})
		}
	}
}

// blockingPipe signals when a Read starts after every delivered byte was
// returned: that Read blocks until the pipe is closed.
type blockingPipe struct {
	*io.PipeReader
	delivered, returned int
	blocked             chan struct{}
	once                sync.Once
}

func (p *blockingPipe) Read(b []byte) (int, error) {
	if p.returned >= p.delivered {
		p.once.Do(func() { close(p.blocked) })
	}

	n, err := p.PipeReader.Read(b)
	p.returned += n

	return n, err
}

// cancellingReader is an unseekable, non-closable stream that cancels its
// context after a set number of reads, so only a check between chunks can stop
// the copy. It ends after cancelAfter+64 reads so a missing check fails the
// test instead of spooling forever.
type cancellingReader struct {
	reads, cancelAfter int
	cancel             context.CancelFunc
}

func (r *cancellingReader) Read(p []byte) (int, error) {
	r.reads++
	if r.reads == r.cancelAfter {
		r.cancel()
	}

	if r.reads > r.cancelAfter+64 {
		return 0, io.EOF
	}

	for i := range p {
		p[i] = 0x3C
	}

	return len(p), nil
}

//nolint:paralleltest // isolates TMPDIR with t.Setenv.
func TestSeekableBody_StopsBetweenChunksWhenTheReaderCannotBeClosed(t *testing.T) {
	cases := map[string]int{
		"cancelled while buffering in memory": 1,
		"cancelled while spooling to disk":    inMemoryBodyLimit/512 + 64,
	}
	for name, cancelAfter := range cases {
		t.Run(name, func(t *testing.T) {
			dir := isolateSpoolDir(t)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			body, release, err := seekableBody(ctx, &cancellingReader{cancelAfter: cancelAfter, cancel: cancel})
			require.ErrorIs(t, err, context.Canceled)
			assert.Nil(t, body)
			assert.Nil(t, release)
			requireNoSpoolLeft(t, dir)
		})
	}
}

//nolint:paralleltest // overrides the package-level body cap and TMPDIR.
func TestStorage_Writes_RefuseABodyBeyondTheSinglePutMaximum(t *testing.T) {
	cases := map[string]int64{
		"cap within the in-memory buffer": 64,
		"cap beyond the in-memory buffer": inMemoryBodyLimit + 4096,
	}
	for phase, limit := range cases {
		for name, write := range storageWrites() {
			t.Run(phase+"/"+name, func(t *testing.T) {
				dir := isolateSpoolDir(t)
				overrideMaxBodySize(t, limit)

				fake := newFakeObjectAPI()
				storage, err := NewStorage(fake, testBucket)
				require.NoError(t, err)

				atCap := bytes.Repeat([]byte{0x11}, int(limit))
				require.NoError(t, write(storage, context.Background(), "at-cap", io.MultiReader(bytes.NewReader(atCap)), "application/octet-stream"),
					"a body of exactly the cap must still be written")
				assert.True(t, bytes.Equal(atCap, fake.objects["at-cap"]))

				fake.lastPutBody = nil

				overCap := io.MultiReader(bytes.NewReader(atCap), bytes.NewReader([]byte{0x22}))
				err = write(storage, context.Background(), "over-cap", overCap, "application/octet-stream")
				require.ErrorIs(t, err, errBodyTooLarge)
				assert.Contains(t, err.Error(), strconv.FormatInt(limit, 10), "the error must name the limit")
				assert.Nil(t, fake.lastPutBody, "PutObject must not run for a body beyond the cap")
				assert.NotContains(t, fake.objects, "over-cap")
				requireNoSpoolLeft(t, dir)
			})
		}
	}
}

func TestMaxBodySize_IsTheS3SinglePutMaximum(t *testing.T) {
	t.Parallel()

	assert.Equal(t, int64(5*1024*1024*1024), maxPutObjectSize)
}

// closeRecorder is an unseekable, closable body that reports when it is closed.
type closeRecorder struct {
	io.Reader
	closed chan struct{}
}

func (r closeRecorder) Close() error {
	close(r.closed)

	return nil
}

func TestSeekableBody_LeavesTheBodyOpenOnceItWasRead(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	body := closeRecorder{Reader: bytes.NewReader([]byte("payload")), closed: make(chan struct{})}

	prepared, release, err := seekableBody(ctx, body)
	require.NoError(t, err)
	defer release()

	cancel()

	select {
	case <-body.closed:
		t.Fatal("a cancellation after the body was read must not close the caller's body")
	case <-time.After(200 * time.Millisecond):
	}

	got, err := io.ReadAll(prepared)
	require.NoError(t, err)
	assert.Equal(t, "payload", string(got))
}
