// Package output renders domain types in the formats exposed by the CLI
// (table, json, yaml, wide). NDJSON is deferred until a streaming
// command needs it.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/dj-wasabi/esops-go/internal/types"
)

// Format identifies a user-facing output format.
type Format string

const (
	FormatTable  Format = "table"
	FormatJSON   Format = "json"
	FormatYAML   Format = "yaml"
	FormatWide   Format = "wide"
	FormatNDJSON Format = "ndjson"
)

// Parse validates and normalises a --output value. Per-command
// restrictions (e.g. "health doesn't support ndjson") are enforced by
// cmdsetup.Prepare using the command's SupportedFormats allowlist — the
// renderers here assume the format has already been cleared for use.
func Parse(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "table":
		return FormatTable, nil
	case "json":
		return FormatJSON, nil
	case "yaml", "yml":
		return FormatYAML, nil
	case "wide":
		return FormatWide, nil
	case "ndjson":
		return FormatNDJSON, nil
	default:
		return "", fmt.Errorf("unknown output format %q (supported: table, json, ndjson, yaml, wide)", s)
	}
}

// Health renders a ClusterHealth in the requested format to w.
// For JSON/YAML, only the health record is written (dialect/context
// metadata goes to stderr via the caller); scripts get a stable shape.
func Health(w io.Writer, h types.ClusterHealth, info types.ClusterInfo, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(h)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(h)
	case FormatWide:
		return healthTable(w, h, info, true)
	case FormatTable, "":
		return healthTable(w, h, info, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// Shards renders a slice of Shard rows in the requested format. Table
// output shows the columns an operator reaches for first during an
// incident (index, shard, role, state, node, size). Wide adds docs,
// IP, and the raw STORE_BYTES. JSON/YAML emit the neutral Shard shape
// unchanged — scripts depend on stable field names.
func Shards(w io.Writer, shards []types.Shard, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(shards)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(shards)
	case FormatWide:
		return shardsTable(w, shards, true)
	case FormatTable, "":
		return shardsTable(w, shards, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// Indices renders a slice of Index rows in the requested format. Table
// output shows the columns an operator reaches for when triaging an
// index-count or size question (name, health, status, primaries/replicas,
// docs, size). Wide adds deleted docs, UUID, creation date and
// primary-store bytes. JSON/YAML emit the neutral Index shape;
// NDJSON writes one Index per line so large lists don't have to be
// held in the downstream consumer's memory in one blob.
func Indices(w io.Writer, indices []types.Index, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(indices)
	case FormatNDJSON:
		enc := json.NewEncoder(w)
		for _, ix := range indices {
			if err := enc.Encode(ix); err != nil {
				return err
			}
		}
		return nil
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(indices)
	case FormatWide:
		return indicesTable(w, indices, true)
	case FormatTable, "":
		return indicesTable(w, indices, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// HotThreads renders a /_nodes/hot_threads response. The cluster returns
// a free-form text block (per-node "::: {node}..." headers followed by
// stack samples) that is the canonical diagnostic format — operators
// read it by eye and paste it into tickets. We preserve it verbatim for
// table/wide; JSON/YAML wrap it in {"text": "..."} so the scripted
// contract ("JSON output is a single object") holds without pretending
// to parse a schema that is really an implementation detail of the
// cluster's own hot-threads printer.
func HotThreads(w io.Writer, h types.HotThreads, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(h)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(h)
	case FormatTable, FormatWide, "":
		_, err := io.WriteString(w, h.Text)
		if err != nil {
			return err
		}
		// Guarantee the output ends on a newline so shell prompts don't
		// land in the middle of the last stack frame. The cluster already
		// usually adds one, but some older ES versions skip the trailing
		// newline on empty responses.
		if !strings.HasSuffix(h.Text, "\n") {
			_, err = io.WriteString(w, "\n")
		}
		return err
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// Audit renders a SecurityAudit — the full `ops audit` view. JSON/YAML
// emit the whole struct as one object so scripts can read TLS, status,
// users, and roles in a single parse. The table format is a four-section
// layout (TLS, status, users, roles) read top-to-bottom; wide adds
// backend-role / email / description columns that are too wide for the
// default view.
//
// When the cluster reports security as disabled, the users and roles
// sections are suppressed — the status line carries the explanation.
func Audit(w io.Writer, a types.SecurityAudit, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(a)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(a)
	case FormatWide:
		return auditTable(w, a, true)
	case FormatTable, "":
		return auditTable(w, a, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

// Rebalance renders a /_cluster/reroute outcome. Table output is a
// two-section layout: a one-line "did the master ack? was it a dry
// run?" header, then a per-command summary listing the action and
// the {from,to} nodes the command targeted. Wide mode adds the cluster's
// per-decider explanations[] block when the request was made with
// --explain — operators reading "the cluster refused my move, why?" want
// the deciders inline, not behind a second command. JSON/YAML emit the
// neutral RerouteResult shape directly.
func Rebalance(w io.Writer, r types.RerouteResult, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(r)
	case FormatWide:
		return rebalanceTable(w, r, true)
	case FormatTable, "":
		return rebalanceTable(w, r, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func rebalanceTable(w io.Writer, r types.RerouteResult, wide bool) error {
	hw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(hw, "ACKNOWLEDGED\tDRY_RUN\tCOMMANDS")
	_, _ = fmt.Fprintf(hw, "%t\t%t\t%d\n", r.Acknowledged, r.DryRun, len(r.Commands))
	if err := hw.Flush(); err != nil {
		return err
	}

	if len(r.Commands) > 0 {
		_, _ = fmt.Fprintln(w)
		cw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(cw, "ACTION\tINDEX\tSHARD\tFROM\tTO\tFLAGS")
		for _, cmd := range r.Commands {
			_, _ = fmt.Fprintf(cw, "%s\t%s\t%d\t%s\t%s\t%s\n",
				cmd.Action,
				dashIfEmpty(cmd.Index),
				cmd.Shard,
				dashIfEmpty(cmd.FromNode),
				dashIfEmpty(cmd.ToNode),
				dashIfEmpty(rebalanceFlags(cmd)),
			)
		}
		if err := cw.Flush(); err != nil {
			return err
		}
	}

	if len(r.Explanations) == 0 {
		return nil
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "EXPLANATIONS")
	ew := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(ew, "COMMAND\tDECIDER\tDECISION\tEXPLANATION")
		for _, ex := range r.Explanations {
			if len(ex.Decisions) == 0 {
				_, _ = fmt.Fprintf(ew, "%s\t-\t-\t-\n", ex.Command)
				continue
			}
			for _, d := range ex.Decisions {
				_, _ = fmt.Fprintf(ew, "%s\t%s\t%s\t%s\n",
					ex.Command, d.Decider, d.Decision, d.Explanation)
			}
		}
	} else {
		_, _ = fmt.Fprintln(ew, "COMMAND\tDECISIONS")
		for _, ex := range r.Explanations {
			_, _ = fmt.Fprintf(ew, "%s\t%s\n", ex.Command, summariseRerouteDecisions(ex.Decisions))
		}
	}
	return ew.Flush()
}

// rebalanceFlags renders the boolean knobs (allow_primary,
// accept_data_loss) as a compact "k1,k2" cell — only flags that are
// actually set make it into the cell, so the column reads "-" for the
// happy path of a routine move.
func rebalanceFlags(cmd types.RerouteCommand) string {
	parts := make([]string, 0, 2)
	if cmd.AllowPrimary {
		parts = append(parts, "allow_primary")
	}
	if cmd.AcceptDataLoss {
		parts = append(parts, "accept_data_loss")
	}
	return strings.Join(parts, ",")
}

// summariseRerouteDecisions compresses a deciders list into a single
// cell for the default table — same logic as the allocation-explain
// summary: NO/THROTTLE rulings carry the signal, YES rulings are noise
// unless the operator asked for them.
func summariseRerouteDecisions(ds []types.RerouteDecision) string {
	if len(ds) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(ds))
	for _, d := range ds {
		if strings.EqualFold(d.Decision, "YES") {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", d.Decider, strings.ToLower(d.Decision)))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func auditTable(w io.Writer, a types.SecurityAudit, wide bool) error {
	// Section 1: TLS posture. Four compact key/value lines; no tabwriter
	// needed — the short keys keep alignment without it.
	_, _ = fmt.Fprintln(w, "TLS")
	_, _ = fmt.Fprintf(w, "  scheme:     %s\n", a.TLS.Scheme)
	_, _ = fmt.Fprintf(w, "  insecure:   %t\n", a.TLS.InsecureSkipVerify)
	_, _ = fmt.Fprintf(w, "  cacert_set: %t\n", a.TLS.CACertSet)
	_, _ = fmt.Fprintf(w, "  auth:       %s\n", a.TLS.AuthType)
	if a.TLS.ClientCertSet {
		_, _ = fmt.Fprintln(w, "  client_cert: true")
	}

	// Section 2: cluster-side status. When security is disabled the note
	// is the load-bearing piece; elevate it so the operator can't miss it.
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "SECURITY")
	_, _ = fmt.Fprintf(w, "  dialect:  %s\n", a.Status.Dialect)
	_, _ = fmt.Fprintf(w, "  enabled:  %t\n", a.Status.Enabled)
	if a.Status.Note != "" {
		_, _ = fmt.Fprintf(w, "  note:     %s\n", a.Status.Note)
	}

	if !a.Status.Enabled {
		return nil
	}

	// Section 3: users table. Reserved flag rendered as "res" — short
	// enough to keep the table narrow, obvious in meaning from context.
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "USERS")
	uw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(uw, "USERNAME\tENABLED\tRESERVED\tROLES\tBACKEND_ROLES\tFULL_NAME\tEMAIL")
	} else {
		_, _ = fmt.Fprintln(uw, "USERNAME\tENABLED\tRESERVED\tROLES")
	}
	for _, u := range a.Users {
		if wide {
			_, _ = fmt.Fprintf(uw, "%s\t%t\t%t\t%s\t%s\t%s\t%s\n",
				u.Username, u.Enabled, u.Reserved,
				joinOrDash(u.Roles), joinOrDash(u.BackendRoles),
				dashIfEmpty(u.FullName), dashIfEmpty(u.Email))
		} else {
			_, _ = fmt.Fprintf(uw, "%s\t%t\t%t\t%s\n",
				u.Username, u.Enabled, u.Reserved, joinOrDash(u.Roles))
		}
	}
	if err := uw.Flush(); err != nil {
		return err
	}

	// Section 4: roles table. The cluster privileges column is truncated
	// in the default view since "all" is often short but cluster lists
	// can be long; wide mode shows the full list.
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "ROLES")
	rw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(rw, "NAME\tRESERVED\tCLUSTER\tINDEX_PATTERNS\tDESCRIPTION")
	} else {
		_, _ = fmt.Fprintln(rw, "NAME\tRESERVED\tCLUSTER\tINDEX_PATTERNS")
	}
	for _, r := range a.Roles {
		if wide {
			_, _ = fmt.Fprintf(rw, "%s\t%t\t%s\t%s\t%s\n",
				r.Name, r.Reserved,
				joinOrDash(r.ClusterPrivileges), joinOrDash(r.IndexPatterns),
				dashIfEmpty(r.Description))
		} else {
			_, _ = fmt.Fprintf(rw, "%s\t%t\t%s\t%s\n",
				r.Name, r.Reserved,
				truncate(joinOrDash(r.ClusterPrivileges), 40),
				truncate(joinOrDash(r.IndexPatterns), 40))
		}
	}
	return rw.Flush()
}

func joinOrDash(s []string) string {
	if len(s) == 0 {
		return "-"
	}
	return strings.Join(s, ",")
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// truncate clips a cell's content to n runes, appending an ellipsis when
// it had to cut — so the default table stays one row per entity but the
// operator still sees that there's more behind `-o wide`.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// AllocationExplain renders a single allocation-explain record. JSON/YAML
// emit the neutral AllocationExplanation shape directly; the table format
// is a two-section layout operators read top-to-bottom: a header block
// with the shard-level facts ("is it assigned? why not?") followed by a
// per-node decision table. Wide expands the decision table into a full
// deciders dump (one row per decider rule), which is the signal-dense
// view when debugging "why did the cluster not pick node X?".
func AllocationExplain(w io.Writer, ex types.AllocationExplanation, f Format) error {
	switch f {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(ex)
	case FormatYAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(ex)
	case FormatWide:
		return allocationExplainTable(w, ex, true)
	case FormatTable, "":
		return allocationExplainTable(w, ex, false)
	default:
		return fmt.Errorf("unsupported output format %q", f)
	}
}

func allocationExplainTable(w io.Writer, ex types.AllocationExplanation, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	role := "r"
	if ex.Primary {
		role = "p"
	}
	node := "-"
	if ex.CurrentNode != nil && ex.CurrentNode.Name != "" {
		node = ex.CurrentNode.Name
	}
	canAllocate := ex.CanAllocate
	if canAllocate == "" {
		canAllocate = "-"
	}
	_, _ = fmt.Fprintln(tw, "INDEX\tSHARD\tROLE\tSTATE\tNODE\tCAN_ALLOCATE")
	_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n",
		ex.Index, ex.Shard, role, ex.CurrentState, node, canAllocate)
	if err := tw.Flush(); err != nil {
		return err
	}

	// Narrative block: reason for unassignment + human-language
	// explanation strings. Kept outside the tabwriter so free-form prose
	// wraps naturally in terminals.
	if ex.UnassignedInfo != nil {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintf(w, "UNASSIGNED_REASON: %s\n", ex.UnassignedInfo.Reason)
		if ex.UnassignedInfo.At != "" {
			_, _ = fmt.Fprintf(w, "UNASSIGNED_AT:     %s\n", ex.UnassignedInfo.At)
		}
		if ex.UnassignedInfo.LastAllocationStatus != "" {
			_, _ = fmt.Fprintf(w, "LAST_STATUS:       %s\n", ex.UnassignedInfo.LastAllocationStatus)
		}
		if ex.UnassignedInfo.Details != "" {
			_, _ = fmt.Fprintf(w, "DETAILS:           %s\n", ex.UnassignedInfo.Details)
		}
	}
	if ex.AllocateExplanation != "" {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintf(w, "EXPLANATION: %s\n", ex.AllocateExplanation)
	}
	if ex.MoveExplanation != "" {
		_, _ = fmt.Fprintf(w, "MOVE: %s\n", ex.MoveExplanation)
	}
	if ex.RebalanceExplanation != "" {
		_, _ = fmt.Fprintf(w, "REBALANCE: %s\n", ex.RebalanceExplanation)
	}
	if ex.Note != "" {
		_, _ = fmt.Fprintf(w, "NOTE: %s\n", ex.Note)
	}

	if len(ex.NodeDecisions) == 0 {
		return nil
	}

	_, _ = fmt.Fprintln(w)
	nw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(nw, "NODE\tDECISION\tWEIGHT\tDECIDER\tRULING\tEXPLANATION")
		for _, d := range ex.NodeDecisions {
			if len(d.Deciders) == 0 {
				_, _ = fmt.Fprintf(nw, "%s\t%s\t%d\t-\t-\t-\n",
					d.NodeName, d.NodeDecision, d.WeightRanking)
				continue
			}
			for _, dd := range d.Deciders {
				_, _ = fmt.Fprintf(nw, "%s\t%s\t%d\t%s\t%s\t%s\n",
					d.NodeName, d.NodeDecision, d.WeightRanking,
					dd.Decider, dd.Decision, dd.Explanation)
			}
		}
	} else {
		_, _ = fmt.Fprintln(nw, "NODE\tDECISION\tDECIDERS")
		for _, d := range ex.NodeDecisions {
			_, _ = fmt.Fprintf(nw, "%s\t%s\t%s\n",
				d.NodeName, d.NodeDecision, summariseDeciders(d.Deciders))
		}
	}
	return nw.Flush()
}

// summariseDeciders compresses a node's deciders list into a single
// cell for the default table. Only NO/THROTTLE rulings get surfaced —
// YES rulings are noise unless the operator passed --include-yes.
// Empty or all-yes lists render "-" so the column stays grep-friendly.
func summariseDeciders(ds []types.AllocationDecider) string {
	if len(ds) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(ds))
	for _, d := range ds {
		if strings.EqualFold(d.Decision, "YES") {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", d.Decider, strings.ToLower(d.Decision)))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func indicesTable(w io.Writer, indices []types.Index, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "NAME\tHEALTH\tSTATUS\tPRI\tREP\tDOCS\tDELETED\tSTORE\tPRI_STORE\tUUID\tCREATED")
	} else {
		_, _ = fmt.Fprintln(tw, "NAME\tHEALTH\tSTATUS\tPRI\tREP\tDOCS\tSTORE")
	}
	for _, ix := range indices {
		health := ix.Health
		if health == "" {
			health = "-"
		}
		if wide {
			uuid := ix.UUID
			if uuid == "" {
				uuid = "-"
			}
			created := formatCreation(ix.CreationDateMillis)
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%s\t%s\t%s\t%s\n",
				ix.Name, health, ix.Status,
				ix.Primaries, ix.Replicas,
				ix.DocsCount, ix.DocsDeleted,
				humanBytes(ix.StoreBytes), humanBytes(ix.PrimaryStoreBytes),
				uuid, created,
			)
		} else {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%s\n",
				ix.Name, health, ix.Status,
				ix.Primaries, ix.Replicas,
				ix.DocsCount, humanBytes(ix.StoreBytes),
			)
		}
	}
	return tw.Flush()
}

// formatCreation renders an epoch-millis creation timestamp as RFC3339.
// Zero (unknown) renders as "-" so the wide table stays grep-friendly.
func formatCreation(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

func shardsTable(w io.Writer, shards []types.Shard, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	// REASON is appended to the default table only when at least one row
	// actually has a reason — keeps the happy-path view tight, but makes
	// `ops shards --unassigned` self-explanatory without -o wide.
	showReason := wide
	if !showReason {
		for _, s := range shards {
			if s.UnassignedReason != "" {
				showReason = true
				break
			}
		}
	}

	switch {
	case wide:
		_, _ = fmt.Fprintln(tw, "INDEX\tSHARD\tROLE\tSTATE\tDOCS\tSTORE\tSTORE_BYTES\tNODE\tIP\tREASON\tSINCE")
	case showReason:
		_, _ = fmt.Fprintln(tw, "INDEX\tSHARD\tROLE\tSTATE\tSTORE\tNODE\tREASON")
	default:
		_, _ = fmt.Fprintln(tw, "INDEX\tSHARD\tROLE\tSTATE\tSTORE\tNODE")
	}

	for _, s := range shards {
		role := "r"
		if s.Primary {
			role = "p"
		}
		node := s.Node
		if node == "" {
			node = "-"
		}
		reason := s.UnassignedReason
		if reason == "" {
			reason = "-"
		}
		switch {
		case wide:
			ip := s.IP
			if ip == "" {
				ip = "-"
			}
			since := s.UnassignedFor
			if since == "" {
				since = "-"
			}
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%d\t%s\t%d\t%s\t%s\t%s\t%s\n",
				s.Index, s.Shard, role, s.State,
				s.Docs, humanBytes(s.StoreBytes), s.StoreBytes, node, ip, reason, since)
		case showReason:
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
				s.Index, s.Shard, role, s.State, humanBytes(s.StoreBytes), node, reason)
		default:
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n",
				s.Index, s.Shard, role, s.State, humanBytes(s.StoreBytes), node)
		}
	}
	return tw.Flush()
}

// humanBytes formats byte counts in the same kubectl-ish style used by
// the upstream _cat/shards default output: one decimal place, IEC units.
// A zero-byte shard (unassigned, empty, or system) renders as "0b".
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%db", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cb", float64(n)/float64(div), "kmgtpe"[exp])
}

func healthTable(w io.Writer, h types.ClusterHealth, info types.ClusterInfo, wide bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if wide {
		_, _ = fmt.Fprintln(tw, "NAME\tSTATUS\tDIALECT\tVERSION\tNODES\tDATA_NODES\tSHARDS\tPRIMARIES\tRELOC\tINIT\tUNASSIGNED\tPENDING\tACTIVE%")
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%.1f\n",
			h.ClusterName, h.Status, info.Dialect, info.Version,
			h.NumberOfNodes, h.NumberOfDataNodes,
			h.ActiveShards, h.ActivePrimaryShards,
			h.RelocatingShards, h.InitializingShards, h.UnassignedShards,
			h.NumberOfPendingTasks, h.ActiveShardsPercentAsNumber,
		)
	} else {
		_, _ = fmt.Fprintln(tw, "NAME\tSTATUS\tNODES\tDATA_NODES\tSHARDS\tUNASSIGNED\tPENDING")
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%d\n",
			h.ClusterName, h.Status,
			h.NumberOfNodes, h.NumberOfDataNodes,
			h.ActiveShards, h.UnassignedShards, h.NumberOfPendingTasks,
		)
	}
	return tw.Flush()
}
