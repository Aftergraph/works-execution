// Package standards validates JSON documents against the works-execution
// standards schemas (JSON Schema 2020-12). It is intentionally minimal:
// load a schema once, validate many documents.
//
// Schemas live under docs/standards/schemas/ and are embedded into the binary
// at build time so the validator works without filesystem access.
package standards

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

//go:embed schemas/*.json
var embedded embed.FS

var (
	loadOnce sync.Once
	schemas  map[string]*jsonschema.Schema
	loadErr  error
)

// Load compiles every embedded schema and returns them keyed by $id.
func Load() (map[string]*jsonschema.Schema, error) {
	loadOnce.Do(func() {
		compiler := jsonschema.NewCompiler()
		entries, err := embedded.ReadDir("schemas")
		if err != nil {
			loadErr = fmt.Errorf("read embedded schemas: %w", err)
			return
		}
		// Stable order helps debugging.
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".schema.json") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)

		out := map[string]*jsonschema.Schema{}
		for _, n := range names {
			data, err := embedded.ReadFile("schemas/" + n)
			if err != nil {
				loadErr = fmt.Errorf("read %s: %w", n, err)
				return
			}
			// Add as a named resource; the compiler picks up the $id.
			if err := compiler.AddResource("https://works-execution.dev/schemas/"+n, strings.NewReader(string(data))); err != nil {
				loadErr = fmt.Errorf("compile %s: %w", n, err)
				return
			}
		}
		for _, n := range names {
			id := "https://works-execution.dev/schemas/" + n
			sch, err := compiler.Compile(id)
			if err != nil {
				loadErr = fmt.Errorf("final compile %s: %w", n, err)
				return
			}
			out[id] = sch
		}
		schemas = out
	})
	return schemas, loadErr
}

// Validate checks `document` against the named schema. `name` is the
// basename (e.g. "action-manifest.schema.json") or the full $id.
func Validate(name string, document any) error {
	all, err := Load()
	if err != nil {
		return fmt.Errorf("standards: load: %w", err)
	}
	var sch *jsonschema.Schema
	if s, ok := all["https://works-execution.dev/schemas/"+name]; ok {
		sch = s
	} else if s, ok := all[name]; ok {
		sch = s
	} else {
		return fmt.Errorf("standards: unknown schema %q", name)
	}
	if err := sch.Validate(document); err != nil {
		return err
	}
	return nil
}

// ValidateBytes is a convenience wrapper for []byte inputs.
func ValidateBytes(name string, data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("standards: parse: %w", err)
	}
	return Validate(name, v)
}

// ErrUnknownSchema is returned when an invalid schema name is requested.
var ErrUnknownSchema = errors.New("standards: unknown schema")

// ListSchemas returns the basenames of every embedded schema, sorted.
func ListSchemas() []string {
	entries, err := embedded.ReadDir("schemas")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".schema.json") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}