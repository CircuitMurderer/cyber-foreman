package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSPAHandlerServesAssetsAndFallsBackToIndex(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<main>foreman-ui</main>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "app.js"), []byte("console.log('foreman')"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler, err := NewSPAHandler(root)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		path      string
		contains  string
		immutable bool
	}{
		{path: "/assets/app.js", contains: "console.log", immutable: true},
		{path: "/tasks/task-123", contains: "foreman-ui"},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
		response := recorder.Result()
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK || !strings.Contains(string(body), test.contains) {
			t.Fatalf("GET %s: status=%d body=%s", test.path, response.StatusCode, body)
		}
		if test.immutable != strings.Contains(response.Header.Get("Cache-Control"), "immutable") {
			t.Fatalf("GET %s: Cache-Control=%q", test.path, response.Header.Get("Cache-Control"))
		}
	}
}

func TestSPAHandlerRequiresIndex(t *testing.T) {
	if _, err := NewSPAHandler(t.TempDir()); !errorsIsNotExist(err) {
		t.Fatalf("NewSPAHandler error = %v, want not-exist", err)
	}
}

func errorsIsNotExist(err error) bool { return err != nil && os.IsNotExist(err) }
