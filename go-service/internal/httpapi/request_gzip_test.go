package httpapi

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func gzipBytes(t *testing.T, text string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestGzipRequestMiddlewareUnpacksBodies(t *testing.T) {
	var seen, seenEncoding string
	var readErr error
	h := gzipRequestMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		seen, seenEncoding, readErr = string(body), r.Header.Get("Content-Encoding"), err
		w.WriteHeader(http.StatusNoContent)
	}))
	send := func(body []byte, encoding string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/turn", bytes.NewReader(body))
		if encoding != "" {
			req.Header.Set("Content-Encoding", encoding)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := send(gzipBytes(t, `{"turn":1}`), "gzip"); rec.Code != http.StatusNoContent || seen != `{"turn":1}` || seenEncoding != "" {
		t.Fatalf("gzip body: code=%d seen=%q encoding=%q", rec.Code, seen, seenEncoding)
	}
	if rec := send([]byte(`{"turn":2}`), ""); rec.Code != http.StatusNoContent || seen != `{"turn":2}` {
		t.Fatalf("plain body: code=%d seen=%q", rec.Code, seen)
	}
	if rec := send([]byte("not gzip"), "gzip"); rec.Code != http.StatusBadRequest {
		t.Fatalf("broken gzip: code=%d", rec.Code)
	}
	if rec := send([]byte("x"), "br"); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("unknown encoding: code=%d", rec.Code)
	}

	defer func(limit int64) { maxGzipRequestBodyBytes = limit }(maxGzipRequestBodyBytes)
	maxGzipRequestBodyBytes = 16
	send(gzipBytes(t, strings.Repeat("a", 64)), "gzip")
	if readErr != errGzipRequestBodyTooLarge || len(seen) > 17 {
		t.Fatalf("oversized body: err=%v read=%d bytes", readErr, len(seen))
	}
}
