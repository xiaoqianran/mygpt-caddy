package agent

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
)

//go:embed openapi.json
var openAPITemplate []byte

type Server struct {
	cfg       Config
	log       *slog.Logger
	sessions  *sessionStore
	artifacts *artifactStore
	mux       *http.ServeMux
}

type requestMeta struct {
	requestID, conversationID, userID, gptID, baseURL string
}

func New(cfg Config, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	sessions, err := newSessionStore(cfg.StateDir, cfg.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	artifacts, err := newArtifactStore(cfg.StateDir, cfg.APIToken, cfg.ArtifactTTL, cfg.MaxArtifactBytes)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, log: log, sessions: sessions, artifacts: artifacts, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /health", s.health)
	s.mux.HandleFunc("GET /openapi.json", s.openapi)
	s.mux.Handle("/v1/files/download/", artifacts)
	s.mux.HandleFunc("POST /v1/command/run", s.runCommand)
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	s.mux.ServeHTTP(w, r)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "mygpt-caddy", "version": Version})
}

func (s *Server) openapi(w http.ResponseWriter, r *http.Request) {
	baseURL, err := s.publicBaseURL(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "public URL is not configured")
		return
	}
	doc := strings.ReplaceAll(string(openAPITemplate), "{{ACTION_BASE_URL}}", baseURL)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, doc)
}

func (s *Server) runCommand(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="mygpt-caddy"`)
		writeError(w, http.StatusUnauthorized, "missing or invalid Bearer token")
		return
	}
	gptID := strings.TrimSpace(r.Header.Get("Openai-Gpt-Id"))
	if len(s.cfg.AllowedGPTIDs) > 0 {
		if _, ok := s.cfg.AllowedGPTIDs[strings.ToLower(gptID)]; !ok {
			writeError(w, http.StatusUnauthorized, "GPT is not allowed")
			return
		}
	}
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])); mediaType != "application/json" {
		writeError(w, http.StatusBadRequest, "Content-Type must be application/json")
		return
	}
	body := http.MaxBytesReader(w, r.Body, s.cfg.MaxRequestBytes)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var req commandRequest
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "request body must contain one JSON object")
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		writeError(w, http.StatusBadRequest, "command is required")
		return
	}
	requestID, err := randomID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot create request ID")
		return
	}
	baseURL, err := s.publicBaseURL(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "public URL is not configured")
		return
	}
	meta := requestMeta{
		requestID: requestID, conversationID: r.Header.Get("Openai-Conversation-Id"),
		userID: r.Header.Get("Openai-Ephemeral-User-Id"), gptID: gptID, baseURL: baseURL,
	}
	key := sessionKey(meta.conversationID, meta.userID)
	unlock := s.sessions.lock(key)
	defer unlock()

	timeout := requestTimeout(req.TimeoutSeconds, s.cfg.CommandTimeout)
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	files, inputDir, err := s.downloadFiles(ctx, req.OpenAIFileIDRefs, requestID)
	if inputDir != "" {
		defer os.RemoveAll(inputDir)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, err := s.execute(ctx, req, s.sessions.get(key), inputDir, files, meta)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.sessions.set(key, resp.Workdir); err != nil {
		s.log.Error("persist session", "request_id", requestID, "error", err)
	}
	s.log.Info("command finished", "request_id", requestID, "exit_code", resp.ExitCode,
		"timed_out", resp.TimedOut, "duration_ms", resp.DurationMS, "attachments", len(resp.OpenAIFileResponse))
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) authorized(r *http.Request) bool {
	const prefix = "Bearer "
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	provided := []byte(strings.TrimSpace(strings.TrimPrefix(value, prefix)))
	expected := []byte(s.cfg.APIToken)
	return len(provided) == len(expected) && subtle.ConstantTimeCompare(provided, expected) == 1
}

func (s *Server) publicBaseURL(r *http.Request) (string, error) {
	if s.cfg.ActionBaseURL != "" {
		return s.cfg.ActionBaseURL, nil
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	proto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])
	if proto == "" && r.TLS != nil {
		proto = "https"
	}
	if proto != "https" || host == "" {
		return "", errors.New("no HTTPS public origin")
	}
	u := &url.URL{Scheme: "https", Host: host}
	if u.Hostname() == "" {
		return "", errors.New("invalid public origin")
	}
	return u.String(), nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}
