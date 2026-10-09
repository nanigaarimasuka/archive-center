package httpapi

import (
	"net/http"
	"net/http/pprof"
)

// TEMPORARY diagnostics for perf/lean-diag only. Profiles are served under the
// normal route prefix, so they sit behind the deployment's reverse-proxy login.
// Do not merge into perf/exact or perf/lean.
func registerDiagPprofRoutes(sub *http.ServeMux) {
	sub.HandleFunc("GET /debug/pprof/", pprof.Index)
	sub.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	sub.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	sub.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	sub.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
}
