package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type fileRef struct {
	Name         string `json:"name"`
	ID           string `json:"id"`
	MIMEType     string `json:"mime_type"`
	DownloadLink string `json:"download_link"`
}

type savedFile struct {
	Name     string `json:"name"`
	ID       string `json:"id,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	Path     string `json:"path"`
}

type downloadStats struct {
	SourceHost string
	FinalHost  string
	Status     int
	Bytes      int64
	Redirects  int
	DurationMS int64
}

var unsafeFilename = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (s *Server) downloadFiles(ctx context.Context, raw []json.RawMessage, requestID string) ([]savedFile, string, error) {
	if len(raw) == 0 {
		s.auditEvent(ctx, "upload.batch", "skipped", map[string]any{"file_count": 0})
		return nil, "", nil
	}
	s.auditEvent(ctx, "upload.batch", "started", map[string]any{"file_count": len(raw)})
	if len(raw) > 10 {
		s.auditEvent(ctx, "upload.validation", "failed", map[string]any{"reason": "too_many_files", "file_count": len(raw)})
		return nil, "", errors.New("openaiFileIdRefs accepts at most 10 files")
	}
	dir := filepath.Join(s.cfg.StateDir, "uploads", requestID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.auditEvent(ctx, "upload.directory", "failed", map[string]any{"error": err.Error()})
		return nil, "", err
	}
	s.auditEvent(ctx, "upload.directory", "created", map[string]any{"path": dir})
	refs := make([]fileRef, len(raw))
	names := make([]string, len(raw))
	usedNames := make(map[string]struct{}, len(raw))
	for i, item := range raw {
		if len(item) == 0 || item[0] == '"' {
			_ = os.RemoveAll(dir)
			s.auditEvent(ctx, "upload.validation", "failed", map[string]any{"index": i, "reason": "reference_not_expanded"})
			return nil, "", errors.New("file reference was not expanded by ChatGPT; retry with the uploaded file attached")
		}
		if err := json.Unmarshal(item, &refs[i]); err != nil || refs[i].DownloadLink == "" {
			_ = os.RemoveAll(dir)
			s.auditEvent(ctx, "upload.validation", "failed", map[string]any{"index": i, "reason": "invalid_reference"})
			return nil, "", errors.New("invalid openaiFileIdRefs entry")
		}
		name := safeFilename(refs[i].Name, i)
		if _, exists := usedNames[name]; exists {
			name = fmt.Sprintf("%d-%s", i+1, name)
		}
		usedNames[name] = struct{}{}
		names[i] = name
		s.auditEvent(ctx, "upload.validation", "succeeded", map[string]any{
			"index": i, "name": name, "file_id": refs[i].ID, "mime_type": refs[i].MIMEType,
			"source_host": linkHost(refs[i].DownloadLink),
		})
	}
	files := make([]savedFile, len(raw))
	errCh := make(chan error, len(raw))
	var wg sync.WaitGroup
	for i, ref := range refs {
		wg.Add(1)
		go func(i int, ref fileRef, name string) {
			defer wg.Done()
			path := filepath.Join(dir, name)
			s.auditEvent(ctx, "upload.download", "started", map[string]any{
				"index": i, "name": name, "file_id": ref.ID, "mime_type": ref.MIMEType,
				"source_host": linkHost(ref.DownloadLink),
			})
			stats, err := s.downloadOne(ctx, ref.DownloadLink, path)
			data := map[string]any{
				"index": i, "name": name, "file_id": ref.ID, "source_host": stats.SourceHost,
				"final_host": stats.FinalHost, "http_status": stats.Status, "bytes": stats.Bytes,
				"redirects": stats.Redirects, "duration_ms": stats.DurationMS,
			}
			if err != nil {
				data["error"] = err.Error()
				s.auditEvent(ctx, "upload.download", "failed", data)
				errCh <- fmt.Errorf("download %s: %w", name, err)
				return
			}
			s.auditEvent(ctx, "upload.download", "succeeded", data)
			files[i] = savedFile{Name: name, ID: ref.ID, MIMEType: ref.MIMEType, Path: path}
		}(i, ref, names[i])
	}
	wg.Wait()
	close(errCh)
	if err := <-errCh; err != nil {
		_ = os.RemoveAll(dir)
		return nil, "", err
	}
	s.auditEvent(ctx, "upload.batch", "succeeded", map[string]any{"file_count": len(files)})
	return files, dir, nil
}

func (s *Server) downloadOne(ctx context.Context, link, path string) (stats downloadStats, resultErr error) {
	started := time.Now()
	defer func() { stats.DurationMS = time.Since(started).Milliseconds() }()
	u, err := url.Parse(link)
	if u != nil {
		stats.SourceHost = strings.ToLower(u.Hostname())
	}
	if err != nil || u.Scheme != "https" || !s.allowedUploadHost(u.Hostname()) {
		return stats, errors.New("download_link must use HTTPS on an allowed OpenAI file host")
	}
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: s.uploadTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			stats.Redirects = len(via)
			if len(via) > 3 || req.URL.Scheme != "https" || !s.allowedUploadHost(req.URL.Hostname()) {
				return errors.New("disallowed file redirect")
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return stats, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return stats, err
	}
	defer resp.Body.Close()
	stats.Status = resp.StatusCode
	stats.FinalHost = strings.ToLower(resp.Request.URL.Hostname())
	if resp.StatusCode != http.StatusOK {
		return stats, fmt.Errorf("remote returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > s.cfg.MaxInputFileBytes {
		return stats, errors.New("file exceeds configured size limit")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return stats, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(path)
		}
	}()
	written, copyErr := io.Copy(file, io.LimitReader(resp.Body, s.cfg.MaxInputFileBytes+1))
	stats.Bytes = written
	closeErr := file.Close()
	if copyErr != nil {
		return stats, copyErr
	}
	if closeErr != nil {
		return stats, closeErr
	}
	if written > s.cfg.MaxInputFileBytes {
		return stats, errors.New("file exceeds configured size limit")
	}
	keep = true
	return stats, nil
}

func linkHost(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func (s *Server) allowedUploadHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, allowed := range s.cfg.AllowedUploadHosts {
		if strings.HasPrefix(allowed, ".") {
			base := strings.TrimPrefix(allowed, ".")
			if host == base || strings.HasSuffix(host, allowed) {
				return true
			}
		} else if host == allowed {
			return true
		}
	}
	return false
}

func safeFilename(name string, index int) string {
	name = filepath.Base(strings.TrimSpace(name))
	name = unsafeFilename.ReplaceAllString(name, "_")
	name = strings.Trim(name, "._-")
	if name == "" {
		name = fmt.Sprintf("file-%d", index+1)
	}
	if len(name) > 160 {
		name = name[:160]
	}
	return name
}
