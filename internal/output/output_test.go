package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/api"
)

func TestJSON_PrettyPrints(t *testing.T) {
	var buf bytes.Buffer
	v := struct {
		Foo string `json:"foo"`
		Bar int    `json:"bar"`
	}{Foo: "hello", Bar: 7}
	if err := JSON(&buf, v); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	out := buf.String()
	// Pretty-printed: newlines and 2-space indent.
	if !strings.Contains(out, "\n  \"foo\": \"hello\"") {
		t.Errorf("expected pretty-printed indent, got:\n%s", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("expected trailing newline, got %q", out)
	}
	// Round-trip: still valid JSON.
	var back map[string]any
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back["foo"] != "hello" || back["bar"].(float64) != 7 {
		t.Errorf("decoded = %v", back)
	}
}

func TestMeTable_RendersAllSections(t *testing.T) {
	me := &api.Me{
		UserId:          "auth0|abc123",
		OrgId:           "org_acme",
		Tier:            "free",
		Roles:           []string{"member", "admin"},
		Scopes:          []string{"read:builds", "deploy:env"},
		IsCyodaEmployee: false,
		Features:        map[string]bool{"deploy_app": true, "shared_envs": false},
	}
	me.Quota.EnvDeploys = api.QuotaCounter{Window: "month", Used: 2, Limit: 10}
	me.Quota.AppDeploys = api.QuotaCounter{Window: "month", Used: 0, Limit: 5}

	var buf bytes.Buffer
	if err := MeTable(&buf, me); err != nil {
		t.Fatalf("MeTable: %v", err)
	}
	out := buf.String()

	// Identity section.
	for _, want := range []string{
		"USER_ID",
		"auth0|abc123",
		"ORG_ID",
		"org_acme",
		"TIER",
		"free",
		"ROLES",
		"member, admin",
		"SCOPES",
		"read:builds, deploy:env",
		"IS_CYODA_EMPLOYEE",
		"false",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("MeTable output missing %q\n%s", want, out)
		}
	}
	// Quota section.
	for _, want := range []string{
		"QUOTA",
		"ENV_DEPLOYS",
		"2/10",
		"APP_DEPLOYS",
		"0/5",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("MeTable output missing quota %q\n%s", want, out)
		}
	}
	// Features section: sorted keys, deterministic.
	for _, want := range []string{
		"FEATURES",
		"deploy_app",
		"true",
		"shared_envs",
		"false",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("MeTable output missing features %q\n%s", want, out)
		}
	}
	// Determinism: features must be sorted alphabetically (deploy_app before
	// shared_envs).
	if i, j := strings.Index(out, "deploy_app"), strings.Index(out, "shared_envs"); i < 0 || j < 0 || i >= j {
		t.Errorf("features not in sorted order:\n%s", out)
	}
}

func TestMeTable_Nil(t *testing.T) {
	var buf bytes.Buffer
	if err := MeTable(&buf, nil); err == nil {
		t.Fatal("MeTable(nil): expected error, got nil")
	}
}

func TestEnvTable_RendersAllFields(t *testing.T) {
	snap := &EnvSnapshot{
		EnvId:         "env_abc",
		Namespace:     "ns_org_acme",
		State:         "PROCESSING",
		JobStatus:     "RUNNING",
		JobStatusText: "rolling out cassandra-basic",
	}
	var buf bytes.Buffer
	if err := EnvTable(&buf, snap); err != nil {
		t.Fatalf("EnvTable: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"ENV_ID", "env_abc",
		"NAMESPACE", "ns_org_acme",
		"STATE", "PROCESSING",
		"JOB_STATUS", "RUNNING",
		"JOB_STATUS_TEXT", "rolling out cassandra-basic",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("EnvTable output missing %q\n%s", want, out)
		}
	}
}

func TestEnvTable_OmitsEmptyOptionalFields(t *testing.T) {
	snap := &EnvSnapshot{
		EnvId:     "env_abc",
		Namespace: "ns_x",
		State:     "PROCESSING",
	}
	var buf bytes.Buffer
	if err := EnvTable(&buf, snap); err != nil {
		t.Fatalf("EnvTable: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "JOB_STATUS") {
		t.Errorf("EnvTable should omit empty JOB_STATUS row:\n%s", out)
	}
}

func TestEnvTable_Nil(t *testing.T) {
	var buf bytes.Buffer
	if err := EnvTable(&buf, nil); err == nil {
		t.Fatal("EnvTable(nil): expected error, got nil")
	}
}

func TestBuildTable_RendersAllFields(t *testing.T) {
	snap := &BuildSnapshot{
		BuildId:       "bld_abc123",
		Action:        "BUILD",
		State:         "PROCESSING",
		BranchName:    "main",
		CreatedAt:     "2026-05-04T12:00:00Z",
		JobStatus:     "RUNNING",
		JobStatusText: "compiling user code",
		PipelineName:  "pipeline-default",
		ChatId:        "chat_xyz",
	}
	var buf bytes.Buffer
	if err := BuildTable(&buf, snap); err != nil {
		t.Fatalf("BuildTable: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"BUILD_ID", "bld_abc123",
		"ACTION", "BUILD",
		"STATE", "PROCESSING",
		"BRANCH_NAME", "main",
		"CREATED_AT", "2026-05-04T12:00:00Z",
		"JOB_STATUS", "RUNNING",
		"JOB_STATUS_TEXT", "compiling user code",
		"PIPELINE_NAME", "pipeline-default",
		"CHAT_ID", "chat_xyz",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("BuildTable output missing %q\n%s", want, out)
		}
	}
}

func TestBuildTable_OmitsEmptyOptionalFields(t *testing.T) {
	snap := &BuildSnapshot{
		BuildId: "bld_xyz",
		Action:  "BUILD",
		State:   "QUEUED",
	}
	var buf bytes.Buffer
	if err := BuildTable(&buf, snap); err != nil {
		t.Fatalf("BuildTable: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"BUILD_ID", "bld_xyz", "ACTION", "STATE", "QUEUED"} {
		if !strings.Contains(out, want) {
			t.Errorf("BuildTable output missing required %q\n%s", want, out)
		}
	}
	for _, omit := range []string{
		"BRANCH_NAME", "CREATED_AT", "PIPELINE_NAME",
		"JOB_STATUS", "JOB_STATUS_TEXT", "CHAT_ID",
	} {
		if strings.Contains(out, omit) {
			t.Errorf("BuildTable should omit empty optional %q\n%s", omit, out)
		}
	}
}

func TestBuildTable_Nil(t *testing.T) {
	var buf bytes.Buffer
	if err := BuildTable(&buf, nil); err == nil {
		t.Fatal("BuildTable(nil): expected error, got nil")
	}
	// And no output written on the nil path.
	if buf.Len() != 0 {
		t.Errorf("BuildTable(nil) wrote output: %q", buf.String())
	}
}

func TestBuildListTable_RendersList(t *testing.T) {
	bs := []BuildSnapshot{
		{
			BuildId:   "bld_1",
			Action:    "BUILD",
			State:     "SUCCESS",
			CreatedAt: "2026-05-04T10:00:00Z",
		},
		{
			BuildId:   "bld_2",
			Action:    "DEPLOY",
			State:     "PROCESSING",
			CreatedAt: "2026-05-04T11:00:00Z",
		},
	}
	var buf bytes.Buffer
	if err := BuildListTable(&buf, bs); err != nil {
		t.Fatalf("BuildListTable: %v", err)
	}
	out := buf.String()
	// Column headers.
	for _, want := range []string{"BUILD_ID", "ACTION", "STATE", "CREATED_AT"} {
		if !strings.Contains(out, want) {
			t.Errorf("BuildListTable output missing header %q\n%s", want, out)
		}
	}
	// Row values.
	for _, want := range []string{
		"bld_1", "BUILD", "SUCCESS", "2026-05-04T10:00:00Z",
		"bld_2", "DEPLOY", "PROCESSING", "2026-05-04T11:00:00Z",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("BuildListTable output missing row value %q\n%s", want, out)
		}
	}
}

// TestBuildListTable_RendersCursor — the implementation does not surface the
// cursor itself (it takes only []BuildSnapshot; per its doc the caller prints
// any non-empty cursor to stderr). Confirm the helper doesn't accidentally
// emit a cursor on its own and renders rows verbatim regardless.
func TestBuildListTable_RendersCursor(t *testing.T) {
	bs := []BuildSnapshot{
		{BuildId: "bld_only", Action: "BUILD", State: "SUCCESS", CreatedAt: "2026-05-04T10:00:00Z"},
	}
	var buf bytes.Buffer
	if err := BuildListTable(&buf, bs); err != nil {
		t.Fatalf("BuildListTable: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "bld_only") {
		t.Errorf("BuildListTable output missing row:\n%s", out)
	}
	// Helper must not invent a cursor row of its own — surface is rows + header only.
	if strings.Contains(strings.ToLower(out), "cursor") {
		t.Errorf("BuildListTable must not render cursor itself (caller's job):\n%s", out)
	}
}

func TestBuildListTable_EmptyList(t *testing.T) {
	var buf bytes.Buffer
	if err := BuildListTable(&buf, nil); err != nil {
		t.Fatalf("BuildListTable(nil): %v", err)
	}
	out := buf.String()
	// Header still rendered so the user sees an obvious empty-state shape.
	for _, want := range []string{"BUILD_ID", "ACTION", "STATE", "CREATED_AT"} {
		if !strings.Contains(out, want) {
			t.Errorf("BuildListTable empty output missing header %q\n%s", want, out)
		}
	}
	// No row data — only the header line.
	if n := strings.Count(out, "\n"); n != 1 {
		t.Errorf("BuildListTable empty output should have one line (header only), got %d:\n%s", n, out)
	}
}
