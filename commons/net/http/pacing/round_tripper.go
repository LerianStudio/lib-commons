package pacing

import (
	"errors"
	"fmt"
	"net/http"
)

// BucketsFunc derives the pacing buckets that apply to one outbound request. It
// runs before the request leaves the process, so it may read the request context
// for a tenant and the request itself for a target institution.
//
// Returning an error, or an empty slice, refuses the request: an outbound call
// that nothing paces must not reach the rail.
type BucketsFunc func(*http.Request) ([]Bucket, error)

// RoundTripper paces every outbound request at the transport boundary and then
// delegates to next. Because it sits in the transport, a redirect and a client
// retry are each paced on their own, which a wait placed above request
// construction cannot do.
type RoundTripper struct {
	next    http.RoundTripper
	pacer   *Pacer
	buckets BucketsFunc
}

// NewRoundTripper builds a pacing RoundTripper. A nil next transport falls back
// to http.DefaultTransport. A nil pacer or a nil BucketsFunc is refused: both
// would leave the outbound path unpaced.
func NewRoundTripper(next http.RoundTripper, pacer *Pacer, buckets BucketsFunc) (*RoundTripper, error) {
	if pacer == nil || pacer.conn == nil {
		return nil, ErrPacerUnavailable
	}

	if buckets == nil {
		return nil, ErrNoBuckets
	}

	if next == nil {
		next = http.DefaultTransport
	}

	return &RoundTripper{next: next, pacer: pacer, buckets: buckets}, nil
}

// RoundTrip implements http.RoundTripper. It never returns both a response and
// an error, and it closes the request body on every path that does not reach the
// next transport, as the http.RoundTripper contract requires.
func (rt *RoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt == nil || rt.pacer == nil || rt.buckets == nil || rt.next == nil {
		return nil, refuse(req, ErrPacerUnavailable)
	}

	buckets, err := rt.buckets(req)
	if err != nil {
		return nil, refuse(req, fmt.Errorf("pacing: cannot derive buckets for the request: %w", err))
	}

	if err := rt.pacer.Acquire(req.Context(), buckets...); err != nil {
		return nil, refuse(req, err)
	}

	return rt.next.RoundTrip(req)
}

// refuse closes the request body and returns the refusal. A close failure is
// joined onto the refusal rather than discarded, so the refusal still matches
// errors.Is while nothing is silently dropped.
func refuse(req *http.Request, err error) error {
	if req == nil || req.Body == nil {
		return err
	}

	if closeErr := req.Body.Close(); closeErr != nil {
		return errors.Join(err, closeErr)
	}

	return err
}
