package canonpack

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

func TestNeutralManifestFixturePassesPreview(t *testing.T) {
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

	report := PreviewZIP(archive.Bytes(), "3.1.0")
	if !report.Valid {
		t.Fatalf("neutral manifest fixture failed preview: %#v", report.Diagnostics)
	}
	if report.ValidationProfile != ValidationProfile || report.Summary.PackID == "" || report.Summary.StableWorkID == "" {
		t.Fatalf("preview summary/profile missing: %#v", report)
	}
}

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

func TestInspectZIPBindsExactArchiveAndManifestBytes(t *testing.T) {
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

	inspection, err := InspectZIP(archive.Bytes(), "3.1.0")
	if err != nil {
		t.Fatal(err)
	}
	archiveHash := sha256.Sum256(archive.Bytes())
	manifestHash := sha256.Sum256(manifest)
	if inspection.ArchiveSHA256 != hex.EncodeToString(archiveHash[:]) {
		t.Fatalf("archive hash=%q", inspection.ArchiveSHA256)
	}
	if inspection.ManifestSHA256 != hex.EncodeToString(manifestHash[:]) {
		t.Fatalf("manifest hash=%q", inspection.ManifestSHA256)
	}
	if !bytes.Equal(inspection.ManifestJSON, manifest) {
		t.Fatal("inspection did not preserve the exact manifest bytes")
	}
}
