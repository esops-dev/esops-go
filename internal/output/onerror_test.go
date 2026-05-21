package output_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/internal/output"
	"github.com/esops-dev/esops-go/pkg/types"
)

func TestOnErrorSummaryFooter_nilIsNoOp(t *testing.T) {
	var buf bytes.Buffer
	output.OnErrorSummaryFooter(&buf, nil, output.FormatTable, true)
	if buf.Len() != 0 {
		t.Errorf("expected no output for nil summary, got %q", buf.String())
	}
}

func TestOnErrorSummaryFooter_jsonAndYamlAreNoOp(t *testing.T) {
	sum := &types.OnErrorSummary{Policy: types.OnErrorSkip, Succeeded: 1}
	for _, f := range []output.Format{output.FormatJSON, output.FormatYAML, output.FormatNDJSON} {
		var buf bytes.Buffer
		output.OnErrorSummaryFooter(&buf, sum, f, true)
		if buf.Len() != 0 {
			t.Errorf("format %q: expected no output (JSON/YAML carry summary in result struct), got %q",
				f, buf.String())
		}
	}
}

func TestOnErrorSummaryFooter_summaryLineAlwaysShown(t *testing.T) {
	sum := &types.OnErrorSummary{
		Policy:    types.OnErrorSkip,
		Succeeded: 2,
		Failed:    1,
		Items: []types.OnErrorItemResult{
			{Name: "a", Status: types.OnErrorStatusSucceeded},
			{Name: "b", Status: types.OnErrorStatusFailed, Error: "boom"},
			{Name: "c", Status: types.OnErrorStatusSucceeded},
		},
	}
	var buf bytes.Buffer
	output.OnErrorSummaryFooter(&buf, sum, output.FormatTable, false)
	out := buf.String()
	if !strings.Contains(out, "ON-ERROR") {
		t.Errorf("missing ON-ERROR header: %q", out)
	}
	if !strings.Contains(out, "policy=skip") {
		t.Errorf("missing policy=skip: %q", out)
	}
	if !strings.Contains(out, "succeeded=2") || !strings.Contains(out, "failed=1") {
		t.Errorf("missing counts: %q", out)
	}
	// verbose=false: per-item rows must NOT be shown.
	if strings.Contains(out, "STATUS") {
		t.Errorf("non-verbose output included per-item table: %q", out)
	}
}

func TestOnErrorSummaryFooter_verboseShowsPerItem(t *testing.T) {
	sum := &types.OnErrorSummary{
		Policy:    types.OnErrorSkip,
		Succeeded: 1,
		Failed:    1,
		Items: []types.OnErrorItemResult{
			{Name: "a", Status: types.OnErrorStatusSucceeded},
			{Name: "b", Status: types.OnErrorStatusFailed, Error: "rejected"},
		},
	}
	var buf bytes.Buffer
	output.OnErrorSummaryFooter(&buf, sum, output.FormatTable, true)
	out := buf.String()
	if !strings.Contains(out, "STATUS") {
		t.Errorf("verbose output missing STATUS header: %q", out)
	}
	if !strings.Contains(out, "succeeded") || !strings.Contains(out, "failed") {
		t.Errorf("verbose missing status rows: %q", out)
	}
	if !strings.Contains(out, "rejected") {
		t.Errorf("verbose missing error message: %q", out)
	}
}

func TestOnErrorSummaryFooter_rollbackCountsAppearWhenNonZero(t *testing.T) {
	sum := &types.OnErrorSummary{
		Policy:         types.OnErrorRollback,
		Failed:         1,
		RolledBack:     2,
		RollbackFailed: 1,
	}
	var buf bytes.Buffer
	output.OnErrorSummaryFooter(&buf, sum, output.FormatTable, false)
	out := buf.String()
	for _, want := range []string{"rolled-back=2", "rollback-failed=1"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in summary: %q", want, out)
		}
	}
}

func TestOnErrorSummaryFooter_omitsZeroRollbackCounts(t *testing.T) {
	sum := &types.OnErrorSummary{Policy: types.OnErrorSkip, Succeeded: 3}
	var buf bytes.Buffer
	output.OnErrorSummaryFooter(&buf, sum, output.FormatTable, false)
	out := buf.String()
	if strings.Contains(out, "rolled-back=") || strings.Contains(out, "rollback-failed=") {
		t.Errorf("zero rollback counts should be omitted: %q", out)
	}
}
