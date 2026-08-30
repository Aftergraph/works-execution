// Package kanban provides a tiny CLI for inspecting the version-controlled
// kanban board at docs/kanban/board.json.
//
// This is intentionally not a real kanban tool — it's a read-mostly summary
// generator so a human can `make kanban` and see status at a glance, and a
// machine can query state via JSON. The actual dispatching of work to
// subagents uses Hermes's own Kanban dispatcher (out of scope here).
package kanban

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Card is one row on the board.
type Card struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Lane           string   `json:"lane"`
	Column         string   `json:"column"` // backlog|ready|in_progress|review|done
	StandardIDs    []string `json:"standard_ids,omitempty"`
	Acceptance     string   `json:"acceptance"`
	Assignee       string   `json:"assignee,omitempty"`
	Evidence       []string `json:"evidence,omitempty"`
	BlockedReason  string   `json:"blocked_reason,omitempty"`
	UnblockCheck   string   `json:"unblock_check,omitempty"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
}

// Board is the schema for docs/kanban/board.json.
type Board struct {
	SchemaVersion string            `json:"schema_version"`
	BoardName     string            `json:"board_name"`
	Columns       []string          `json:"columns"`
	Lanes         map[string]Lane   `json:"lanes"`
	Cards         []Card            `json:"cards"`
}

// Lane describes the lane.
type Lane struct {
	Description string `json:"description"`
}

// Load reads a board from disk.
func Load(path string) (*Board, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("kanban: read %s: %w", path, err)
	}
	var b Board
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("kanban: parse %s: %w", path, err)
	}
	return &b, nil
}

// Summary returns a human-readable summary of the board.
func (b *Board) Summary() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n", b.BoardName)
	byColumn := map[string][]Card{}
	for _, c := range b.Cards {
		byColumn[c.Column] = append(byColumn[c.Column], c)
	}
	for _, col := range b.Columns {
		cards := byColumn[col]
		fmt.Fprintf(&sb, "## %s (%d)\n", strings.ToUpper(col), len(cards))
		if len(cards) == 0 {
			sb.WriteString("  (empty)\n")
			continue
		}
		// Sort by lane then by ID within each column.
		sort.Slice(cards, func(i, j int) bool {
			if cards[i].Lane != cards[j].Lane {
				return cards[i].Lane < cards[j].Lane
			}
			return cards[i].ID < cards[j].ID
		})
		for _, c := range cards {
			block := ""
			if c.BlockedReason != "" {
				block = " [BLOCKED]"
			}
			fmt.Fprintf(&sb, "  - %s [%s] %s%s\n", c.ID, c.Lane, c.Title, block)
			if len(c.StandardIDs) > 0 {
				fmt.Fprintf(&sb, "      standards: %s\n", strings.Join(c.StandardIDs, ", "))
			}
		}
		sb.WriteString("\n")
	}
	// Lane summaries.
	sb.WriteString("## Lanes\n")
	for name, lane := range b.Lanes {
		fmt.Fprintf(&sb, "  - %s: %s\n", name, lane.Description)
	}
	return sb.String()
}

// LaneSummary returns cards in a specific lane.
func (b *Board) LaneSummary(lane string) []Card {
	var out []Card
	for _, c := range b.Cards {
		if c.Lane == lane {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// CardByID returns a single card by ID, or nil if not found.
func (b *Board) CardByID(id string) *Card {
	for i := range b.Cards {
		if b.Cards[i].ID == id {
			return &b.Cards[i]
		}
	}
	return nil
}

// MoveCard changes a card's column. Returns an error if the card is unknown.
func (b *Board) MoveCard(id, column string) error {
	for i := range b.Cards {
		if b.Cards[i].ID == id {
			b.Cards[i].Column = column
			b.Cards[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			return nil
		}
	}
	return fmt.Errorf("kanban: unknown card %q", id)
}

// Save writes the board back to disk.
func (b *Board) Save(path string) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644)
}

// Validate checks the board against its own schema invariants. Used by
// `make kanban-validate`.
func (b *Board) Validate() error {
	allowedColumns := map[string]bool{}
	for _, c := range b.Columns {
		allowedColumns[c] = true
	}
	knownLanes := map[string]bool{}
	for k := range b.Lanes {
		knownLanes[k] = true
	}
	seen := map[string]bool{}
	for i, c := range b.Cards {
		if c.ID == "" {
			return fmt.Errorf("card[%d]: missing id", i)
		}
		if seen[c.ID] {
			return fmt.Errorf("card[%d]: duplicate id %q", i, c.ID)
		}
		seen[c.ID] = true
		if c.Title == "" {
			return fmt.Errorf("card %q: missing title", c.ID)
		}
		if !allowedColumns[c.Column] {
			return fmt.Errorf("card %q: column %q not in %v", c.ID, c.Column, b.Columns)
		}
		if !knownLanes[c.Lane] {
			return fmt.Errorf("card %q: lane %q not declared in lanes", c.ID, c.Lane)
		}
		if c.BlockedReason != "" && c.Column == "done" {
			return fmt.Errorf("card %q: cannot be both BLOCKED and done", c.ID)
		}
	}
	return nil
}