package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type Registry interface {
	Put(context.Context, Workspace) error
	Get(context.Context, string) (Workspace, error)
	List(context.Context) ([]Workspace, error)
	Delete(context.Context, string) error
}

type FileRegistry struct {
	mu   sync.Mutex
	path string
}

type registryDocument struct {
	Workspaces map[string]Workspace `json:"workspaces"`
}

func NewFileRegistry(path string) (*FileRegistry, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: workspace registry path is required", ErrMalformed)
	}
	return &FileRegistry{path: path}, nil
}

func (r *FileRegistry) load() (registryDocument, error) {
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return registryDocument{Workspaces: map[string]Workspace{}}, nil
	}
	if err != nil {
		return registryDocument{}, err
	}
	var doc registryDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return registryDocument{}, fmt.Errorf("workspace registry: corrupt registry file: %w", err)
	}
	if doc.Workspaces == nil {
		return registryDocument{}, fmt.Errorf("workspace registry: corrupt registry file: missing workspaces")
	}
	for id, ws := range doc.Workspaces {
		if id == "" || id != ws.ID {
			return registryDocument{}, fmt.Errorf("workspace registry: corrupt registry file: workspace id mismatch")
		}
		if err := ws.Validate(); err != nil {
			return registryDocument{}, fmt.Errorf("workspace registry: corrupt workspace %s: %w", id, err)
		}
	}
	return doc, nil
}

func (r *FileRegistry) save(doc registryDocument) error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(r.path), filepath.Base(r.path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, r.path)
}

func (r *FileRegistry) Put(ctx context.Context, ws Workspace) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ws.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	doc, err := r.load()
	if err != nil {
		return err
	}
	doc.Workspaces[ws.ID] = ws
	return r.save(doc)
}

func (r *FileRegistry) Get(ctx context.Context, id string) (Workspace, error) {
	if err := ctx.Err(); err != nil {
		return Workspace{}, err
	}
	if id == "" {
		return Workspace{}, ErrNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	doc, err := r.load()
	if err != nil {
		return Workspace{}, err
	}
	ws, ok := doc.Workspaces[id]
	if !ok {
		return Workspace{}, ErrNotFound
	}
	return ws, nil
}

func (r *FileRegistry) List(ctx context.Context) ([]Workspace, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	doc, err := r.load()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(doc.Workspaces))
	for id := range doc.Workspaces {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Workspace, 0, len(ids))
	for _, id := range ids {
		out = append(out, doc.Workspaces[id])
	}
	return out, nil
}

func (r *FileRegistry) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	doc, err := r.load()
	if err != nil {
		return err
	}
	if _, ok := doc.Workspaces[id]; !ok {
		return ErrNotFound
	}
	delete(doc.Workspaces, id)
	return r.save(doc)
}

var _ Registry = (*FileRegistry)(nil)
