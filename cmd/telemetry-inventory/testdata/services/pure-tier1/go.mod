module example.com/puretier1

go 1.25.0

require (
	go.opentelemetry.io/otel v1.42.0
	go.opentelemetry.io/otel/metric v1.42.0
)

require github.com/cespare/xxhash/v2 v2.3.0 // indirect

replace github.com/LerianStudio/lib-commons/v5 => ../../../../..
