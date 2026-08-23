package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

type commandRequest struct {
	Command          string            `json:"command"`
	Workdir          string            `json:"workdir,omitempty"`
	Stdin            string            `json:"stdin,omitempty"`
	TimeoutSeconds   int               `json:"timeout_seconds,omitempty"`
	OpenAIFileIDRefs []json.RawMessage `json:"openaiFileIdRefs,omitempty"`
}

type commandResponse struct {
	ExitCode           int         `json:"exit_code"`
	Stdout             string      `json:"stdout"`
	Stderr             string      `json:"stderr"`
	TimedOut           bool        `json:"timed_out"`
	OutputTruncated    bool        `json:"output_truncated"`
	DurationMS         int64       `json:"duration_ms"`
	Workdir            string      `json:"workdir"`
	InputFiles         []savedFile `json:"input_files,omitempty"`
	OpenAIFileResponse []string    `json:"openaiFileResponse,omitempty"`
}

func (s *Server) execute(ctx context.Context, req commandRequest, startDir, inputDir string, files []savedFile, meta requestMeta) (commandResponse, error) {
	workdir, err := resolveWorkdir(startDir, req.Workdir)
	if err != nil {
		return commandResponse{}, err
	}
	stdoutCapture, stdoutArtifact, err := s.artifacts.newCapture("stdout")
	if err != nil {
		return commandResponse{}, err
	}
	stderrCapture, stderrArtifact, err := s.artifacts.newCapture("stderr")
	if err != nil {
		s.artifacts.discard(stdoutArtifact)
		return commandResponse{}, err
	}
	defer func() {
		_ = closeCapture(stdoutCapture)
		_ = closeCapture(stderrCapture)
	}()

	cwdState := filepath.Join(s.cfg.StateDir, "cwd-"+meta.requestID)
	defer os.Remove(cwdState)
	script := "trap 'pwd -P > \"$MYGPT_CWD_STATE\"' EXIT\n" + req.Command
	cmd := exec.Command("/bin/bash", "--noprofile", "--norc", "-c", script)
	cmd.Dir = workdir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout, cmd.Stderr = stdoutCapture, stderrCapture
	cmd.Stdin = strings.NewReader(req.Stdin)
	pathsJSON, _ := json.Marshal(files)
	cmd.Env = append(os.Environ(),
		"DEBIAN_FRONTEND=noninteractive", "PAGER=cat", "GIT_PAGER=cat", "SYSTEMD_PAGER=cat",
		"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "CI=1",
		"MYGPT_CWD_STATE="+cwdState,
		"OPENAI_FILE_DIR="+inputDir,
		"OPENAI_FILE_PATHS_JSON="+string(pathsJSON),
		"OPENAI_CONVERSATION_ID="+meta.conversationID,
		"OPENAI_EPHEMERAL_USER_ID="+meta.userID,
		"OPENAI_GPT_ID="+meta.gptID,
	)

	started := time.Now()
	if err := cmd.Start(); err != nil {
		s.artifacts.discard(stdoutArtifact)
		s.artifacts.discard(stderrArtifact)
		return commandResponse{}, fmt.Errorf("start command: %w", err)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	var waitErr error
	timedOut := false
	select {
	case waitErr = <-waitCh:
	case <-ctx.Done():
		timedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		waitErr = <-waitCh
	}
	duration := time.Since(started)
	_ = closeCapture(stdoutCapture)
	_ = closeCapture(stderrCapture)

	exitCode := 0
	if waitErr != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	finalDir := workdir
	if data, err := os.ReadFile(cwdState); err == nil {
		if candidate := strings.TrimSpace(string(data)); candidate != "" {
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				finalDir = candidate
			}
		}
	}

	resp := commandResponse{
		ExitCode: exitCode, TimedOut: timedOut, DurationMS: duration.Milliseconds(),
		Workdir: finalDir, InputFiles: files,
		OutputTruncated: stdoutCapture.truncated || stderrCapture.truncated,
	}
	totalChars, err := capturedChars(stdoutArtifact.path, stderrArtifact.path)
	if err != nil {
		return commandResponse{}, err
	}
	if totalChars <= s.cfg.InlineOutputChars && !resp.OutputTruncated {
		resp.Stdout, err = readText(stdoutArtifact.path)
		if err == nil {
			resp.Stderr, err = readText(stderrArtifact.path)
		}
		s.artifacts.discard(stdoutArtifact)
		s.artifacts.discard(stderrArtifact)
		return resp, err
	}

	baseURL := meta.baseURL
	if stdoutCapture.written > 0 {
		resp.Stdout = tailFile(stdoutArtifact.path, 4_000)
		published := s.artifacts.publish(stdoutArtifact, stdoutCapture.written)
		resp.OpenAIFileResponse = append(resp.OpenAIFileResponse, s.artifacts.URL(baseURL, published))
	} else {
		s.artifacts.discard(stdoutArtifact)
	}
	if stderrCapture.written > 0 {
		resp.Stderr = tailFile(stderrArtifact.path, 4_000)
		published := s.artifacts.publish(stderrArtifact, stderrCapture.written)
		resp.OpenAIFileResponse = append(resp.OpenAIFileResponse, s.artifacts.URL(baseURL, published))
	} else {
		s.artifacts.discard(stderrArtifact)
	}
	return resp, nil
}

func resolveWorkdir(current, requested string) (string, error) {
	dir := current
	if requested != "" {
		if filepath.IsAbs(requested) {
			dir = requested
		} else {
			dir = filepath.Join(current, requested)
		}
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", errors.New("invalid workdir")
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", errors.New("workdir does not exist or is not a directory")
	}
	return dir, nil
}

func capturedChars(paths ...string) (int, error) {
	total := 0
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return 0, err
		}
		total += utf8.RuneCount(data)
	}
	return total, nil
}

func readText(path string) (string, error) {
	data, err := os.ReadFile(path)
	return string(data), err
}

func tailFile(path string, maxRunes int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	runes := []rune(string(data))
	if len(runes) > maxRunes {
		runes = runes[len(runes)-maxRunes:]
	}
	return string(runes)
}

func requestTimeout(requested int, maximum time.Duration) time.Duration {
	if requested <= 0 {
		return maximum
	}
	d := time.Duration(requested) * time.Second
	if d > maximum {
		return maximum
	}
	return d
}

var _ io.Writer = (*cappedFile)(nil)
