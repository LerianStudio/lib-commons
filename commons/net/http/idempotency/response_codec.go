package idempotency

import "context"

// ResponseCodec transforms the serialized replay response before storage and
// reverses that transformation during replay. Applications can provide an
// authenticated-encryption implementation so sensitive response bodies are
// never stored as plaintext. Implementations must be deterministic only in the
// sense that Decode reverses Encode; randomized encryption is supported.
type ResponseCodec interface {
	Encode(ctx context.Context, plaintext []byte) ([]byte, error)
	Decode(ctx context.Context, encoded []byte) ([]byte, error)
}

type identityResponseCodec struct{}

func (identityResponseCodec) Encode(_ context.Context, plaintext []byte) ([]byte, error) {
	return append([]byte(nil), plaintext...), nil
}

func (identityResponseCodec) Decode(_ context.Context, encoded []byte) ([]byte, error) {
	return append([]byte(nil), encoded...), nil
}
