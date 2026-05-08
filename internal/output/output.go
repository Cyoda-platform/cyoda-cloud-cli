// Package output renders command results to an io.Writer in either
// pretty-printed JSON or a typed human-readable table.
//
// Per spec §6.5 commands write data (this package) to stdout and logs to
// stderr. The functions here take an io.Writer so callers can choose the
// destination — production code passes os.Stdout; tests pass a bytes.Buffer.
//
// Table formatters are intentionally typed, not reflection-driven: each
// resource (Me, env, app, build, …) gets its own function. This keeps the
// surface small and the output predictable. Only MeTable is implemented in
// this milestone; env/app/build land with their respective commands.
package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/api"
)

// JSON pretty-prints v as JSON with a two-space indent and a trailing
// newline. The trailing newline is what json.Encoder appends naturally; we
// don't strip it.
func JSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// EnvSnapshot is the unified shape for env rendering across both list and
// detail views. JSON tags mirror the OpenAPI EnvDetail/EnvSummary snake_case
// names so --output-json round-trips the API's own field naming
// (spec §6.5). Detail-only fields (AppNamespace, CyodaEnvURL, M2MClientID,
// BuildID, JobStatus, JobStatusText) use ,omitempty so the summary-list
// shape doesn't emit empty strings.
type EnvSnapshot struct {
	EnvID         string `json:"env_id,omitempty"`
	EnvName       string `json:"env_name,omitempty"`
	Namespace     string `json:"namespace,omitempty"`
	AppNamespace  string `json:"app_namespace,omitempty"`
	CyodaEnvURL   string `json:"cyoda_env_url,omitempty"`
	M2MClientID   string `json:"m2m_client_id,omitempty"`
	State         string `json:"state"`
	JobStatus     string `json:"job_status,omitempty"`
	JobStatusText string `json:"job_status_text,omitempty"`
	CreationDate  string `json:"creation_date,omitempty"`
	BuildID       string `json:"build_id,omitempty"`
}

// EnvTable renders an EnvSnapshot as a human-readable two-column table.
// Empty optional fields are omitted so a summary-shape snapshot
// (env_name + namespace + state only) doesn't emit blank rows for fields
// that only exist on the detail shape.
func EnvTable(w io.Writer, e *EnvSnapshot) error {
	if e == nil {
		return errors.New("output: EnvTable: nil EnvSnapshot")
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	rows := [][2]string{}
	addOpt := func(k, v string) {
		if v != "" {
			rows = append(rows, [2]string{k, v})
		}
	}
	// Order: identity, namespaces, URL, state, job state, timestamps.
	addOpt("ENV_NAME", e.EnvName)
	addOpt("ENV_ID", e.EnvID)
	addOpt("NAMESPACE", e.Namespace)
	addOpt("APP_NAMESPACE", e.AppNamespace)
	addOpt("CYODA_ENV_URL", e.CyodaEnvURL)
	addOpt("M2M_CLIENT_ID", e.M2MClientID)
	rows = append(rows, [2]string{"STATE", e.State})
	addOpt("JOB_STATUS", e.JobStatus)
	addOpt("JOB_STATUS_TEXT", e.JobStatusText)
	addOpt("CREATION_DATE", e.CreationDate)
	addOpt("BUILD_ID", e.BuildID)
	for _, r := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\n", r[0], r[1]); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// EnvListTable renders a slice of EnvSnapshots as a tabular list view with
// columns ENV_NAME / STATE / NAMESPACE / CREATION_DATE. The list is sorted
// by ENV_NAME for stable output across runs (server orders by creation_date
// desc, but the CLI surfaces the alphabetical view because list output is
// typically grepped/eyeballed by name).
func EnvListTable(w io.Writer, envs []EnvSnapshot) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ENV_NAME\tSTATE\tNAMESPACE\tCREATION_DATE"); err != nil {
		return err
	}
	sorted := make([]EnvSnapshot, len(envs))
	copy(sorted, envs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].EnvName < sorted[j].EnvName })
	for _, e := range sorted {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			e.EnvName, e.State, e.Namespace, e.CreationDate); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// BuildSnapshot is the unified-shape for build-table rendering. It mirrors
// the *api.Build fields the CLI surfaces; pointer-typed source fields are
// flattened to plain strings so callers don't repeat nil-deref boilerplate.
//
// JSON tags mirror the OpenAPI Build schema's snake_case names. branch_name
// is request-side (not on the Build response) but the snapshot carries it
// through from the build/deploy command's --branch flag so the user sees it
// in JSON output; the tag still uses snake_case for parity. Optional fields
// use ,omitempty so the queued-only case (most fields empty) emits a compact
// document.
type BuildSnapshot struct {
	BuildId       string `json:"build_id"`
	Action        string `json:"action,omitempty"`
	State         string `json:"state"`
	BranchName    string `json:"branch_name,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	JobStatus     string `json:"job_status,omitempty"`
	JobStatusText string `json:"job_status_text,omitempty"`
	PipelineName  string `json:"pipeline_name,omitempty"`
	ChatId        string `json:"chat_id,omitempty"`
}

// BuildTable renders a single BuildSnapshot as a two-column key/value table.
// Empty optional fields are omitted so the output stays compact for the
// queued-only case (where most fields are empty).
func BuildTable(w io.Writer, b *BuildSnapshot) error {
	if b == nil {
		return errors.New("output: BuildTable: nil BuildSnapshot")
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	rows := [][2]string{
		{"BUILD_ID", b.BuildId},
		{"ACTION", b.Action},
		{"STATE", b.State},
	}
	addOpt := func(k, v string) {
		if v != "" {
			rows = append(rows, [2]string{k, v})
		}
	}
	addOpt("BRANCH_NAME", b.BranchName)
	addOpt("CREATED_AT", b.CreatedAt)
	addOpt("PIPELINE_NAME", b.PipelineName)
	addOpt("JOB_STATUS", b.JobStatus)
	addOpt("JOB_STATUS_TEXT", b.JobStatusText)
	addOpt("CHAT_ID", b.ChatId)
	for _, r := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\n", r[0], r[1]); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// BuildListTable renders a list of BuildSnapshots as a tabular view with a
// fixed column order: BUILD_ID, ACTION, STATE, CREATED_AT. The schema's
// branch_name field is not on the Build model — it's a request-side field —
// so it's omitted here. The nextCursor argument is informational; when
// non-empty the caller is expected to print it elsewhere (typically stderr).
func BuildListTable(w io.Writer, bs []BuildSnapshot) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "BUILD_ID\tACTION\tSTATE\tCREATED_AT"); err != nil {
		return err
	}
	for _, b := range bs {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			b.BuildId, b.Action, b.State, b.CreatedAt); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// MeTable renders an *api.Me as a three-section human-readable table:
// identity, quota, features. Output is deterministic — features are sorted
// alphabetically by key, slice fields are joined with ", ".
func MeTable(w io.Writer, m *api.Me) error {
	if m == nil {
		return errors.New("output: MeTable: nil Me")
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	// Identity section.
	rows := []struct {
		k, v string
	}{
		{"USER_ID", m.UserId},
		{"ORG_ID", m.OrgId},
		{"TIER", m.Tier},
		{"ROLES", strings.Join(m.Roles, ", ")},
		{"SCOPES", strings.Join(m.Scopes, ", ")},
		{"IS_CYODA_EMPLOYEE", fmt.Sprintf("%t", m.IsCyodaEmployee)},
	}
	for _, r := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\n", r.k, r.v); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	// Quota section.
	if _, err := fmt.Fprintln(w, "\nQUOTA"); err != nil {
		return err
	}
	qtw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	q := m.Quota
	fmt.Fprintf(qtw, "ENV_DEPLOYS\t%d/%d\t(%s)\n", q.EnvDeploys.Used, q.EnvDeploys.Limit, q.EnvDeploys.Window)
	fmt.Fprintf(qtw, "APP_DEPLOYS\t%d/%d\t(%s)\n", q.AppDeploys.Used, q.AppDeploys.Limit, q.AppDeploys.Window)
	if err := qtw.Flush(); err != nil {
		return err
	}

	// Features section.
	if _, err := fmt.Fprintln(w, "\nFEATURES"); err != nil {
		return err
	}
	keys := make([]string, 0, len(m.Features))
	for k := range m.Features {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ftw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, k := range keys {
		fmt.Fprintf(ftw, "%s\t%t\n", k, m.Features[k])
	}
	return ftw.Flush()
}
