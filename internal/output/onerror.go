package output

import (
	"fmt"
	"io"

	"github.com/esops-dev/esops-go/pkg/types"
)

// OnErrorSummaryFooter writes a "ON-ERROR" summary block to w for
// table / wide formats. JSON / YAML carry the per-item records in the
// result struct itself (the OnError field is encoded directly), so
// this helper is a no-op for those formats.
//
// The summary line is always shown when sum is non-nil. Per-item
// status is shown only when verbose is true — the default keeps
// regular table output uncluttered for the common "everything
// succeeded" case while still surfacing the totals.
//
// Counts are read directly from sum so the renderer doesn't have to
// walk Items. Failed / RolledBack / RollbackFailed are omitted from
// the summary line when zero, matching the JSON `omitempty` pattern
// the result types use.
func OnErrorSummaryFooter(w io.Writer, sum *types.OnErrorSummary, f Format, verbose bool) {
	if sum == nil {
		return
	}
	switch f {
	case FormatTable, FormatWide, "":
	default:
		return
	}

	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "ON-ERROR")
	_, _ = fmt.Fprintf(w, "  policy=%s succeeded=%d failed=%d",
		sum.Policy, sum.Succeeded, sum.Failed,
	)
	if sum.RolledBack > 0 {
		_, _ = fmt.Fprintf(w, " rolled-back=%d", sum.RolledBack)
	}
	if sum.RollbackFailed > 0 {
		_, _ = fmt.Fprintf(w, " rollback-failed=%d", sum.RollbackFailed)
	}
	_, _ = fmt.Fprintln(w)

	if !verbose || len(sum.Items) == 0 {
		return
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "  STATUS              ITEM")
	for _, it := range sum.Items {
		if it.Error != "" {
			_, _ = fmt.Fprintf(w, "  %-18s  %s  (%s)\n", it.Status, it.Name, it.Error)
			continue
		}
		_, _ = fmt.Fprintf(w, "  %-18s  %s\n", it.Status, it.Name)
	}
}
