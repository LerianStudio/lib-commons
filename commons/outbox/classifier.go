package outbox

// RetryClassifier determines whether an error should not be retried.
type RetryClassifier interface {
	IsNonRetryable(err error) bool
}

type RetryClassifierFunc func(err error) bool

func (fn RetryClassifierFunc) IsNonRetryable(err error) bool {
	if fn == nil {
		return false
	}

	return fn(err)
}
