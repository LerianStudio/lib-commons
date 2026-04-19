//go:build unit

package streaming

import (
	"errors"
	"testing"
	"time"
)

// TestLoadConfig_Defaults verifies that with all STREAMING_* env unset, the
// loader returns a disabled-by-default Config. When Enabled=false, no
// validation fires — callers get a NoopEmitter per C5.
func TestLoadConfig_Defaults(t *testing.T) {
	// DO NOT t.Parallel — t.Setenv guarantees parallel-safety per Go docs,
	// but os.Getenv reads are cross-test so we keep this sequential for clarity.
	clearStreamingEnv(t)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() returned unexpected error: %v", err)
	}

	if cfg.Enabled {
		t.Errorf("default Enabled = true; want false")
	}
	if len(cfg.Brokers) == 0 || cfg.Brokers[0] != "localhost:9092" {
		t.Errorf("default Brokers = %v; want [localhost:9092]", cfg.Brokers)
	}
	if cfg.Compression != "lz4" {
		t.Errorf("default Compression = %q; want lz4", cfg.Compression)
	}
	if cfg.RequiredAcks != "all" {
		t.Errorf("default RequiredAcks = %q; want all", cfg.RequiredAcks)
	}
	if cfg.BatchLingerMs != 5 {
		t.Errorf("default BatchLingerMs = %d; want 5", cfg.BatchLingerMs)
	}
	if cfg.BatchMaxBytes != 1_048_576 {
		t.Errorf("default BatchMaxBytes = %d; want 1048576", cfg.BatchMaxBytes)
	}
	if cfg.MaxBufferedRecords != 10_000 {
		t.Errorf("default MaxBufferedRecords = %d; want 10000", cfg.MaxBufferedRecords)
	}
	if cfg.RecordRetries != 10 {
		t.Errorf("default RecordRetries = %d; want 10", cfg.RecordRetries)
	}
	if cfg.RecordDeliveryTimeout != 30*time.Second {
		t.Errorf("default RecordDeliveryTimeout = %v; want 30s", cfg.RecordDeliveryTimeout)
	}
	if cfg.CloseTimeout != 30*time.Second {
		t.Errorf("default CloseTimeout = %v; want 30s", cfg.CloseTimeout)
	}
	if cfg.CBFailureRatio != 0.5 {
		t.Errorf("default CBFailureRatio = %v; want 0.5", cfg.CBFailureRatio)
	}
	if cfg.CBMinRequests != 10 {
		t.Errorf("default CBMinRequests = %d; want 10", cfg.CBMinRequests)
	}
	if cfg.CBTimeout != 30*time.Second {
		t.Errorf("default CBTimeout = %v; want 30s", cfg.CBTimeout)
	}
}

// TestLoadConfig_EnabledWithoutBrokers surfaces ErrMissingBrokers when
// ENABLED=true but the BROKERS value reduces to an empty slice after CSV
// splitting and whitespace trimming. This is the real operator mistake —
// mistyped broker list that collapses to nothing.
func TestLoadConfig_EnabledWithoutBrokers(t *testing.T) {
	clearStreamingEnv(t)
	t.Setenv("STREAMING_ENABLED", "true")
	// A comma with only whitespace collapses to an empty slice in splitCSV.
	t.Setenv("STREAMING_BROKERS", " , , ")
	t.Setenv("STREAMING_CLOUDEVENTS_SOURCE", "//lerian.midaz/tx-service")

	_, err := LoadConfig()
	if !errors.Is(err, ErrMissingBrokers) {
		t.Fatalf("LoadConfig() err = %v; want ErrMissingBrokers", err)
	}
}

// TestLoadConfig_EnabledWithoutSource surfaces ErrMissingSource when
// ENABLED=true but CLOUDEVENTS_SOURCE is empty.
func TestLoadConfig_EnabledWithoutSource(t *testing.T) {
	clearStreamingEnv(t)
	t.Setenv("STREAMING_ENABLED", "true")
	t.Setenv("STREAMING_BROKERS", "broker:9092")
	t.Setenv("STREAMING_CLOUDEVENTS_SOURCE", "")

	_, err := LoadConfig()
	if !errors.Is(err, ErrMissingSource) {
		t.Fatalf("LoadConfig() err = %v; want ErrMissingSource", err)
	}
}

// TestLoadConfig_InvalidCompression surfaces ErrInvalidCompression for
// values outside the allowed codec set.
func TestLoadConfig_InvalidCompression(t *testing.T) {
	clearStreamingEnv(t)
	t.Setenv("STREAMING_ENABLED", "true")
	t.Setenv("STREAMING_BROKERS", "broker:9092")
	t.Setenv("STREAMING_CLOUDEVENTS_SOURCE", "//lerian.midaz/tx-service")
	t.Setenv("STREAMING_COMPRESSION", "brotli")

	_, err := LoadConfig()
	if !errors.Is(err, ErrInvalidCompression) {
		t.Fatalf("LoadConfig() err = %v; want ErrInvalidCompression", err)
	}
}

// TestLoadConfig_InvalidAcks surfaces ErrInvalidAcks for non-enum values.
func TestLoadConfig_InvalidAcks(t *testing.T) {
	clearStreamingEnv(t)
	t.Setenv("STREAMING_ENABLED", "true")
	t.Setenv("STREAMING_BROKERS", "broker:9092")
	t.Setenv("STREAMING_CLOUDEVENTS_SOURCE", "//lerian.midaz/tx-service")
	t.Setenv("STREAMING_REQUIRED_ACKS", "maybe")

	_, err := LoadConfig()
	if !errors.Is(err, ErrInvalidAcks) {
		t.Fatalf("LoadConfig() err = %v; want ErrInvalidAcks", err)
	}
}

// TestLoadConfig_DisabledSkipsValidation proves that ENABLED=false short-circuits
// validation — you can have empty BROKERS and no SOURCE and still get a valid
// (disabled) config back, which yields a NoopEmitter at construction.
func TestLoadConfig_DisabledSkipsValidation(t *testing.T) {
	clearStreamingEnv(t)
	t.Setenv("STREAMING_ENABLED", "false")
	t.Setenv("STREAMING_BROKERS", "")
	t.Setenv("STREAMING_CLOUDEVENTS_SOURCE", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() with disabled returned unexpected error: %v", err)
	}
	if cfg.Enabled {
		t.Errorf("cfg.Enabled = true; want false")
	}
}

// TestLoadConfig_ValidCSVBrokers splits STREAMING_BROKERS on commas and trims
// whitespace.
func TestLoadConfig_ValidCSVBrokers(t *testing.T) {
	clearStreamingEnv(t)
	t.Setenv("STREAMING_ENABLED", "true")
	t.Setenv("STREAMING_BROKERS", "broker1:9092, broker2:9092 ,broker3:9092")
	t.Setenv("STREAMING_CLOUDEVENTS_SOURCE", "//lerian.midaz/tx-service")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() err = %v", err)
	}
	want := []string{"broker1:9092", "broker2:9092", "broker3:9092"}
	if len(cfg.Brokers) != len(want) {
		t.Fatalf("len(Brokers) = %d; want %d", len(cfg.Brokers), len(want))
	}
	for i, w := range want {
		if cfg.Brokers[i] != w {
			t.Errorf("Brokers[%d] = %q; want %q", i, cfg.Brokers[i], w)
		}
	}
}

// TestLoadConfig_AllValidCompressionCodecs asserts each supported codec.
func TestLoadConfig_AllValidCompressionCodecs(t *testing.T) {
	for _, codec := range []string{"snappy", "lz4", "zstd", "gzip", "none"} {
		t.Run(codec, func(t *testing.T) {
			clearStreamingEnv(t)
			t.Setenv("STREAMING_ENABLED", "true")
			t.Setenv("STREAMING_BROKERS", "broker:9092")
			t.Setenv("STREAMING_CLOUDEVENTS_SOURCE", "//lerian.midaz/tx-service")
			t.Setenv("STREAMING_COMPRESSION", codec)

			cfg, err := LoadConfig()
			if err != nil {
				t.Fatalf("LoadConfig(%s) err = %v", codec, err)
			}
			if cfg.Compression != codec {
				t.Errorf("cfg.Compression = %q; want %q", cfg.Compression, codec)
			}
		})
	}
}

// TestLoadConfig_AllValidAcks asserts each supported acks value.
func TestLoadConfig_AllValidAcks(t *testing.T) {
	for _, acks := range []string{"all", "leader", "none"} {
		t.Run(acks, func(t *testing.T) {
			clearStreamingEnv(t)
			t.Setenv("STREAMING_ENABLED", "true")
			t.Setenv("STREAMING_BROKERS", "broker:9092")
			t.Setenv("STREAMING_CLOUDEVENTS_SOURCE", "//lerian.midaz/tx-service")
			t.Setenv("STREAMING_REQUIRED_ACKS", acks)

			cfg, err := LoadConfig()
			if err != nil {
				t.Fatalf("LoadConfig(%s) err = %v", acks, err)
			}
			if cfg.RequiredAcks != acks {
				t.Errorf("cfg.RequiredAcks = %q; want %q", cfg.RequiredAcks, acks)
			}
		})
	}
}

// TestLoadConfig_CBFailureRatioOverride exercises the float env-var parser
// and confirms the CB thresholds round-trip into Config.
func TestLoadConfig_CBFailureRatioOverride(t *testing.T) {
	clearStreamingEnv(t)
	t.Setenv("STREAMING_ENABLED", "true")
	t.Setenv("STREAMING_BROKERS", "broker:9092")
	t.Setenv("STREAMING_CLOUDEVENTS_SOURCE", "//lerian.midaz/tx-service")
	t.Setenv("STREAMING_CB_FAILURE_RATIO", "0.25")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() err = %v", err)
	}
	if cfg.CBFailureRatio != 0.25 {
		t.Errorf("CBFailureRatio = %v; want 0.25", cfg.CBFailureRatio)
	}
}

// TestLoadConfig_CBFailureRatioBadValueUsesDefault confirms the float parser
// falls back to the default when the env var is unparseable.
func TestLoadConfig_CBFailureRatioBadValueUsesDefault(t *testing.T) {
	clearStreamingEnv(t)
	t.Setenv("STREAMING_ENABLED", "true")
	t.Setenv("STREAMING_BROKERS", "broker:9092")
	t.Setenv("STREAMING_CLOUDEVENTS_SOURCE", "//lerian.midaz/tx-service")
	t.Setenv("STREAMING_CB_FAILURE_RATIO", "not-a-float")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() err = %v", err)
	}
	if cfg.CBFailureRatio != 0.5 {
		t.Errorf("CBFailureRatio = %v; want 0.5 (default)", cfg.CBFailureRatio)
	}
}

// TestLoadConfig_EventTogglesBogusEntriesSkipped exercises the forgiving
// parser — a malformed entry doesn't poison the whole toggle map.
func TestLoadConfig_EventTogglesBogusEntriesSkipped(t *testing.T) {
	clearStreamingEnv(t)
	t.Setenv("STREAMING_ENABLED", "true")
	t.Setenv("STREAMING_BROKERS", "broker:9092")
	t.Setenv("STREAMING_CLOUDEVENTS_SOURCE", "//lerian.midaz/tx-service")
	// "no-equals" lacks =, "=value" lacks key, "key=maybe" has unknown value.
	t.Setenv("STREAMING_EVENT_TOGGLES", "no-equals,=value,key=maybe,real.event=true")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() err = %v", err)
	}
	if got := cfg.EventToggles["real.event"]; !got {
		t.Errorf("real.event toggle = %v; want true", got)
	}
	// Malformed entries should not create spurious keys.
	if _, exists := cfg.EventToggles["no-equals"]; exists {
		t.Errorf("no-equals unexpectedly parsed into EventToggles")
	}
}

// TestLoadConfig_EventToggles parses the CSV "resource.event=bool" format.
func TestLoadConfig_EventToggles(t *testing.T) {
	clearStreamingEnv(t)
	t.Setenv("STREAMING_ENABLED", "true")
	t.Setenv("STREAMING_BROKERS", "broker:9092")
	t.Setenv("STREAMING_CLOUDEVENTS_SOURCE", "//lerian.midaz/tx-service")
	t.Setenv("STREAMING_EVENT_TOGGLES", "transaction.created=false,account.updated=true")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() err = %v", err)
	}
	if cfg.EventToggles["transaction.created"] != false {
		t.Errorf("EventToggles[transaction.created] = %v; want false", cfg.EventToggles["transaction.created"])
	}
	if cfg.EventToggles["account.updated"] != true {
		t.Errorf("EventToggles[account.updated] = %v; want true", cfg.EventToggles["account.updated"])
	}
}

// clearStreamingEnv wipes every STREAMING_* env var for the duration of the
// test. t.Setenv with empty string is sufficient — the loader treats empty
// strings as "not set" via GetenvOrDefault.
func clearStreamingEnv(t *testing.T) {
	t.Helper()

	vars := []string{
		"STREAMING_ENABLED",
		"STREAMING_BROKERS",
		"STREAMING_CLIENT_ID",
		"STREAMING_BATCH_LINGER_MS",
		"STREAMING_BATCH_MAX_BYTES",
		"STREAMING_MAX_BUFFERED_RECORDS",
		"STREAMING_COMPRESSION",
		"STREAMING_RECORD_RETRIES",
		"STREAMING_RECORD_DELIVERY_TIMEOUT_S",
		"STREAMING_REQUIRED_ACKS",
		"STREAMING_CB_FAILURE_RATIO",
		"STREAMING_CB_MIN_REQUESTS",
		"STREAMING_CB_TIMEOUT_S",
		"STREAMING_CLOSE_TIMEOUT_S",
		"STREAMING_CLOUDEVENTS_SOURCE",
		"STREAMING_EVENT_TOGGLES",
	}
	for _, v := range vars {
		t.Setenv(v, "")
	}
}
