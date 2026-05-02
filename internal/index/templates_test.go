package index_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/esops-dev/esops-go/pkg/client"
	"github.com/esops-dev/esops-go/internal/index"
	"github.com/esops-dev/esops-go/pkg/types"
)

// fakeTemplateInspector returns canned templates and records the
// names slice the business layer asked for.
type fakeTemplateInspector struct {
	got  []string
	rows []types.IndexTemplate
	err  error
}

func (f *fakeTemplateInspector) GetTemplates(_ context.Context, names []string) ([]types.IndexTemplate, error) {
	f.got = append([]string(nil), names...)
	return f.rows, f.err
}

func (f *fakeTemplateInspector) GetTemplate(_ context.Context, name string) (types.IndexTemplate, error) {
	f.got = append([]string(nil), name)
	if f.err != nil {
		return types.IndexTemplate{}, f.err
	}
	for _, t := range f.rows {
		if t.Name == name {
			return t, nil
		}
	}
	return types.IndexTemplate{}, fmt.Errorf("%w: index template %q", client.ErrNotFound, name)
}

// fakeTemplateUpdater records every Put/Delete and returns canned
// responses.
type fakeTemplateUpdater struct {
	puts    []types.IndexTemplatePutRequest
	deletes []types.IndexTemplateDeleteRequest
	putResp types.IndexTemplatePutResult
	delResp types.IndexTemplateDeleteResult
	err     error
}

func (f *fakeTemplateUpdater) PutTemplate(_ context.Context, req types.IndexTemplatePutRequest) (types.IndexTemplatePutResult, error) {
	f.puts = append(f.puts, req)
	if f.err != nil {
		return types.IndexTemplatePutResult{}, f.err
	}
	r := f.putResp
	if r.Action == "" {
		r.Action = "template-put"
	}
	if r.Name == "" {
		r.Name = req.Name
	}
	return r, nil
}

func (f *fakeTemplateUpdater) DeleteTemplate(_ context.Context, req types.IndexTemplateDeleteRequest) (types.IndexTemplateDeleteResult, error) {
	f.deletes = append(f.deletes, req)
	if f.err != nil {
		return types.IndexTemplateDeleteResult{}, f.err
	}
	r := f.delResp
	if r.Action == "" {
		r.Action = "template-delete"
	}
	if r.Name == "" {
		r.Name = req.Name
	}
	return r, nil
}

func sampleTemplates() []types.IndexTemplate {
	return []types.IndexTemplate{
		// Out-of-order on purpose — ListTemplates sorts client-side.
		{Name: "logs-v2", IndexPatterns: []string{"logs-*"}, Priority: 100},
		{Name: "events", IndexPatterns: []string{"events-*"}},
		{Name: "audit", IndexPatterns: []string{"audit-*"}, Priority: 200},
	}
}

func TestListTemplates_sortsByName(t *testing.T) {
	ti := &fakeTemplateInspector{rows: sampleTemplates()}
	out, err := index.ListTemplates(context.Background(), ti, index.TemplateListOptions{})
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(out) != 3 || out[0].Name != "audit" || out[1].Name != "events" || out[2].Name != "logs-v2" {
		t.Errorf("sort order: %+v", names(out))
	}
}

func TestListTemplates_passesNamesThrough(t *testing.T) {
	ti := &fakeTemplateInspector{rows: nil}
	_, err := index.ListTemplates(context.Background(), ti, index.TemplateListOptions{
		Names: []string{"logs-v2", "events"},
	})
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(ti.got) != 2 || ti.got[0] != "logs-v2" || ti.got[1] != "events" {
		t.Errorf("names forwarded = %v", ti.got)
	}
}

func TestListTemplates_propagatesError(t *testing.T) {
	sentinel := errors.New("boom")
	ti := &fakeTemplateInspector{err: sentinel}
	_, err := index.ListTemplates(context.Background(), ti, index.TemplateListOptions{})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}

func TestGetTemplate_requiresName(t *testing.T) {
	ti := &fakeTemplateInspector{rows: sampleTemplates()}
	_, err := index.GetTemplate(context.Background(), ti, "")
	if err == nil || !strings.Contains(err.Error(), "--name") {
		t.Errorf("expected error mentioning --name, got: %v", err)
	}
}

func TestGetTemplate_rejectsPattern(t *testing.T) {
	ti := &fakeTemplateInspector{rows: sampleTemplates()}
	_, err := index.GetTemplate(context.Background(), ti, "logs-*")
	if err == nil || !strings.Contains(err.Error(), "literal") {
		t.Errorf("expected literal-name rejection, got: %v", err)
	}
}

func TestGetTemplate_returnsMatchingEntry(t *testing.T) {
	ti := &fakeTemplateInspector{rows: sampleTemplates()}
	got, err := index.GetTemplate(context.Background(), ti, "events")
	if err != nil {
		t.Fatalf("GetTemplate: %v", err)
	}
	if got.Name != "events" {
		t.Errorf("got %q, want events", got.Name)
	}
}

func TestGetTemplate_notFound(t *testing.T) {
	ti := &fakeTemplateInspector{rows: sampleTemplates()}
	_, err := index.GetTemplate(context.Background(), ti, "missing")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got: %v", err)
	}
}

func TestPutTemplate_validatesName(t *testing.T) {
	tu := &fakeTemplateUpdater{}
	_, err := index.PutTemplate(context.Background(), tu, index.TemplatePutOptions{
		Body: []byte(`{"index_patterns":["logs-*"]}`),
	})
	if err == nil || !strings.Contains(err.Error(), "--name") {
		t.Errorf("expected --name error, got: %v", err)
	}
}

func TestPutTemplate_rejectsPatternName(t *testing.T) {
	tu := &fakeTemplateUpdater{}
	_, err := index.PutTemplate(context.Background(), tu, index.TemplatePutOptions{
		Name: "logs-*",
		Body: []byte(`{"index_patterns":["logs-*"]}`),
	})
	if err == nil || !strings.Contains(err.Error(), "literal") {
		t.Errorf("expected literal-name error, got: %v", err)
	}
}

func TestPutTemplate_validatesBody(t *testing.T) {
	tu := &fakeTemplateUpdater{}
	cases := []struct {
		name      string
		body      []byte
		wantErr   string
	}{
		{"empty body", nil, "body is required"},
		{"non-JSON", []byte(`{ broken`), "valid JSON"},
		{"missing index_patterns", []byte(`{"priority":1}`), "index_patterns"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := index.PutTemplate(context.Background(), tu, index.TemplatePutOptions{
				Name: "logs-v2",
				Body: c.body,
			})
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("err = %v, want substring %q", err, c.wantErr)
			}
			if len(tu.puts) != 0 {
				t.Errorf("cluster contacted on validation failure: %+v", tu.puts)
			}
		})
	}
}

func TestPutTemplate_dryRun_doesNotCallCluster(t *testing.T) {
	tu := &fakeTemplateUpdater{}
	got, err := index.PutTemplate(context.Background(), tu, index.TemplatePutOptions{
		Name:   "logs-v2",
		Body:   []byte(`{"index_patterns":["logs-*"]}`),
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("PutTemplate: %v", err)
	}
	if !got.DryRun || !got.Acknowledged {
		t.Errorf("expected synthetic dry-run ack, got %+v", got)
	}
	if len(tu.puts) != 0 {
		t.Errorf("dry-run contacted cluster: %+v", tu.puts)
	}
}

func TestPutTemplate_passesRequestThrough(t *testing.T) {
	tu := &fakeTemplateUpdater{}
	body := []byte(`{"index_patterns":["logs-*"],"priority":100}`)
	_, err := index.PutTemplate(context.Background(), tu, index.TemplatePutOptions{
		Name:   "logs-v2",
		Body:   body,
		Create: true,
	})
	if err != nil {
		t.Fatalf("PutTemplate: %v", err)
	}
	if len(tu.puts) != 1 {
		t.Fatalf("expected 1 call, got %d", len(tu.puts))
	}
	if tu.puts[0].Name != "logs-v2" || !tu.puts[0].Create || string(tu.puts[0].Body) != string(body) {
		t.Errorf("request not forwarded: %+v", tu.puts[0])
	}
}

func TestDeleteTemplate_validatesName(t *testing.T) {
	tu := &fakeTemplateUpdater{}
	_, err := index.DeleteTemplate(context.Background(), tu, index.TemplateDeleteOptions{})
	if err == nil || !strings.Contains(err.Error(), "--name") {
		t.Errorf("expected --name error, got: %v", err)
	}
	_, err = index.DeleteTemplate(context.Background(), tu, index.TemplateDeleteOptions{Name: "logs-*"})
	if err == nil || !strings.Contains(err.Error(), "literal") {
		t.Errorf("expected literal-name error, got: %v", err)
	}
}

func TestDeleteTemplate_dryRun_doesNotCallCluster(t *testing.T) {
	tu := &fakeTemplateUpdater{}
	got, err := index.DeleteTemplate(context.Background(), tu, index.TemplateDeleteOptions{
		Name:   "logs-v2",
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}
	if !got.DryRun || !got.Acknowledged {
		t.Errorf("expected synthetic dry-run ack, got %+v", got)
	}
	if len(tu.deletes) != 0 {
		t.Errorf("dry-run contacted cluster: %+v", tu.deletes)
	}
}

func TestDeleteTemplate_passesRequestThrough(t *testing.T) {
	tu := &fakeTemplateUpdater{}
	_, err := index.DeleteTemplate(context.Background(), tu, index.TemplateDeleteOptions{
		Name: "logs-v2",
	})
	if err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}
	if len(tu.deletes) != 1 || tu.deletes[0].Name != "logs-v2" {
		t.Errorf("delete request not forwarded: %+v", tu.deletes)
	}
}

func TestDeleteTemplate_propagatesAdapterError(t *testing.T) {
	sentinel := errors.New("boom")
	tu := &fakeTemplateUpdater{err: sentinel}
	_, err := index.DeleteTemplate(context.Background(), tu, index.TemplateDeleteOptions{Name: "logs-v2"})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}

func names(ts []types.IndexTemplate) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}
