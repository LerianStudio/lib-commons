//go:build unit

package s3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
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
			body, release, err := seekableBody(io.MultiReader(bytes.NewReader(payload)))
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
