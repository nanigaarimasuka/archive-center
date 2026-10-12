package httpapi

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/http/pprof"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TEMPORARY diagnostics for perf/lean-diag only. Profiles are served under the
// normal route prefix, so they sit behind the deployment's reverse-proxy login.
// Do not merge into perf/exact or perf/lean.
func registerDiagPprofRoutes(sub *http.ServeMux) {
	installDiagTape()
	sub.HandleFunc("GET /debug/pprof/", pprof.Index)
	sub.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	sub.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	sub.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	sub.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
}

// diagCapturePrepareTurn saves prepare-turn request bodies (already
// unpacked) into AC_DIAG_CAPTURE_DIR, so a real turn can be replayed while
// measuring memory. The bodies hold the conversation: the directory is
// private and the files are removed after the measurement.
func diagCapturePrepareTurn(next http.Handler) http.Handler {
	dir := strings.TrimSpace(os.Getenv("AC_DIAG_CAPTURE_DIR"))
	if dir == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/prepare-turn") {
			body, err := io.ReadAll(r.Body)
			_ = r.Body.Close()
			if err == nil {
				name := filepath.Join(dir, "prepare-turn-"+time.Now().Format("150405.000")+".json")
				_ = os.WriteFile(name, body, 0o600)
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
		}
		next.ServeHTTP(w, r)
	})
}

// diagTape records outbound HTTP responses (Chroma, embeddings) in
// AC_DIAG_TAPE_DIR when AC_DIAG_TAPE_MODE=record, and serves them back by
// method, URL and body when it is "replay", so two builds see the same
// search results for the same request.
type diagTape struct {
	next   http.RoundTripper
	dir    string
	replay bool
}

func (t diagTape) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.String()+"\n"), body...))
	name := filepath.Join(t.dir, hex.EncodeToString(sum[:])+".resp")
	if t.replay {
		saved, err := os.ReadFile(name)
		if err != nil {
			_ = os.WriteFile(filepath.Join(t.dir, "miss-"+hex.EncodeToString(sum[:8])+".req"), body, 0o600)
			slog.Error("diag tape miss", "method", r.Method, "url", r.URL.String())
			return nil, fmt.Errorf("diag tape miss: %s %s", r.Method, r.URL.String())
		}
		return http.ReadResponse(bufio.NewReader(bytes.NewReader(saved)), r)
	}
	resp, err := t.next.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	dump, err := httputil.DumpResponse(resp, true)
	if err == nil {
		_ = os.WriteFile(name, dump, 0o600)
		_ = os.WriteFile(strings.TrimSuffix(name, ".resp")+".req", body, 0o600)
	}
	return resp, nil
}

// diagTapeInstalled turns off timing-dependent embedding sharing, whose
// query shapes depend on which concurrent search claims an id first.
var diagTapeInstalled bool

func installDiagTape() {
	dir := strings.TrimSpace(os.Getenv("AC_DIAG_TAPE_DIR"))
	if dir == "" {
		return
	}
	replay := strings.TrimSpace(os.Getenv("AC_DIAG_TAPE_MODE")) == "replay"
	diagTapeInstalled = true
	http.DefaultTransport = diagTape{next: http.DefaultTransport, dir: dir, replay: replay}
	proxyHTTPClient = &http.Client{Transport: http.DefaultTransport}
	slog.Info("diag tape installed", "dir", dir, "replay", replay)
}
