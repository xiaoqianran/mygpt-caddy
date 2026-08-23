package audit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func Read(dir string) ([]Event, error) {
	files, err := Files(dir)
	if err != nil {
		return nil, err
	}
	var events []Event
	for _, path := range files {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
		line := 0
		for scanner.Scan() {
			line++
			var event Event
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				_ = file.Close()
				return nil, fmt.Errorf("%s:%d: %w", path, line, err)
			}
			events = append(events, event)
		}
		scanErr := scanner.Err()
		closeErr := file.Close()
		if scanErr != nil {
			return nil, fmt.Errorf("scan %s: %w", path, scanErr)
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	return events, nil
}

type VerifyResult struct {
	Valid      bool   `json:"valid"`
	Events     int    `json:"events"`
	Chains     int    `json:"chains"`
	FirstError string `json:"first_error,omitempty"`
}

func Verify(events []Event) VerifyResult {
	result := VerifyResult{Valid: true, Events: len(events)}
	lastByChain := make(map[string]string)
	seenChain := make(map[string]bool)
	lastSequence := make(map[string]uint64)
	seenTrace := make(map[string]bool)
	for index, event := range events {
		if event.Schema != SchemaVersion || event.ChainID == "" || event.TraceID == "" || event.Hash == "" {
			result.Valid = false
			result.FirstError = fmt.Sprintf("event %d has missing identity fields", index+1)
			return result
		}
		if !seenChain[event.ChainID] {
			seenChain[event.ChainID] = true
			result.Chains++
		} else if event.PreviousHash != lastByChain[event.ChainID] {
			result.Valid = false
			result.FirstError = fmt.Sprintf("event %d breaks chain %s", index+1, event.ChainID)
			return result
		}
		traceKey := event.ChainID + "\x00" + event.TraceID
		if seenTrace[traceKey] && event.Sequence != lastSequence[traceKey]+1 {
			result.Valid = false
			result.FirstError = fmt.Sprintf("event %d breaks trace sequence %s", index+1, event.TraceID)
			return result
		}
		expected, err := ExpectedHash(event)
		if err != nil || expected != event.Hash {
			result.Valid = false
			result.FirstError = fmt.Sprintf("event %d has invalid hash", index+1)
			return result
		}
		lastByChain[event.ChainID] = event.Hash
		seenTrace[traceKey] = true
		lastSequence[traceKey] = event.Sequence
	}
	return result
}
