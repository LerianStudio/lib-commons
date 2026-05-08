//go:build unit

package metrics_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
)

// TestMetricType_String covers every MetricType variant including the
// `unknown` fallback (cast int=99) so refactors of the switch can't silently
// drop a case.
func TestMetricType_String(t *testing.T) {
	cases := []struct {
		mt   metrics.MetricType
		want string
	}{
		{metrics.Counter, "counter"},
		{metrics.Histogram, "histogram"},
		{metrics.Gauge, "gauge"},
		{metrics.MetricType(99), "unknown"},
	}

	for _, tc := range cases {
		if got := tc.mt.String(); got != tc.want {
			t.Errorf("MetricType(%d).String() = %q, want %q", tc.mt, got, tc.want)
		}
	}
}

// TestHelpers_NoDuplicateGoFunctionName asserts the Helpers slice declares
// each Go method name exactly once. The analyzer's helperByMethod init-time
// panic is the production guard; this test catches the same regression at
// `make ci` time.
func TestHelpers_NoDuplicateGoFunctionName(t *testing.T) {
	seen := make(map[string]struct{}, len(metrics.Helpers))

	for _, h := range metrics.Helpers {
		if _, dup := seen[h.GoFunctionName]; dup {
			t.Errorf("metrics.Helpers contains duplicate GoFunctionName %q", h.GoFunctionName)
		}

		seen[h.GoFunctionName] = struct{}{}
	}

	if len(seen) != len(metrics.Helpers) {
		t.Errorf("unique GoFunctionName count = %d, want %d", len(seen), len(metrics.Helpers))
	}
}

// TestHelpers_NoDuplicateMetricName mirrors TestHelpers_NoDuplicateGoFunctionName
// for the emitted metric name. Two helpers emitting the same metric name with
// different unit/description collapses dashboards silently.
func TestHelpers_NoDuplicateMetricName(t *testing.T) {
	seen := make(map[string]struct{}, len(metrics.Helpers))

	for _, h := range metrics.Helpers {
		if _, dup := seen[h.MetricName]; dup {
			t.Errorf("metrics.Helpers contains duplicate MetricName %q", h.MetricName)
		}

		seen[h.MetricName] = struct{}{}
	}

	if len(seen) != len(metrics.Helpers) {
		t.Errorf("unique MetricName count = %d, want %d", len(seen), len(metrics.Helpers))
	}
}

// TestHelpers_NoEmptyMetricName rejects empty MetricName entries — an empty
// metric name would produce nameless instruments at runtime.
func TestHelpers_NoEmptyMetricName(t *testing.T) {
	for i, h := range metrics.Helpers {
		if h.MetricName == "" {
			t.Errorf("metrics.Helpers[%d] (GoFunctionName=%q) has empty MetricName", i, h.GoFunctionName)
		}

		if h.GoFunctionName == "" {
			t.Errorf("metrics.Helpers[%d] (MetricName=%q) has empty GoFunctionName", i, h.MetricName)
		}
	}
}

// TestHelpersMatchRegistry enforces bidirectional consistency between
// MetricsFactory.Record* methods and the metrics.Helpers registry.
func TestHelpersMatchRegistry(t *testing.T) {
	factoryType := reflect.TypeOf((*metrics.MetricsFactory)(nil))

	methodsInCode := map[string]bool{}
	for i := 0; i < factoryType.NumMethod(); i++ {
		name := factoryType.Method(i).Name
		if strings.HasPrefix(name, "Record") {
			methodsInCode[name] = true
		}
	}

	methodsInRegistry := map[string]bool{}
	for _, h := range metrics.Helpers {
		methodsInRegistry[h.GoFunctionName] = true
	}

	for name := range methodsInCode {
		if !methodsInRegistry[name] {
			t.Errorf("MetricsFactory.%s exists but is not in metrics.Helpers: add an entry to registry.go", name)
		}
	}

	for name := range methodsInRegistry {
		if !methodsInCode[name] {
			t.Errorf("metrics.Helpers references %s but no method exists on MetricsFactory: remove it from registry.go", name)
		}
	}
}
