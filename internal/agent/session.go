package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type sessionStore struct {
	mu       sync.Mutex
	path     string
	fallback string
	dirs     map[string]string
	locks    map[string]*sync.Mutex
}

func newSessionStore(stateDir, fallback string) (*sessionStore, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	s := &sessionStore{
		path: filepath.Join(stateDir, "sessions.json"), fallback: fallback,
		dirs: make(map[string]string), locks: make(map[string]*sync.Mutex),
	}
	data, err := os.ReadFile(s.path)
	if err == nil {
		if err := json.Unmarshal(data, &s.dirs); err != nil {
			return nil, fmt.Errorf("read session state: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read session state: %w", err)
	}
	return s, nil
}

func sessionKey(conversationID, userID string) string {
	if conversationID == "" && userID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(userID + "\x00" + conversationID))
	return hex.EncodeToString(sum[:16])
}

func (s *sessionStore) lock(key string) func() {
	if key == "" {
		return func() {}
	}
	s.mu.Lock()
	lock := s.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		s.locks[key] = lock
	}
	s.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func (s *sessionStore) get(key string) string {
	if key == "" {
		return s.fallback
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if dir := s.dirs[key]; dir != "" {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return s.fallback
}

func (s *sessionStore) set(key, dir string) error {
	if key == "" || dir == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dirs[key] = dir
	data, err := json.Marshal(s.dirs)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	return os.Chmod(s.path, 0o600)
}
