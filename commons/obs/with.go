package obs

import (
	"context"
	"fmt"

	"github.com/LerianStudio/lib-commons/v7/commons/internal/nilcheck"
)

// With returns a Logger that attaches kv to every event it emits.
//
// It is a free function and not a method on Logger on purpose: a method that
// returns the interface it is declared on cannot be satisfied by any
// implementation outside the declaring package, which would reintroduce
// exactly the nominal coupling this package removes.
//
// A nil logger yields Nop(), including a Logger holding a typed nil pointer:
// an adapter whose methods dereference their receiver would panic on the first
// delegated call, and "l == nil" alone does not reject it. Empty kv yields the
// logger unchanged.
func With(l Logger, kv ...any) Logger {
	if nilcheck.Interface(l) {
		l = Nop()
	}

	if len(kv) == 0 {
		return l
	}

	return &boundLogger{base: l, kv: NormalizeKV(kv...)}
}

// WithGroup returns a Logger that namespaces the keys of every event it
// emits under name, joined with a dot. Attributes bound before the group is
// opened are not namespaced; attributes bound after it are.
//
// A nil logger yields Nop(), typed nils included, on the same grounds as With.
// An empty name yields the logger unchanged.
func WithGroup(l Logger, name string) Logger {
	if nilcheck.Interface(l) {
		l = Nop()
	}

	if name == "" {
		return l
	}

	return &boundLogger{base: l, prefix: name + "."}
}

// boundLogger carries attributes and/or a key prefix on behalf of With and
// WithGroup. It always delegates to base, so wrapping a decorating logger
// (for example a tenant-aware logger) preserves that decoration.
type boundLogger struct {
	base   Logger
	kv     []any
	prefix string
}

func (l *boundLogger) Log(ctx context.Context, level int, msg string, kv ...any) {
	if l == nil || nilcheck.Interface(l.base) {
		return
	}

	if ctx == nil {
		ctx = context.Background()
	}

	l.base.Log(ctx, level, msg, l.merge(kv)...)
}

func (l *boundLogger) Enabled(level int) bool {
	if l == nil || nilcheck.Interface(l.base) {
		return false
	}

	return l.base.Enabled(level)
}

func (l *boundLogger) Sync(ctx context.Context) error {
	if l == nil || nilcheck.Interface(l.base) {
		return nil
	}

	if ctx == nil {
		ctx = context.Background()
	}

	return l.base.Sync(ctx)
}

// merge concatenates the bound attributes with the call-site attributes,
// applying the group prefix to the latter only.
func (l *boundLogger) merge(kv []any) []any {
	call := NormalizeKV(kv...)

	if l.prefix != "" {
		for i := 0; i < len(call); i += 2 {
			if key, ok := call[i].(string); ok {
				call[i] = l.prefix + key
			}
		}
	}

	if len(l.kv) == 0 {
		return call
	}

	merged := make([]any, 0, len(l.kv)+len(call))
	merged = append(merged, l.kv...)
	merged = append(merged, call...)

	return merged
}

// NormalizeKV canonicalises a variadic key/value list into an even-length
// slice whose even indices are always non-empty strings.
//
// A key that is not a non-empty string is replaced by the positional
// placeholder "arg_N", where N is its index. A trailing key with no value is
// paired with nil. Adapters should use this so that every implementation of
// Logger agrees on how malformed input is rendered.
func NormalizeKV(kv ...any) []any {
	if len(kv) == 0 {
		return nil
	}

	out := make([]any, 0, len(kv)+len(kv)%2)

	for i := 0; i < len(kv); i += 2 {
		key := fmt.Sprintf("arg_%d", i)
		if ks, ok := kv[i].(string); ok && ks != "" {
			key = ks
		}

		var value any
		if i+1 < len(kv) {
			value = kv[i+1]
		}

		out = append(out, key, value)
	}

	return out
}
