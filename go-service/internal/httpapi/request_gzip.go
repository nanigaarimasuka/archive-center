package httpapi

import (
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"strings"
)

// maxGzipRequestBodyBytes caps a gzip request body once it is unpacked, the
// same as the largest uncompressed body a handler accepts.
var maxGzipRequestBodyBytes int64 = 64 << 20

// gzipRequestMiddleware unpacks request bodies the plugin sent with
// Content-Encoding: gzip, so handlers read plain JSON as before.
func gzipRequestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		encoding := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
		if encoding == "" || encoding == "identity" {
			next.ServeHTTP(w, r)
			return
		}
		if encoding != "gzip" {
			writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"status": "error", "code": "unsupported_content_encoding", "detail": "only gzip request bodies are accepted"})
			return
		}
		reader, err := gzip.NewReader(r.Body)
		if err != nil {
			writeBadRequest(w, "gzip request body is invalid: "+err.Error())
			return
		}
		r.Body = &gzipRequestBody{reader: reader, limited: io.LimitReader(reader, maxGzipRequestBodyBytes+1), raw: r.Body}
		r.Header.Del("Content-Encoding")
		r.Header.Del("Content-Length")
		r.ContentLength = -1
		next.ServeHTTP(w, r)
	})
}

var errGzipRequestBodyTooLarge = errors.New("gzip request body is larger than allowed once unpacked")

type gzipRequestBody struct {
	reader  *gzip.Reader
	limited io.Reader
	raw     io.ReadCloser
	read    int64
}

func (b *gzipRequestBody) Read(p []byte) (int, error) {
	n, err := b.limited.Read(p)
	b.read += int64(n)
	if b.read > maxGzipRequestBodyBytes {
		return n, errGzipRequestBodyTooLarge
	}
	return n, err
}

func (b *gzipRequestBody) Close() error {
	_ = b.reader.Close()
	return b.raw.Close()
}
