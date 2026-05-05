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
