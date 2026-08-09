package roadmap

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/lib/pq"
)

func TestParseSystemIDs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want pq.Int64Array
	}{
		{"empty", "", pq.Int64Array{}},
		{"whitespace only", "   \t\n ", pq.Int64Array{}},
		{"single", "42", pq.Int64Array{42}},
		{"comma separated", "1,2,3", pq.Int64Array{1, 2, 3}},
		{"space separated", "1 2 3", pq.Int64Array{1, 2, 3}},
		{"mixed separators", "1, 2\t3\n4", pq.Int64Array{1, 2, 3, 4}},
		{"dedup preserves first order", "3,1,3,1,2", pq.Int64Array{3, 1, 2}},
		{"skips zero and negatives", "0,-5,7", pq.Int64Array{7}},
		{"skips non-numeric", "1,abc,2", pq.Int64Array{1, 2}},
		{"stray commas tolerated", ",,1,,2,,", pq.Int64Array{1, 2}},
		{"all invalid yields empty", "0,-1,foo", pq.Int64Array{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSystemIDs(tt.in)
			if got == nil {
				t.Fatalf("parseSystemIDs(%q) returned nil; must be non-nil to satisfy NOT NULL column", tt.in)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseSystemIDs(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestCoalesceInt64Array(t *testing.T) {
	if got := CoalesceInt64Array(nil); got == nil || len(got) != 0 {
		t.Errorf("nil input: got %v, want non-nil empty", got)
	}
	in := pq.Int64Array{1, 2, 3}
	if got := CoalesceInt64Array(in); !reflect.DeepEqual(got, in) {
		t.Errorf("non-nil input: got %v, want %v", got, in)
	}
	// A non-nil empty slice must be returned as-is (already satisfies the column).
	empty := pq.Int64Array{}
	if got := CoalesceInt64Array(empty); got == nil {
		t.Errorf("empty input returned nil")
	}
}

// The markdown pipeline crosses the host sanitiser seam; tests install an
// identity stand-in (the allow-list itself is host-tested).
func init() {
	SetDeps(Deps{SanitizeForum: func(s string) string { return s }})
}

func modUser(id int) *Viewer   { return &Viewer{ID: id, Mod: true} }
func plainUser(id int) *Viewer { return &Viewer{ID: id} }

func TestCanModifyFlowNode(t *testing.T) {
	owner := 7
	other := 9
	tests := []struct {
		name string
		user *Viewer
		node *FlowNode
		want bool
	}{
		{"nil user", nil, &FlowNode{}, false},
		{"mod overrides locked", modUser(1), &FlowNode{Locked: true, CreatedBy: &owner}, true},
		{"mod overrides ownership", modUser(1), &FlowNode{CreatedBy: &owner}, true},
		{"user blocked on locked", plainUser(owner), &FlowNode{Locked: true, CreatedBy: &owner}, false},
		{"owner can modify own unlocked", plainUser(owner), &FlowNode{CreatedBy: &owner}, true},
		{"non-owner blocked", plainUser(other), &FlowNode{CreatedBy: &owner}, false},
		{"user blocked on seed with nil creator", plainUser(owner), &FlowNode{CreatedBy: nil}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canModifyFlowNode(tt.user, tt.node); got != tt.want {
				t.Errorf("canModifyFlowNode = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCanModifyFlowEdge(t *testing.T) {
	owner := 3
	other := 4
	tests := []struct {
		name string
		user *Viewer
		edge *FlowEdge
		want bool
	}{
		{"nil user", nil, &FlowEdge{}, false},
		{"mod overrides locked", modUser(1), &FlowEdge{Locked: true, CreatedBy: &owner}, true},
		{"user blocked on locked", plainUser(owner), &FlowEdge{Locked: true, CreatedBy: &owner}, false},
		{"owner can modify own", plainUser(owner), &FlowEdge{CreatedBy: &owner}, true},
		{"non-owner blocked", plainUser(other), &FlowEdge{CreatedBy: &owner}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canModifyFlowEdge(tt.user, tt.edge); got != tt.want {
				t.Errorf("canModifyFlowEdge = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProcessMockupData(t *testing.T) {
	t.Run("non-mockup kind passes through untouched", func(t *testing.T) {
		raw := json.RawMessage(`{"markdown":"# hi"}`)
		got := processMockupData("system", raw)
		if string(got) != string(raw) {
			t.Errorf("non-mockup mutated: got %s", got)
		}
	})
	t.Run("empty payload passes through", func(t *testing.T) {
		got := processMockupData("mockup", nil)
		if got != nil {
			t.Errorf("nil payload should pass through, got %s", got)
		}
	})
	t.Run("bad json passes through untouched", func(t *testing.T) {
		raw := json.RawMessage(`{not valid`)
		got := processMockupData("mockup", raw)
		if string(got) != string(raw) {
			t.Errorf("bad json mutated: got %s", got)
		}
	})
	t.Run("mockup injects markdown_html", func(t *testing.T) {
		raw := json.RawMessage(`{"markdown":"**bold**"}`)
		got := processMockupData("mockup", raw)
		var obj map[string]interface{}
		if err := json.Unmarshal(got, &obj); err != nil {
			t.Fatalf("result not valid JSON: %v", err)
		}
		html, ok := obj["markdown_html"].(string)
		if !ok {
			t.Fatalf("markdown_html missing from %s", got)
		}
		if html == "" {
			t.Errorf("expected rendered html for bold markdown, got empty")
		}
		// Source field must survive.
		if obj["markdown"] != "**bold**" {
			t.Errorf("markdown source lost: %v", obj["markdown"])
		}
	})
}

func TestExtractMockupBits(t *testing.T) {
	t.Run("nil node yields empties", func(t *testing.T) {
		h, md, mdHTML := extractMockupBits(nil)
		if h != "" || md != "" || mdHTML != "" {
			t.Errorf("expected all empty, got %q %q %q", h, md, mdHTML)
		}
	})
	t.Run("empty data yields empties", func(t *testing.T) {
		h, md, mdHTML := extractMockupBits(&FlowNode{})
		if h != "" || md != "" || mdHTML != "" {
			t.Errorf("expected all empty, got %q %q %q", h, md, mdHTML)
		}
	})
	t.Run("bad json yields empties", func(t *testing.T) {
		n := &FlowNode{DataJSON: []byte(`{broken`)}
		h, md, mdHTML := extractMockupBits(n)
		if h != "" || md != "" || mdHTML != "" {
			t.Errorf("expected all empty, got %q %q %q", h, md, mdHTML)
		}
	})
	t.Run("extracts present fields, missing stay empty", func(t *testing.T) {
		n := &FlowNode{DataJSON: []byte(`{"html":"<div>x</div>","markdown":"# t"}`)}
		h, md, mdHTML := extractMockupBits(n)
		if h != "<div>x</div>" {
			t.Errorf("html = %q", h)
		}
		if md != "# t" {
			t.Errorf("markdown = %q", md)
		}
		if mdHTML != "" {
			t.Errorf("markdown_html should be empty when absent, got %q", mdHTML)
		}
	})
}
