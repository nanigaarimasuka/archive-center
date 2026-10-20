package canonpack

import (
	"archive/zip"
	"bytes"
	"os"
	"testing"
)

// A fork build (upstream release + fork revision) is compatible as the
// upstream release it is based on. The fixture supports 3.1.0 up to 4.0.0.
func TestForkVersionIsCompatibleAsItsUpstreamRelease(t *testing.T) {
	manifest, err := os.ReadFile("testdata/neutral-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	w, err := zw.Create(ManifestName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		version string
		valid   bool
	}{
		{"3.1.0.1", true},
		{"3.9.9.12", true},
		{"3.0.9.1", false},
		{"4.0.0.1", false},
		{"3.1.0.01", false},
	} {
		report := PreviewZIP(archive.Bytes(), tc.version)
		if report.Valid != tc.valid {
			t.Fatalf("version %s: valid=%v, want %v: %#v", tc.version, report.Valid, tc.valid, report.Diagnostics)
		}
	}
}
