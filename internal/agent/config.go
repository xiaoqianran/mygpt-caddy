package agent

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	Version             = "1.1.0"
	defaultTimeout      = 38 * time.Second
	defaultInlineChars  = 30_000
	defaultArtifactSize = 10_000_000
)

type Config struct {
	ListenAddr         string
	APIToken           string
	ActionBaseURL      string
	WorkspaceRoot      string
	StateDir           string
	CommandTimeout     time.Duration
	InlineOutputChars  int
	MaxArtifactBytes   int64
	ArtifactTTL        time.Duration
	MaxRequestBytes    int64
	MaxInputFileBytes  int64
	AllowedGPTIDs      map[string]struct{}
	AllowedUploadHosts []string
	AuditEnabled       bool
	AuditDir           string
	AuditRetentionDays int
	AuditFsync         bool
	AuditOutputChars   int
}

func LoadConfig() (Config, error) {
	stateDir := env("STATE_DIR", "/var/lib/mygpt-caddy")
	cfg := Config{
		ListenAddr:         env("LISTEN_ADDR", "127.0.0.1:8787"),
		APIToken:           strings.TrimSpace(os.Getenv("API_TOKEN")),
		ActionBaseURL:      strings.TrimRight(strings.TrimSpace(os.Getenv("ACTION_BASE_URL")), "/"),
		WorkspaceRoot:      env("WORKSPACE_ROOT", "/root"),
		StateDir:           stateDir,
		CommandTimeout:     secondsEnv("COMMAND_TIMEOUT_SECONDS", defaultTimeout),
		InlineOutputChars:  intEnv("INLINE_OUTPUT_CHARS", defaultInlineChars),
		MaxArtifactBytes:   int64Env("MAX_ARTIFACT_BYTES", defaultArtifactSize),
		ArtifactTTL:        secondsEnv("ARTIFACT_TTL_SECONDS", 15*time.Minute),
		MaxRequestBytes:    99_000,
		MaxInputFileBytes:  int64Env("MAX_INPUT_FILE_BYTES", defaultArtifactSize),
		AllowedGPTIDs:      splitSet(os.Getenv("ALLOWED_GPT_IDS")),
		AllowedUploadHosts: splitList(env("ALLOWED_UPLOAD_HOSTS", ".oaiusercontent.com")),
		AuditEnabled:       boolEnv("AUDIT_ENABLED", true),
		AuditDir:           env("AUDIT_DIR", filepath.Join(stateDir, "audit")),
		AuditRetentionDays: intEnv("AUDIT_RETENTION_DAYS", 30),
		AuditFsync:         boolEnv("AUDIT_FSYNC", true),
		AuditOutputChars:   intEnv("AUDIT_OUTPUT_CHARS", 4_000),
	}
	if cfg.APIToken == "" {
		return Config{}, errors.New("API_TOKEN is required")
	}
	if cfg.CommandTimeout <= 0 || cfg.CommandTimeout > defaultTimeout {
		cfg.CommandTimeout = defaultTimeout
	}
	if cfg.InlineOutputChars < 1 || cfg.InlineOutputChars > 60_000 {
		return Config{}, errors.New("INLINE_OUTPUT_CHARS must be between 1 and 60000")
	}
	if cfg.MaxArtifactBytes < 1 || cfg.MaxArtifactBytes > defaultArtifactSize {
		return Config{}, fmt.Errorf("MAX_ARTIFACT_BYTES must be between 1 and %d", defaultArtifactSize)
	}
	if cfg.MaxInputFileBytes < 1 || cfg.MaxInputFileBytes > defaultArtifactSize {
		return Config{}, fmt.Errorf("MAX_INPUT_FILE_BYTES must be between 1 and %d", defaultArtifactSize)
	}
	if cfg.AuditRetentionDays < 1 || cfg.AuditRetentionDays > 3650 {
		return Config{}, errors.New("AUDIT_RETENTION_DAYS must be between 1 and 3650")
	}
	if cfg.AuditOutputChars < 0 || cfg.AuditOutputChars > 20_000 {
		return Config{}, errors.New("AUDIT_OUTPUT_CHARS must be between 0 and 20000")
	}
	if cfg.ActionBaseURL != "" {
		u, err := url.Parse(cfg.ActionBaseURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" {
			return Config{}, errors.New("ACTION_BASE_URL must be an HTTPS origin without a path")
		}
	}
	root, err := filepath.Abs(cfg.WorkspaceRoot)
	if err != nil {
		return Config{}, fmt.Errorf("resolve WORKSPACE_ROOT: %w", err)
	}
	cfg.WorkspaceRoot = root
	return cfg, nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func secondsEnv(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return time.Duration(n) * time.Second
}

func intEnv(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil {
		return fallback
	}
	return value
}

func int64Env(name string, fallback int64) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(name)), 10, 64)
	if err != nil {
		return fallback
	}
	return value
}

func boolEnv(name string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func splitList(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.ToLower(strings.TrimSpace(item)); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func splitSet(value string) map[string]struct{} {
	items := splitList(value)
	out := make(map[string]struct{}, len(items))
	for _, item := range items {
		out[item] = struct{}{}
	}
	return out
}
