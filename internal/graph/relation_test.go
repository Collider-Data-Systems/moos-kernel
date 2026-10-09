package graph

import (
	"encoding/json"
	"testing"
	"time"
)

// TestRelation_CarriesNoPortColors_t342: a relation's JSON shape has no
// src_color / tgt_color keys (t342 ruling 6). They were never written and
// served "" on every relation; an end's color lives in the operad's
// PortColors, keyed by port name.
func TestRelation_CarriesNoPortColors_t342(t *testing.T) {
	rel := Relation{
		URN:             "urn:moos:rel:t342.bound",
		RewriteCategory: WF08,
		SrcURN:          "urn:moos:program:t342",
		SrcPort:         "bound-to",
		TgtURN:          "urn:moos:storage:t342",
		TgtPort:         "binds",
		CreatedAt:       time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(rel)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"src_color", "tgt_color"} {
		if _, ok := keys[k]; ok {
			t.Errorf("relation JSON still carries %q: %s", k, raw)
		}
	}
	for _, k := range []string{"urn", "rewrite_category", "src_urn", "src_port", "tgt_urn", "tgt_port", "created_at"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("relation JSON lost %q: %s", k, raw)
		}
	}

	// A pre-t342 serialization (both keys "") still decodes: the keys are
	// ignored, every other field survives.
	old := []byte(`{"urn":"urn:moos:rel:t342.bound","rewrite_category":"WF08","src_urn":"urn:moos:program:t342","src_port":"bound-to","src_color":"","tgt_urn":"urn:moos:storage:t342","tgt_port":"binds","tgt_color":"","created_at":"2026-10-09T00:00:00Z"}`)
	var back Relation
	if err := json.Unmarshal(old, &back); err != nil {
		t.Fatalf("decode pre-t342 relation: %v", err)
	}
	if !back.CreatedAt.Equal(rel.CreatedAt) {
		t.Errorf("pre-t342 relation created_at = %v, want %v", back.CreatedAt, rel.CreatedAt)
	}
	back.CreatedAt = rel.CreatedAt
	if back != rel {
		t.Errorf("pre-t342 relation decoded to %+v, want %+v", back, rel)
	}
}
