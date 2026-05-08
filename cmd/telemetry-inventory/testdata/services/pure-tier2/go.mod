module example.com/puretier2

go 1.25.9

require (
	github.com/LerianStudio/lib-commons/v5 v5.0.0
	go.opentelemetry.io/otel v1.42.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	go.opentelemetry.io/otel/metric v1.42.0 // indirect
	go.uber.org/mock v0.6.0 // indirect
)

replace github.com/LerianStudio/lib-commons/v5 => ../../../../..
