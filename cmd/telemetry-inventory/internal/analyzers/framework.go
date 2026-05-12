package analyzers

import (
	"go/ast"
	"reflect"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// FrameworkAnalyzer detects known OpenTelemetry auto-instrumentation hooks.
var FrameworkAnalyzer = &analysis.Analyzer{
	Name:       "framework",
	Doc:        "Detects framework auto-instrumentation integrations.",
	Run:        runFramework,
	Requires:   []*analysis.Analyzer{inspect.Analyzer},
	ResultType: reflect.TypeFor[*FrameworkFindings](),
}

type frameworkSpec struct {
	Package     string
	Symbol      string
	Framework   string
	AutoMetrics []string
	AutoSpans   []string
}

const (
	libHTTPPkg         = "github.com/LerianStudio/lib-commons/v5/commons/net/http"
	autoMetricDuration = "http.server.duration"
	autoMetricReqSize  = "http.server.request.size"
	autoSpanHTTPRoute  = "HTTP route"
)

var frameworkSpecs = []frameworkSpec{
	{Package: "github.com/gofiber/contrib/otelfiber", Symbol: "Middleware", Framework: "fiber/otelfiber", AutoMetrics: []string{autoMetricDuration, autoMetricReqSize}, AutoSpans: []string{autoSpanHTTPRoute}},
	{Package: "go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp", Symbol: "NewHandler", Framework: "net/http/otelhttp", AutoMetrics: []string{autoMetricDuration, autoMetricReqSize}, AutoSpans: []string{autoSpanHTTPRoute}},
	{Package: "go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp", Symbol: "NewMiddleware", Framework: "net/http/otelhttp", AutoMetrics: []string{autoMetricDuration, autoMetricReqSize}, AutoSpans: []string{autoSpanHTTPRoute}},
	{Package: "go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc", Symbol: "NewServerHandler", Framework: "grpc/otelgrpc", AutoMetrics: []string{"rpc.server.duration"}, AutoSpans: []string{"gRPC server"}},
	{Package: "go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc", Symbol: "NewClientHandler", Framework: "grpc/otelgrpc", AutoMetrics: []string{"rpc.client.duration"}, AutoSpans: []string{"gRPC client"}},
	{Package: libHTTPPkg, Symbol: "NewTelemetryMiddleware", Framework: "lib-commons/http-telemetry", AutoMetrics: []string{autoMetricDuration}, AutoSpans: []string{autoSpanHTTPRoute}},
	{Package: libHTTPPkg, Symbol: "WithGrpcLogging", Framework: "lib-commons/grpc-logging", AutoSpans: []string{"gRPC access log correlation"}},
	{Package: libHTTPPkg, Symbol: "WithHTTPLogging", Framework: "lib-commons/http-logging", AutoSpans: []string{"HTTP access log correlation"}},
	{Package: "github.com/jackc/pgx/v5/tracelog", Symbol: "TraceLog", Framework: "pgx/tracelog", AutoSpans: []string{"PostgreSQL query"}},
}

func runFramework(pass *analysis.Pass) (any, error) {
	insp, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok || insp == nil {
		return &FrameworkFindings{}, nil
	}

	out := &FrameworkFindings{}

	insp.Preorder([]ast.Node{(*ast.SelectorExpr)(nil)}, func(n ast.Node) {
		sel, _ := n.(*ast.SelectorExpr)
		if sel == nil {
			return
		}

		pkg := importedPkgPath(pass, sel.X)
		if pkg == "" {
			return
		}

		for _, spec := range frameworkSpecs {
			if spec.Package == pkg && spec.Symbol == sel.Sel.Name {
				out.Frameworks = append(out.Frameworks, schema.FrameworkInstrumentation{
					Framework:     spec.Framework,
					EmissionSites: []schema.EmissionSite{siteFor(pass, sel.Pos(), 0)},
					AutoMetrics:   append([]string(nil), spec.AutoMetrics...),
					AutoSpans:     append([]string(nil), spec.AutoSpans...),
				})
				pass.Reportf(sel.Pos(), "framework %q", spec.Framework)

				return
			}
		}
	})

	return out, nil
}
