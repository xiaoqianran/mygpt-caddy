package audit

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestConcurrentChainedAuditAndVerification(t *testing.T) {
	dir := t.TempDir()
	sink, err := NewFileSink(dir, 30, true)
	if err != nil {
		t.Fatal(err)
	}
	recorder := NewRecorder(sink)
	trace := recorder.NewTrace("trace-test")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			if err := trace.Event("test.event", "ok", map[string]any{"index": index}); err != nil {
				t.Errorf("append: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if result := Verify(events); !result.Valid || result.Events != 20 || result.Chains != 1 {
		t.Fatalf("unexpected verification: %+v", result)
	}
}

func TestTamperingIsDetected(t *testing.T) {
	dir := t.TempDir()
	sink, _ := NewFileSink(dir, 30, false)
	trace := NewRecorder(sink).NewTrace("trace-test")
	_ = trace.Event("command.started", "ok", map[string]any{"command": "true"})
	_ = sink.Close()
	files, _ := Files(dir)
	data, _ := os.ReadFile(files[0])
	data = []byte(strings.Replace(string(data), `"true"`, `"false"`, 1))
	if err := os.WriteFile(filepath.Clean(files[0]), data, 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if result := Verify(events); result.Valid {
		t.Fatal("tampered audit log verified successfully")
	}
}
