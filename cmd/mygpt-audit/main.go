package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/xiaoqianran/mygpt-caddy/internal/audit"
)

func main() {
	dir := flag.String("dir", "/var/lib/mygpt-caddy/audit", "audit JSONL directory")
	limit := flag.Int("limit", 50, "maximum events for recent")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		usage()
	}
	events, err := audit.Read(*dir)
	if err != nil {
		fail(err)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	switch args[0] {
	case "recent":
		start := len(events) - *limit
		if start < 0 {
			start = 0
		}
		for _, event := range events[start:] {
			if err := encoder.Encode(event); err != nil {
				fail(err)
			}
		}
	case "trace":
		if len(args) != 2 {
			usage()
		}
		found := false
		for _, event := range events {
			if event.TraceID == args[1] {
				found = true
				if err := encoder.Encode(event); err != nil {
					fail(err)
				}
			}
		}
		if !found {
			fail(fmt.Errorf("trace %s not found", args[1]))
		}
	case "verify":
		result := audit.Verify(events)
		if err := encoder.Encode(result); err != nil {
			fail(err)
		}
		if !result.Valid {
			os.Exit(1)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mygpt-audit [-dir PATH] [-limit N] recent | trace TRACE_ID | verify")
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "mygpt-audit:", err)
	os.Exit(1)
}
