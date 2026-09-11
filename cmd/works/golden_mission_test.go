package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestGoldenMissionInputRunsSuccessWithIndependentVerification(t *testing.T) {
	id := func(prefix, ch string) string { return prefix + strings.Repeat(ch, 32) }
	canonical := map[string]any{
		"tenant_id":            id("ten_", "1"),
		"principal_id":         id("prn_", "2"),
		"mission_id":           "mis_live_seam_fixture",
		"authority_lease_id":   id("auth_", "3"),
		"execution_context_id": id("ctx_", "4"),
		"work_id":              id("wrk_", "5"),
		"trace_id":             id("trc_", "6"),
		"action_id":            id("act_", "7"),
		"action_decision_id":   id("pdr_", "8"),
	}
	stage := func(name string) map[string]any {
		return map[string]any{"name": name, "ids": canonical}
	}
	fixture := map[string]any{
		"runner_principal": id("prn_", "9"),
		"mission": map[string]any{
			"branch":    "success",
			"canonical": canonical,
			"stages": []any{
				stage("studio"), stage("aie"), stage("trust-gateway"),
				stage("runtime"), stage("works"), stage("verification"),
			},
		},
		"verification": map[string]any{
			"verifier_principal": id("prn_", "a"),
			"verdict":            "accept",
			"action_id":          canonical["action_id"],
			"action_decision_id": canonical["action_decision_id"],
		},
	}
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runGoldenMissionInput(bytes.NewReader(raw), &out); err != nil {
		t.Fatalf("runGoldenMissionInput: %v", err)
	}

	var got struct {
		Decision    string `json:"decision"`
		Pin         string `json:"pin"`
		EffectCount int    `json:"effect_count"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out.String())
	}
	if got.Decision != "accept" {
		t.Fatalf("decision=%q want accept", got.Decision)
	}
	if got.Pin == "" {
		t.Fatal("missing transcript pin")
	}
	if got.EffectCount != 1 {
		t.Fatalf("effect_count=%d want 1", got.EffectCount)
	}
}

func TestGoldenMissionCmdAcceptsStdinFixture(t *testing.T) {
	id := func(prefix, ch string) string { return prefix + strings.Repeat(ch, 32) }
	canonical := map[string]any{
		"tenant_id": id("ten_", "1"), "principal_id": id("prn_", "2"),
		"mission_id": "mis_live_cli_fixture", "authority_lease_id": id("auth_", "3"),
		"execution_context_id": id("ctx_", "4"), "work_id": id("wrk_", "5"),
		"trace_id": id("trc_", "6"), "action_id": id("act_", "7"),
		"action_decision_id": id("pdr_", "8"),
	}
	stage := func(name string) map[string]any { return map[string]any{"name": name, "ids": canonical} }
	fixture := map[string]any{
		"runner_principal": id("prn_", "9"),
		"mission": map[string]any{"branch": "success", "canonical": canonical, "stages": []any{
			stage("studio"), stage("aie"), stage("trust-gateway"), stage("runtime"), stage("works"), stage("verification"),
		}},
		"verification": map[string]any{"verifier_principal": id("prn_", "a"), "verdict": "accept", "action_id": canonical["action_id"], "action_decision_id": canonical["action_decision_id"]},
	}
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := goldenMissionCmd([]string{"--input", "-"}, bytes.NewReader(raw), &out); err != nil {
		t.Fatalf("goldenMissionCmd: %v", err)
	}
	if !strings.Contains(out.String(), `"decision": "accept"`) {
		t.Fatalf("output missing accept decision: %s", out.String())
	}
}
