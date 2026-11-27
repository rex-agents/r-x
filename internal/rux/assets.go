package rux

import (
	"crypto/subtle"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Assets are files a guest shares into its thread (`rux share`, `rux
// screenshot`): screenshots, recordings, PDFs, logs. They're stored with the
// thread and shown inline in the app.

const maxAssetBytes = 25 << 20

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safeAssetName(name string) string {
	name = strings.Trim(unsafeName.ReplaceAllString(filepath.Base(name), "-"), "-.")
	if len(name) > 80 {
		ext := filepath.Ext(name)
		if len(ext) > 12 {
			ext = ""
		}
		name = name[:80-len(ext)] + ext
	}
	if name == "" {
		name = "file"
	}
	return name
}

// assetType is the content type the app serves an asset with. Only types that
// are safe to render inline get their real type; everything else is served as
// a download.
func assetType(name string) (ctype string, inline bool) {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".svg":
		return mime.TypeByExtension(ext), true
	case ".mp4", ".webm", ".mov":
		if ext == ".mov" {
			return "video/quicktime", true
		}
		return mime.TypeByExtension(ext), true
	case ".pdf":
		return "application/pdf", true
	case ".txt", ".log", ".md", ".json", ".csv", ".diff", ".patch", ".yaml", ".yml", ".html":
		return "text/plain; charset=utf-8", true // shown as text, never rendered as HTML
	}
	return "application/octet-stream", false
}

func (t *thread) assetsDir() string { return filepath.Join(t.dir, "assets") }

// handleAssetUpload is on the runner listener: POST /assets?thread=ID with the
// thread token, the file as the body, and X-Rux-Name / X-Rux-Caption headers.
func (s *server) handleAssetUpload(w http.ResponseWriter, r *http.Request) {
	t := s.get(r.URL.Query().Get("thread"))
	if t == nil {
		http.Error(w, "forbidden", 403)
		return
	}
	t.mu.Lock()
	tok, gone := t.meta.Token, t.meta.Status == "destroyed"
	t.mu.Unlock()
	if gone || subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")), []byte(tok)) != 1 {
		http.Error(w, "forbidden", 403)
		return
	}
	name, _ := url.QueryUnescape(r.Header.Get("X-Rux-Name"))
	caption, _ := url.QueryUnescape(r.Header.Get("X-Rux-Caption"))
	if len(caption) > 2000 {
		caption = caption[:2000]
	}
	name = safeAssetName(name)
	if err := os.MkdirAll(t.assetsDir(), 0o700); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	f, err := os.CreateTemp(t.assetsDir(), ".upload-*")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer os.Remove(f.Name())
	n, err := io.Copy(f, http.MaxBytesReader(w, r.Body, maxAssetBytes))
	f.Close()
	if err != nil {
		http.Error(w, fmt.Sprintf("upload failed (limit %d MB): %v", maxAssetBytes>>20, err), 413)
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	entries, _ := os.ReadDir(t.assetsDir())
	file := fmt.Sprintf("%03d-%s", len(entries), name) // entries includes the temp file, so this starts at 001
	if err := os.Rename(f.Name(), filepath.Join(t.assetsDir(), file)); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	ctype, inline := assetType(file)
	u := "/api/threads/" + url.PathEscape(t.meta.ID) + "/assets/" + url.PathEscape(file)
	t.appendLocked("rux", 0, ruxEv("rux_asset", "file", file, "name", name, "caption", caption,
		"mime", strings.Split(ctype, ";")[0], "inline", inline, "size", n, "url", u, "quiet", r.Header.Get("X-Rux-Quiet") != ""))
	writeJSONResp(w, 201, map[string]any{"url": u, "file": file, "size": n})
}

func (s *server) handleAssetGet(w http.ResponseWriter, r *http.Request) {
	t := s.get(r.PathValue("id"))
	file := r.PathValue("file")
	if t == nil || file != filepath.Base(file) || strings.HasPrefix(file, ".") {
		http.NotFound(w, r)
		return
	}
	ctype, inline := assetType(file)
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	// Even an SVG opened directly can't run script or reach anything.
	csp := "default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'"
	if ctype != "application/pdf" { // browsers' PDF viewers don't run in a sandboxed document
		csp += "; sandbox"
	}
	h.Set("Content-Security-Policy", csp)
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	if !inline || r.URL.Query().Has("download") {
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": file}))
	}
	f, err := os.Open(filepath.Join(t.assetsDir(), file))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, "", st.ModTime(), f) // empty name: keep our Content-Type
}
