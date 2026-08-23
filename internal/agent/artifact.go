package agent

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type artifact struct {
	id, path, name, mime string
	size                 int64
	expires              time.Time
}

type artifactStore struct {
	mu       sync.Mutex
	dir      string
	key      []byte
	ttl      time.Duration
	maxBytes int64
	items    map[string]artifact
}

func newArtifactStore(dir, token string, ttl time.Duration, maxBytes int64) (*artifactStore, error) {
	dir = filepath.Join(dir, "files")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create artifact directory: %w", err)
	}
	// Link metadata is intentionally process-local, so crash leftovers are
	// never valid and can be removed at startup.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read artifact directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
	key := sha256.Sum256([]byte("mygpt-caddy-file-links\x00" + token))
	s := &artifactStore{dir: dir, key: key[:], ttl: ttl, maxBytes: maxBytes, items: make(map[string]artifact)}
	go s.cleanupLoop()
	return s, nil
}

type cappedFile struct {
	file      *os.File
	written   int64
	limit     int64
	truncated bool
}

func (w *cappedFile) Write(p []byte) (int, error) {
	original := len(p)
	remaining := w.limit - w.written
	if remaining <= 0 {
		w.truncated = true
		return original, nil
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
		w.truncated = true
	}
	n, err := w.file.Write(p)
	w.written += int64(n)
	if err != nil {
		return n, err
	}
	return original, nil
}

func (s *artifactStore) newCapture(stream string) (*cappedFile, artifact, error) {
	id, err := randomID()
	if err != nil {
		return nil, artifact{}, err
	}
	name := id + "-" + stream + ".log"
	path := filepath.Join(s.dir, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, artifact{}, err
	}
	a := artifact{id: id, path: path, name: name, mime: "text/plain; charset=utf-8"}
	return &cappedFile{file: file, limit: s.maxBytes}, a, nil
}

func (s *artifactStore) discard(a artifact) { _ = os.Remove(a.path) }

func (s *artifactStore) publish(a artifact, size int64) artifact {
	a.size = size
	a.expires = time.Now().Add(s.ttl)
	s.mu.Lock()
	s.items[a.id] = a
	s.mu.Unlock()
	return a
}

func (s *artifactStore) URL(baseURL string, a artifact) string {
	expires := strconv.FormatInt(a.expires.Unix(), 10)
	mac := hmac.New(sha256.New, s.key)
	_, _ = io.WriteString(mac, a.id+"\x00"+a.name+"\x00"+expires)
	signature := hex.EncodeToString(mac.Sum(nil))
	return baseURL + "/v1/files/download/" + url.PathEscape(a.id) + "/" + url.PathEscape(a.name) +
		"?expires=" + expires + "&signature=" + signature
}

func (s *artifactStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if r.Method != http.MethodGet {
		_ = appendAudit(r.Context(), "artifact.download", "failed", map[string]any{"reason": "method_not_allowed", "method": r.Method})
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/files/download/"), "/")
	if len(parts) != 2 {
		_ = appendAudit(r.Context(), "artifact.download", "failed", map[string]any{"reason": "invalid_path"})
		http.NotFound(w, r)
		return
	}
	id, name := parts[0], parts[1]
	_ = appendAudit(r.Context(), "artifact.download", "started", map[string]any{"artifact_id": id, "name": name})
	expires, err := strconv.ParseInt(r.URL.Query().Get("expires"), 10, 64)
	if err != nil || time.Now().Unix() > expires {
		_ = appendAudit(r.Context(), "artifact.download", "failed", map[string]any{"artifact_id": id, "name": name, "reason": "expired"})
		http.Error(w, "link expired", http.StatusGone)
		return
	}
	mac := hmac.New(sha256.New, s.key)
	_, _ = io.WriteString(mac, id+"\x00"+name+"\x00"+strconv.FormatInt(expires, 10))
	want, err := hex.DecodeString(r.URL.Query().Get("signature"))
	if err != nil || !hmac.Equal(want, mac.Sum(nil)) {
		_ = appendAudit(r.Context(), "artifact.download", "failed", map[string]any{"artifact_id": id, "name": name, "reason": "invalid_signature"})
		http.Error(w, "invalid signature", http.StatusForbidden)
		return
	}
	s.mu.Lock()
	a, ok := s.items[id]
	s.mu.Unlock()
	if !ok || a.name != name || a.expires.Unix() != expires {
		_ = appendAudit(r.Context(), "artifact.download", "failed", map[string]any{"artifact_id": id, "name": name, "reason": "not_found"})
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(a.path)
	if err != nil {
		_ = appendAudit(r.Context(), "artifact.download", "failed", map[string]any{"artifact_id": id, "name": name, "reason": "open_failed", "error": err.Error()})
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", a.mime)
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": a.name})
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Length", strconv.FormatInt(a.size, 10))
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	written, copyErr := io.Copy(w, file)
	data := map[string]any{
		"artifact_id": id, "name": name, "bytes": written,
		"duration_ms": time.Since(started).Milliseconds(), "error": errorText(copyErr),
	}
	_ = appendAudit(r.Context(), "artifact.download", outcomeFromError(copyErr), data)
}

func (s *artifactStore) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		s.mu.Lock()
		for id, item := range s.items {
			if now.After(item.expires) {
				_ = os.Remove(item.path)
				delete(s.items, id)
			}
		}
		s.mu.Unlock()
	}
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func closeCapture(c *cappedFile) error {
	if c == nil || c.file == nil {
		return errors.New("invalid capture")
	}
	return c.file.Close()
}
