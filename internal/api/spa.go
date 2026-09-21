package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// NewSPAHandler serves a Vite production build and falls back to index.html
// for browser routes. API routes remain owned by the more specific ServeMux
// patterns registered by NewServer.
func NewSPAHandler(root string) (http.Handler, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	indexPath := filepath.Join(absRoot, "index.html")
	info, err := os.Stat(indexPath)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errors.New("frontend index.html is a directory")
	}
	files := http.FileServer(http.Dir(absRoot))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := filepath.Clean("/" + r.URL.Path)
		relative := strings.TrimPrefix(clean, string(filepath.Separator))
		candidate := filepath.Join(absRoot, relative)
		if stat, statErr := os.Stat(candidate); statErr == nil && !stat.IsDir() {
			if strings.HasPrefix(filepath.ToSlash(relative), "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, indexPath)
	}), nil
}
