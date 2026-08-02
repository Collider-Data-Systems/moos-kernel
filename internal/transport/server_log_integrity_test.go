package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"moos/kernel/internal/kernel"
)

// A6 transport coverage: /healthz stays backward-compatible while gaining the
// bounded scalars, and GET /log/integrity serves the forensic detail.
//
// LogIntegrity satisfies kernel.InspectKernel for the shared fakeInspect stub
// (defined in thook_test.go). Tests that don't care about integrity get the
// zero report; integrityInspect below overrides it where it matters.
func (f *fakeInspect) LogIntegrity() kernel.LogIntegrity { return kernel.LogIntegrity{} }

// integrityInspect serves a canned report without needing a real Runtime.
type integrityInspect struct {
	*fakeInspect
	report kernel.LogIntegrity
}

func (i *integrityInspect) LogIntegrity() kernel.LogIntegrity { return i.report }
func (i *integrityInspect) LogStats() (int, int64) {
	return i.report.LogLen, i.report.MaxLogSeq
}
func (i *integrityInspect) LogSeqMissing() int { return i.report.LogSeqMissing }

// reportWithCollision mirrors the shape of a real duplicate group without any
// real content — moos-kernel is PUBLIC and no sovereign-log data belongs here.
func reportWithCollision() kernel.LogIntegrity {
	return kernel.LogIntegrity{
		KernelURN:                    "urn:moos:kernel:test.primary",
		LogLen:                       8,
		MaxLogSeq:                    5,
		LogSeqMissing:                0,
		DuplicateLogSeqValues:        3,
		DuplicateLogSeqExcessEntries: 3,
		CollisionKinds: map[string]int{
			kernel.KindExactReapply:     2,
			kernel.KindDistinctRewrites: 1,
		},
		LoggedNoFoldEffect: 2,
		SingleWriter:       true,
		Groups: []kernel.CollisionGroup{{
			LogSeq:            3,
			Count:             2,
			Kind:              kernel.KindExactReapply,
			DistinctAppliedAt: 2,
			AppliedCount:      1,
			Entries: []kernel.CollisionEntry{
				{AppendPosition: 3, RewriteType: "ADD", Fingerprint: "aaaa000000000000", FoldEffect: kernel.EntryApplied},
				{AppendPosition: 4, RewriteType: "ADD", Fingerprint: "aaaa000000000000", FoldEffect: kernel.EntryLoggedNoFoldEffect},
			},
		}},
	}
}

func serverWithReport(rep kernel.LogIntegrity) *Server {
	return &Server{inspect: &integrityInspect{fakeInspect: &fakeInspect{}, report: rep}}
}

func getJSON(t *testing.T, h http.HandlerFunc, path string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d, want 200", path, rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: unmarshal: %v", path, err)
	}
	return body
}

// The compatibility guarantee: nothing an existing consumer reads may move.
// The router fan-in, Doctor Pass 2b and the validator skill all key on these.
func TestHealthz_ExistingPropertiesUnchanged(t *testing.T) {
	s := serverWithReport(reportWithCollision())
	body := getJSON(t, s.handleHealthz, "/healthz")

	for _, k := range []string{"status", "log_len", "max_log_seq", "log_seq_missing", "t_day", "ontology_version"} {
		if _, ok := body[k]; !ok {
			t.Errorf("/healthz lost pre-existing property %q — breaks existing consumers", k)
		}
	}
	if body["status"] != "ok" {
		t.Errorf("status = %v, want ok", body["status"])
	}
	if body["log_len"].(float64) != 8 || body["max_log_seq"].(float64) != 5 {
		t.Errorf("log_len/max_log_seq = %v/%v, want 8/5", body["log_len"], body["max_log_seq"])
	}
}

func TestHealthz_AddsBoundedScalars(t *testing.T) {
	s := serverWithReport(reportWithCollision())
	body := getJSON(t, s.handleHealthz, "/healthz")

	if got := body["duplicate_log_seq_values"].(float64); got != 3 {
		t.Errorf("duplicate_log_seq_values = %v, want 3", got)
	}
	if got := body["duplicate_log_seq_excess_entries"].(float64); got != 3 {
		t.Errorf("duplicate_log_seq_excess_entries = %v, want 3", got)
	}
	kinds, ok := body["log_seq_collision_kinds"].(map[string]any)
	if !ok {
		t.Fatalf("log_seq_collision_kinds is %T, want an object", body["log_seq_collision_kinds"])
	}
	if kinds[kernel.KindExactReapply].(float64) != 2 || kinds[kernel.KindDistinctRewrites].(float64) != 1 {
		t.Errorf("collision kinds = %v, want exact_reapply:2 distinct_rewrites:1", kinds)
	}
	// The forward-looking signal — retrospective counters can't detect a NEW
	// collision if the single-writer lock was bypassed.
	if v, ok := body["log_single_writer"].(bool); !ok || !v {
		t.Errorf("log_single_writer = %v, want true", body["log_single_writer"])
	}
	// Self-identification: the fan-in carries no kernel key.
	if body["kernel_urn"] != "urn:moos:kernel:test.primary" {
		t.Errorf("kernel_urn = %v, want the report's URN", body["kernel_urn"])
	}
	// Detail must NOT ride on /healthz — it stays cheap enough to poll.
	if _, leaked := body["collision_groups"]; leaked {
		t.Error("/healthz carries collision_groups; detail belongs on /log/integrity")
	}
}

// A kernel with no URN configured must simply omit the key, not emit "".
func TestHealthz_OmitsEmptyKernelURN(t *testing.T) {
	rep := reportWithCollision()
	rep.KernelURN = ""
	s := serverWithReport(rep)
	body := getJSON(t, s.handleHealthz, "/healthz")

	if _, present := body["kernel_urn"]; present {
		t.Error("kernel_urn present but empty; want the key omitted")
	}
}

func TestLogIntegrity_EndpointServesDetail(t *testing.T) {
	s := serverWithReport(reportWithCollision())
	body := getJSON(t, s.handleLogIntegrity, "/log/integrity")

	groups, ok := body["collision_groups"].([]any)
	if !ok || len(groups) != 1 {
		t.Fatalf("collision_groups = %v, want one group", body["collision_groups"])
	}
	g := groups[0].(map[string]any)
	if g["log_seq"].(float64) != 3 {
		t.Errorf("group log_seq = %v, want 3", g["log_seq"])
	}
	if g["applied_count"].(float64) != 1 {
		t.Errorf("applied_count = %v, want 1", g["applied_count"])
	}

	entries := g["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	e0, e1 := entries[0].(map[string]any), entries[1].(map[string]any)

	// Append positions must uniquely identify both members of a collision.
	if e0["append_position"].(float64) != 3 || e1["append_position"].(float64) != 4 {
		t.Errorf("append positions = %v/%v, want 3/4", e0["append_position"], e1["append_position"])
	}
	// The premise fix, visible on the wire: identical fingerprints, different
	// fold effect. A reader must not be able to conclude "applied twice".
	if e0["fingerprint"] != e1["fingerprint"] {
		t.Error("fingerprints differ within an exact_reapply group")
	}
	if e0["fold_effect"] != kernel.EntryApplied {
		t.Errorf("first entry fold_effect = %v, want %q", e0["fold_effect"], kernel.EntryApplied)
	}
	if e1["fold_effect"] != kernel.EntryLoggedNoFoldEffect {
		t.Errorf("second entry fold_effect = %v, want %q", e1["fold_effect"], kernel.EntryLoggedNoFoldEffect)
	}
}

// A clean fold must answer with the same shape and zeroes — an empty body would
// be indistinguishable from a broken endpoint.
func TestLogIntegrity_EndpointOnCleanFold(t *testing.T) {
	s := serverWithReport(kernel.LogIntegrity{
		LogLen: 42, MaxLogSeq: 42, CollisionKinds: map[string]int{}, SingleWriter: true,
	})
	body := getJSON(t, s.handleLogIntegrity, "/log/integrity")

	if body["duplicate_log_seq_values"].(float64) != 0 {
		t.Errorf("duplicate_log_seq_values = %v, want 0", body["duplicate_log_seq_values"])
	}
	if groups := body["collision_groups"]; groups != nil {
		if g, ok := groups.([]any); ok && len(g) != 0 {
			t.Errorf("collision_groups = %v, want empty", g)
		}
	}
	if body["log_len"].(float64) != 42 {
		t.Errorf("log_len = %v, want 42", body["log_len"])
	}
}
