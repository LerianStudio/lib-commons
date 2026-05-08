package otelhttp

import "net/http"

func NewHandler(http.Handler, string, ...any) http.Handler         { return nil }
func NewMiddleware(string, ...any) func(http.Handler) http.Handler { return nil }
