package audit

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const SchemaVersion = "mygpt.audit.v1"

type Event struct {
	Schema       string         `json:"schema"`
	Timestamp    string         `json:"timestamp"`
	ChainID      string         `json:"chain_id"`
	TraceID      string         `json:"trace_id"`
	Sequence     uint64         `json:"sequence"`
	Stage        string         `json:"stage"`
	Outcome      string         `json:"outcome"`
	Data         map[string]any `json:"data,omitempty"`
	PreviousHash string         `json:"previous_hash,omitempty"`
	Hash         string         `json:"hash,omitempty"`
}

type Sink interface {
	Append(Event) error
	Close() error
}

type Recorder struct {
	sink Sink
}

func NewRecorder(sink Sink) *Recorder { return &Recorder{sink: sink} }

func (r *Recorder) NewTrace(id string) *Trace {
	return &Trace{id: id, recorder: r}
}

func (r *Recorder) Close() error {
	if r == nil || r.sink == nil {
		return nil
	}
	return r.sink.Close()
}

type Trace struct {
	mu       sync.Mutex
	id       string
	sequence uint64
	recorder *Recorder
}

func (t *Trace) ID() string {
	if t == nil {
		return ""
	}
	return t.id
}

func (t *Trace) Event(stage, outcome string, data map[string]any) error {
	if t == nil || t.recorder == nil || t.recorder.sink == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sequence++
	event := Event{
		Schema: SchemaVersion, Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		TraceID: t.id, Sequence: t.sequence, Stage: stage, Outcome: outcome, Data: data,
	}
	return t.recorder.sink.Append(event)
}

type NopSink struct{}

func (NopSink) Append(Event) error { return nil }
func (NopSink) Close() error       { return nil }

type FileSink struct {
	mu            sync.Mutex
	dir           string
	chainID       string
	previousHash  string
	currentDate   string
	file          *os.File
	retentionDays int
	fsync         bool
}

func NewFileSink(dir string, retentionDays int, fsync bool) (*FileSink, error) {
	if retentionDays < 1 {
		return nil, errors.New("audit retention must be at least one day")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create audit directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("protect audit directory: %w", err)
	}
	chainID, err := randomHex(16)
	if err != nil {
		return nil, fmt.Errorf("create audit chain ID: %w", err)
	}
	return &FileSink{dir: dir, chainID: chainID, retentionDays: retentionDays, fsync: fsync}, nil
}

func (s *FileSink) Append(event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := time.Parse(time.RFC3339Nano, event.Timestamp); err != nil {
		return fmt.Errorf("invalid audit timestamp: %w", err)
	}
	date := time.Now().UTC().Format("2006-01-02")
	if err := s.rotate(date); err != nil {
		return err
	}
	event.ChainID = s.chainID
	event.PreviousHash = s.previousHash
	event.Hash = ""
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode audit event: %w", err)
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(event.PreviousHash))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(payload)
	event.Hash = hex.EncodeToString(digest.Sum(nil))
	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode chained audit event: %w", err)
	}
	line = append(line, '\n')
	if _, err := s.file.Write(line); err != nil {
		return fmt.Errorf("append audit event: %w", err)
	}
	if s.fsync {
		if err := s.file.Sync(); err != nil {
			return fmt.Errorf("sync audit event: %w", err)
		}
	}
	s.previousHash = event.Hash
	return nil
}

func (s *FileSink) rotate(date string) error {
	if s.currentDate == date && s.file != nil {
		return nil
	}
	if s.file != nil {
		if err := s.file.Close(); err != nil {
			return err
		}
	}
	path := filepath.Join(s.dir, "audit-"+date+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("protect audit log: %w", err)
	}
	s.file = file
	s.currentDate = date
	s.cleanup()
	return nil
}

func (s *FileSink) cleanup() {
	entries, err := filepath.Glob(filepath.Join(s.dir, "audit-*.jsonl"))
	if err != nil {
		return
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -s.retentionDays)
	for _, path := range entries {
		base := filepath.Base(path)
		dateText := strings.TrimSuffix(strings.TrimPrefix(base, "audit-"), ".jsonl")
		date, err := time.Parse("2006-01-02", dateText)
		if err == nil && date.Before(cutoff) && dateText != s.currentDate {
			_ = os.Remove(path)
		}
	}
}

func (s *FileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

func Files(dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "audit-*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func canonicalPayload(event Event) ([]byte, error) {
	event.Hash = ""
	return json.Marshal(event)
}

func ExpectedHash(event Event) (string, error) {
	payload, err := canonicalPayload(event)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(event.PreviousHash))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(payload)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func randomHex(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
