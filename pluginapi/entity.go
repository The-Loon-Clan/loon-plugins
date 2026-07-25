package pluginapi

import (
	"context"
	"fmt"
	"sync"
)

// EntityEditor is the contract for "a user may suggest a field-level correction
// to this entity." Whoever owns an entity — a plugin, or the host for
// anime/manga/nzb — registers one editor for it, and the wiki/review machinery
// treats every entity the same.
//
// It replaces a switch in pkg/storage/postgres/wiki.go that mapped entity_type
// to (table, id column, field whitelist) for exactly three hardcoded entities
// and then built an UPDATE with fmt.Sprintf. Adding a fourth editable entity
// meant editing pkg/storage, which a plugin cannot do; the switch was also the
// SQL-injection boundary, which is why the call carried an sqllint:allow.
//
// Under this contract the owner supplies Fields() — the whitelist — and Apply,
// which writes its own columns through its own repository. The dynamic SQL is
// gone: an editor's Apply names its table and columns as compile-time literals,
// so there is nothing to whitelist at the SQL layer. See ENTITY-EDITS.md.
type EntityEditor interface {
	// Kind is the stable entity_type stored on wiki_edits ("anime", "manga",
	// "release_group"). Unique per registry.
	Kind() string

	// Label is the human name for the review queue ("Anime").
	Label() string

	// Fields is the whitelist: the only fields a user may suggest a change to,
	// and the only ones Apply will act on. It drives the edit form and the
	// review diff. Nothing outside this set is editable, by construction.
	Fields() []EditableField

	// Apply writes the approved changes — field name to new value — to the
	// entity with the given id. The host has already validated every key
	// against Fields() before this is called, so an editor may trust its keys;
	// it still owns HOW each field is written (types, nullability, side
	// effects). Called once, when an edit is approved.
	Apply(ctx context.Context, id int64, changes map[string]string) error
}

// FieldKind tells the edit form which control to render and hints at parsing.
// The value stored in wiki_edit_changes is always a string regardless.
type FieldKind string

const (
	FieldText     FieldKind = "text"
	FieldTextarea FieldKind = "textarea"
	FieldInt      FieldKind = "int"
	FieldDate     FieldKind = "date"
	FieldURL      FieldKind = "url"
)

// EditableField is one entry in an editor's whitelist.
type EditableField struct {
	Name  string    // wiki_edit_changes.field_name; the column the editor writes
	Label string    // form + diff label
	Kind  FieldKind // form control
}

// EntityEditorRegistryName is the extension-registry key under which the host
// publishes the registry, for plugins that register their own editors after the
// host's are in place. (Host editors are registered directly at boot; the
// published handle is for plugin editors — a later step.)
const EntityEditorRegistryName = "entity.editors"

// EntityEditorRegistry holds the editors, keyed by Kind. Safe for concurrent
// reads; registration happens at boot before serving.
type EntityEditorRegistry struct {
	mu      sync.RWMutex
	editors map[string]EntityEditor
}

// NewEntityEditorRegistry returns an empty registry.
func NewEntityEditorRegistry() *EntityEditorRegistry {
	return &EntityEditorRegistry{editors: map[string]EntityEditor{}}
}

// Register adds an editor. A duplicate Kind is an error rather than a silent
// overwrite: two things claiming to own "anime" is a wiring bug, and the loser
// of a silent overwrite would apply edits nobody could see coming.
func (r *EntityEditorRegistry) Register(e EntityEditor) error {
	if e == nil {
		return fmt.Errorf("entity registry: Register(nil)")
	}
	kind := e.Kind()
	if kind == "" {
		return fmt.Errorf("entity registry: editor has empty Kind")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.editors[kind]; dup {
		return fmt.Errorf("entity registry: %q already registered", kind)
	}
	r.editors[kind] = e
	return nil
}

// Editor returns the editor for a kind. The bool is false for an unknown kind —
// callers treat that as "this item cannot be applied" (a queued edit for a
// disabled plugin), not a panic.
func (r *EntityEditorRegistry) Editor(kind string) (EntityEditor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.editors[kind]
	return e, ok
}

// Editors returns every registered editor, so the review queue can enumerate
// the editable kinds without knowing their names.
func (r *EntityEditorRegistry) Editors() []EntityEditor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]EntityEditor, 0, len(r.editors))
	for _, e := range r.editors {
		out = append(out, e)
	}
	return out
}

// FieldAllowed reports whether field may be edited on kind — the whitelist
// check the host runs before Apply, in one place so every caller enforces it
// the same way.
func (r *EntityEditorRegistry) FieldAllowed(kind, field string) bool {
	e, ok := r.Editor(kind)
	if !ok {
		return false
	}
	for _, f := range e.Fields() {
		if f.Name == field {
			return true
		}
	}
	return false
}
