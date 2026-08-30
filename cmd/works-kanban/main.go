// Command works-kanban inspects or mutates docs/kanban/board.json.
//
// Usage:
//   works-kanban summary                 # human-readable summary
//   works-kanban lane <lane>             # cards in a lane
//   works-kanban card <id>               # single card details
//   works-kanban move <id> <column>      # move a card and save
//   works-kanban validate                # validate invariants
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/JonasAbde/works-execution/internal/kanban"
)

const defaultBoardPath = "docs/kanban/board.json"

func resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	// Try CWD first (developer / Make target), then walk up looking for
	// a directory containing docs/kanban/board.json.
	if _, err := os.Stat(path); err == nil {
		return path
	}
	for wd := "."; ; {
		candidate := filepath.Join(wd, path)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			break
		}
		wd = parent
	}
	// Fall back to original (will surface the file-not-found error).
	return path
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	path := envOr("WORKS_KANBAN_PATH", defaultBoardPath)
	path = resolvePath(path)

	switch os.Args[1] {
	case "summary":
		b, err := kanban.Load(path)
		if err != nil {
			fail(err)
		}
		fmt.Print(b.Summary())
	case "lane":
		if len(os.Args) < 3 {
			fail(fmt.Errorf("usage: works-kanban lane <lane>"))
		}
		b, err := kanban.Load(path)
		if err != nil {
			fail(err)
		}
		cards := b.LaneSummary(os.Args[2])
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		for _, c := range cards {
			_ = enc.Encode(c)
		}
	case "card":
		if len(os.Args) < 3 {
			fail(fmt.Errorf("usage: works-kanban card <id>"))
		}
		b, err := kanban.Load(path)
		if err != nil {
			fail(err)
		}
		c := b.CardByID(os.Args[2])
		if c == nil {
			fail(fmt.Errorf("card %q not found", os.Args[2]))
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(c)
	case "move":
		if len(os.Args) < 4 {
			fail(fmt.Errorf("usage: works-kanban move <id> <column>"))
		}
		b, err := kanban.Load(path)
		if err != nil {
			fail(err)
		}
		if err := b.MoveCard(os.Args[2], os.Args[3]); err != nil {
			fail(err)
		}
		if err := b.Save(path); err != nil {
			fail(err)
		}
		fmt.Printf("moved %s to %s\n", os.Args[2], os.Args[3])
	case "validate":
		b, err := kanban.Load(path)
		if err != nil {
			fail(err)
		}
		if err := b.Validate(); err != nil {
			fail(err)
		}
		fmt.Println("kanban: board valid")
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `works-kanban — inspect or mutate docs/kanban/board.json

Usage:
  works-kanban summary
  works-kanban lane <lane>
  works-kanban card <id>
  works-kanban move <id> <column>
  works-kanban validate

Environment:
  WORKS_KANBAN_PATH  path to board.json (default docs/kanban/board.json)
`)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "works-kanban:", err)
	os.Exit(1)
}