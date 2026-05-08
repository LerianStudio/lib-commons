module example.com/leakspans

go 1.25.0

require go.opentelemetry.io/otel/trace v1.42.0

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	go.opentelemetry.io/otel v1.42.0 // indirect
)

replace github.com/LerianStudio/lib-commons/v5 => ../../../../..
