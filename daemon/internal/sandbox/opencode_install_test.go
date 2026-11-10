package sandbox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func testPackage(t *testing.T) ([]byte, string) {
	t.Helper()
	content := []byte("fake opencode binary")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "package/bin/opencode", Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha512.Sum512(buf.Bytes())
	return buf.Bytes(), "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

func TestDownloadOpenCode(t *testing.T) {
	pkg, integrity := testPackage(t)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pkg.tgz" {
			w.Write(pkg)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"dist": map[string]string{"tarball": srv.URL + "/pkg.tgz", "integrity": integrity},
		})
	}))
	defer srv.Close()
	old := openCodeRegistry
	openCodeRegistry = srv.URL
	defer func() { openCodeRegistry = old }()

	file, err := downloadOpenCode(context.Background(), "2.2.6")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file)
	b, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(b, pkg) {
		t.Fatalf("downloaded package differs: %v", err)
	}
}

func TestDownloadOpenCodeChecksumMismatch(t *testing.T) {
	pkg, _ := testPackage(t)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pkg.tgz" {
			w.Write(pkg)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"dist": map[string]string{"tarball": srv.URL + "/pkg.tgz", "integrity": "sha512-AAAA"},
		})
	}))
	defer srv.Close()
	old := openCodeRegistry
	openCodeRegistry = srv.URL
	defer func() { openCodeRegistry = old }()

	if _, err := downloadOpenCode(context.Background(), "2.2.6"); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("checksum mismatch not detected: %v", err)
	}
}

func TestDownloadOpenCodeMetadataError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	old := openCodeRegistry
	openCodeRegistry = srv.URL
	defer func() { openCodeRegistry = old }()

	if _, err := downloadOpenCode(context.Background(), "2.2.6"); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("metadata error not reported: %v", err)
	}
}
