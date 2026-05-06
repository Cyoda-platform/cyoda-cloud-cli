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
