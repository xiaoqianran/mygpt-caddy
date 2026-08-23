package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T, inline int) *Server {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{
		ListenAddr: "127.0.0.1:0", APIToken: "test-token", ActionBaseURL: "https://action.example.com",
		WorkspaceRoot: dir, StateDir: filepath.Join(dir, "state"), CommandTimeout: 2 * time.Second,
		InlineOutputChars: inline, MaxArtifactBytes: 10_000_000, ArtifactTTL: time.Minute,
		MaxRequestBytes: 99_000, MaxInputFileBytes: 10_000_000,
		AllowedGPTIDs: map[string]struct{}{}, AllowedUploadHosts: []string{".oaiusercontent.com"},
	}
	s, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func call(t *testing.T, s *Server, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/command/run", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}

func TestCommandFailureIsHTTP200(t *testing.T) {
	s := testServer(t, 30_000)
	w := call(t, s, `{"command":"echo problem >&2; exit 7"}`, map[string]string{"Authorization": "Bearer test-token"})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var result commandResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 7 || !strings.Contains(result.Stderr, "problem") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestConversationWorkdirPersists(t *testing.T) {
	s := testServer(t, 30_000)
	headers := map[string]string{"Authorization": "Bearer test-token", "Openai-Conversation-Id": "conversation-1"}
	w := call(t, s, `{"command":"mkdir child && cd child && pwd"}`, headers)
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	w = call(t, s, `{"command":"pwd"}`, headers)
	var result commandResponse
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if !strings.HasSuffix(strings.TrimSpace(result.Stdout), "/child") || !strings.HasSuffix(result.Workdir, "/child") {
		t.Fatalf("workdir did not persist: %+v", result)
	}
}

func TestLargeOutputBecomesSignedFile(t *testing.T) {
	s := testServer(t, 10)
	w := call(t, s, `{"command":"printf 12345678901234567890"}`, map[string]string{"Authorization": "Bearer test-token"})
	var result commandResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.OpenAIFileResponse) != 1 {
		t.Fatalf("expected attachment: %+v", result)
	}
	path := strings.TrimPrefix(result.OpenAIFileResponse[0], "https://action.example.com")
	req := httptest.NewRequest(http.MethodGet, path, nil)
	download := httptest.NewRecorder()
	s.ServeHTTP(download, req)
	if download.Code != http.StatusOK || download.Body.String() != "12345678901234567890" {
		t.Fatalf("download %d: %q", download.Code, download.Body.String())
	}
	if !strings.Contains(download.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("missing attachment header")
	}
}

func TestTimeoutKillsProcessGroup(t *testing.T) {
	s := testServer(t, 30_000)
	s.cfg.CommandTimeout = 150 * time.Millisecond
	started := time.Now()
	w := call(t, s, `{"command":"sleep 5"}`, map[string]string{"Authorization": "Bearer test-token"})
	if time.Since(started) > time.Second {
		t.Fatal("command was not killed promptly")
	}
	var result commandResponse
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if !result.TimedOut {
		t.Fatalf("expected timeout: %+v", result)
	}
}

func TestAuthAndStrictJSON(t *testing.T) {
	s := testServer(t, 30_000)
	if w := call(t, s, `{"command":"true"}`, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	} else if w.Header().Get("X-Request-Id") == "" {
		t.Fatal("missing request correlation ID")
	}
	w := call(t, s, `{"command":"true","unknown":1}`, map[string]string{"Authorization": "Bearer test-token"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestOpenAPIIsValidAndRendered(t *testing.T) {
	s := testServer(t, 30_000)
	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("{{ACTION_BASE_URL}}")) {
		t.Fatal("base URL was not rendered")
	}
	if doc["openapi"] != "3.1.0" {
		t.Fatalf("unexpected document: %v", doc)
	}
	paths := doc["paths"].(map[string]any)
	operationIDs := map[string]bool{}
	operations := 0
	validID := regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	for _, pathValue := range paths {
		for method, operationValue := range pathValue.(map[string]any) {
			if method != "get" && method != "post" && method != "put" && method != "patch" && method != "delete" {
				continue
			}
			operations++
			operation := operationValue.(map[string]any)
			for _, field := range []string{"summary", "description"} {
				if len([]rune(operation[field].(string))) > 300 {
					t.Fatalf("%s exceeds 300 characters", field)
				}
			}
			id := operation["operationId"].(string)
			if !validID.MatchString(id) || operationIDs[id] {
				t.Fatalf("invalid or duplicate operationId %q", id)
			}
			operationIDs[id] = true
		}
	}
	if operations > 30 {
		t.Fatalf("too many operations: %d", operations)
	}
	components := doc["components"].(map[string]any)
	if _, ok := components["schemas"].(map[string]any); !ok {
		t.Fatal("components.schemas must be an object")
	}
}

func TestSafeFilename(t *testing.T) {
	if got := safeFilename("../../hello world.zip", 0); got != "hello_world.zip" {
		t.Fatalf("got %q", got)
	}
	if err := os.WriteFile(filepath.Join(t.TempDir(), "ok"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}
