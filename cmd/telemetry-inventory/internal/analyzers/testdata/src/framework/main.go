package framework

import (
	"net/http"

	libhttp "github.com/LerianStudio/lib-commons/v5/commons/net/http"
	"github.com/gofiber/contrib/otelfiber"
	"github.com/jackc/pgx/v5/tracelog"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Each entry in analyzers.frameworkSpecs is exercised here so the analyzer's
// auto_metrics / auto_spans wiring is covered without depending on a real
// service fixture.
func wire(h http.Handler) {
	_ = otelfiber.Middleware()                  // want `framework "fiber/otelfiber"`
	_ = otelhttp.NewHandler(h, "GET /accounts") // want `framework "net/http/otelhttp"`
	_ = otelhttp.NewMiddleware("GET /balances") // want `framework "net/http/otelhttp"`
	_ = otelgrpc.NewServerHandler()             // want `framework "grpc/otelgrpc"`
	_ = otelgrpc.NewClientHandler()             // want `framework "grpc/otelgrpc"`
	_ = libhttp.NewTelemetryMiddleware(nil)     // want `framework "lib-commons/http-telemetry"`
	_ = libhttp.WithGrpcLogging(nil)            // want `framework "lib-commons/grpc-logging"`
	_ = libhttp.WithHTTPLogging(nil)            // want `framework "lib-commons/http-logging"`

	var _ tracelog.TraceLog // want `framework "pgx/tracelog"`
}
