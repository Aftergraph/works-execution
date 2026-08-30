// Command works-standards inspects the standards registry.
//
// Usage:
//   works-standards list                 # all rows, compact
//   works-standards show <standard_id>   # full row + traceability
//   works-standards validate <file>      # validate file against a schema
//   works-standards gaps                 # PLANNED rows with file-path next steps
//   works-standards summary              # status counts per domain
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/JonasAbde/works-execution/internal/standards"
)

const registryPath = "docs/standards/registry.json"

type registryDoc struct {
	Standards []struct {
		StandardID  string `json:"standard_id"`
		StandardName string `json:"standard_name"`
		Version     string `json:"version"`
		Domain      string `json:"domain"`
		Requirement string `json:"requirement"`
		Status      string `json:"status"`
		Owner       string `json:"owner"`
		Implementation string `json:"implementation"`
		EnforcementPoint string `json:"enforcement_point"`
		Test        string `json:"test"`
		Evidence    string `json:"evidence"`
		Exceptions  []string `json:"exceptions,omitempty"`
		BlockedReason string `json:"blocked_reason,omitempty"`
		UnblockCheck string `json:"unblock_check,omitempty"`
	} `json:"standards"`
	Summary struct {
		Total     int `json:"total"`
		ByStatus  map[string]int `json:"by_status"`
		ByDomain  map[string]int `json:"by_domain"`
	} `json:"summary"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "list":
		data, err := os.ReadFile(registryPath)
		if err != nil { fail(err) }
		var d registryDoc
		if err := json.Unmarshal(data, &d); err != nil { fail(err) }
		fmt.Printf("%-50s %-12s %-12s %s\n", "STANDARD_ID", "DOMAIN", "STATUS", "NAME")
		for _, s := range d.Standards {
			fmt.Printf("%-50s %-12s %-12s %s\n", s.StandardID, s.Domain, s.Status, s.StandardName)
		}
	case "show":
		if len(os.Args) < 3 { failStr("usage: works-standards show <id>") }
		data, err := os.ReadFile(registryPath)
		if err != nil { fail(err) }
		var d registryDoc
		if err := json.Unmarshal(data, &d); err != nil { fail(err) }
		for _, s := range d.Standards {
			if s.StandardID == os.Args[2] {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(s)
				return
			}
		}
		failStr("not found: " + os.Args[2])
	case "validate":
		if len(os.Args) < 3 { failStr("usage: works-standards validate <schema> <file>") }
		schemaName := os.Args[2]
		file := os.Args[3]
		data, err := os.ReadFile(file)
		if err != nil { fail(err) }
		if err := standards.ValidateBytes(schemaName, data); err != nil {
			fail(err)
		}
		fmt.Printf("valid: %s matches %s\n", file, schemaName)
	case "gaps":
		// Show all PLANNED rows with a one-line summary.
		data, err := os.ReadFile(registryPath)
		if err != nil { fail(err) }
		var d registryDoc
		if err := json.Unmarshal(data, &d); err != nil { fail(err) }
		fmt.Printf("%-50s %-12s %s\n", "STANDARD_ID", "DOMAIN", "NEXT STEP / IMPLEMENTATION")
		for _, s := range d.Standards {
			if s.Status != "PLANNED" && s.Status != "PARTIAL" {
				continue
			}
			step := s.Implementation
			if step == "" { step = s.EnforcementPoint }
			fmt.Printf("%-50s %-12s %s\n", s.StandardID, s.Domain, step)
		}
	case "summary":
		data, err := os.ReadFile(registryPath)
		if err != nil { fail(err) }
		var d registryDoc
		if err := json.Unmarshal(data, &d); err != nil { fail(err) }
		fmt.Printf("total: %d standards\n\n", d.Summary.Total)
		fmt.Println("by status:")
		keys := make([]string, 0, len(d.Summary.ByStatus))
		for k := range d.Summary.ByStatus { keys = append(keys, k) }
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("  %-15s %d\n", k, d.Summary.ByStatus[k])
		}
		fmt.Println("\nby domain:")
		keys = keys[:0]
		for k := range d.Summary.ByDomain { keys = append(keys, k) }
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("  %-15s %d\n", k, d.Summary.ByDomain[k])
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `works-standards — inspect docs/standards/registry.json

Usage:
  works-standards list
  works-standards show <standard_id>
  works-standards validate <schema> <file>
  works-standards gaps
  works-standards summary
`)
}

func failStr(s string) { fmt.Fprintln(os.Stderr, "works-standards:", s); os.Exit(1) }
func fail(err error) { fmt.Fprintln(os.Stderr, "works-standards:", err); os.Exit(1) }
// strings used for grep-friendly imports
