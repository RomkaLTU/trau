package tracker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const relHierarchyReverse = "System.LinkTypes.Hierarchy-Reverse"

// azureWriteServer stands in for the organization on the hub's write path: it
// serves the backlog configuration every create consults and answers each filed
// work item with the next id, recording the route and the ops it was sent.
func azureWriteServer(t *testing.T) (Writer, *[]recordedPatch) {
	t.Helper()
	patches := &[]recordedPatch{}
	next := 6711
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveAzureWork(w, r) {
			return
		}
		body, _ := io.ReadAll(r.Body)
		var ops []recordedOp
		_ = json.Unmarshal(body, &ops)
		*patches = append(*patches, recordedPatch{path: r.URL.Path, ops: ops})
		next++
		_, _ = w.Write([]byte(`{"id":` + strconv.Itoa(next) + `}`))
	}))
	t.Cleanup(srv.Close)

	writer, err := NewWriter("azure", Config{BaseURL: srv.URL, APIKey: "pat", Team: "Contoso"})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	return writer, patches
}

// relation returns the id of the work item the patch links to under rel, or 0
// when it carries no such relation.
func (p recordedPatch) relation(rel string) string {
	for _, op := range p.ops {
		value, ok := op.Value.(map[string]any)
		if !ok || op.Path != "/relations/-" || value["rel"] != rel {
			continue
		}
		url, _ := value["url"].(string)
		return url[strings.LastIndex(url, "/")+1:]
	}
	return ""
}

// The one shape trau files on an Azure board: a requirement-level work item — the
// project's own default type — nested under a Feature the board already had, with
// each of its slices filed as a Task beneath it. Nothing an apply creates ever
// sits above requirement level.
func TestAzureWriterFilesRequirementWithTaskChildren(t *testing.T) {
	writer, patches := azureWriteServer(t)
	ctx := context.Background()

	story, err := writer.CreateIssue(ctx, IssueDraft{
		Title:       "Sync the board",
		Description: "Mirror the work items",
		Labels:      []string{"ready-for-agent"},
		Parent:      "4",
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if _, err := writer.CreateIssue(ctx, IssueDraft{
		Title:  "Land the WIQL client",
		Parent: story.Identifier,
		Shape:  DraftSlice,
	}); err != nil {
		t.Fatalf("CreateIssue slice: %v", err)
	}

	if story.Identifier != "6712" || !strings.HasSuffix(story.URL, "/Contoso/_workitems/edit/6712") {
		t.Errorf("created = %q at %q, want 6712 and the board's edit link", story.Identifier, story.URL)
	}
	if len(*patches) != 2 {
		t.Fatalf("filed %d work items, want 2", len(*patches))
	}

	parent, child := (*patches)[0], (*patches)[1]
	if want := "/Contoso/_apis/wit/workitems/$User Story"; parent.path != want {
		t.Errorf("parent filed at %q, want %q", parent.path, want)
	}
	if parent.value("System.Title") != "Sync the board" || parent.value("System.Tags") != "ready-for-agent" {
		t.Errorf("parent ops = %+v", parent.ops)
	}
	if got := parent.relation(relHierarchyReverse); got != "4" {
		t.Errorf("parent hangs off %q, want the picked Feature 4", got)
	}
	if want := "/Contoso/_apis/wit/workitems/$Task"; child.path != want {
		t.Errorf("slice filed at %q, want %q", child.path, want)
	}
	if got := child.relation(relHierarchyReverse); got != "6712" {
		t.Errorf("slice hangs off %q, want the story just filed", got)
	}
}

// The level trau files at is enforced here, not just in the picker: a create that
// pins a portfolio-backlog type is refused and nothing is filed, so bypassing the UI
// still cannot put an Epic or a Feature on the board (ADR 0031).
func TestAzureWriterRefusesPortfolioLevelType(t *testing.T) {
	writer, patches := azureWriteServer(t)

	_, err := writer.CreateIssue(context.Background(), IssueDraft{Title: "Platform rebuild", Type: "Feature"})
	if err == nil || !strings.Contains(err.Error(), `"Feature" is not a requirement-level work-item type`) {
		t.Fatalf("CreateIssue err = %v, want a refusal naming the level", err)
	}
	if len(*patches) != 0 {
		t.Errorf("filed %d work items, want none", len(*patches))
	}
}

// The picker offers only the types the project places at requirement level, so an
// Epic or a Feature is never on the list.
func TestAzureWriterCreatableTypesAreRequirementLevel(t *testing.T) {
	writer, _ := azureWriteServer(t)

	typer, ok := writer.(IssueTyper)
	if !ok {
		t.Fatal("azure writer does not report its creatable types")
	}
	types, err := typer.CreatableTypes(context.Background())
	if err != nil {
		t.Fatalf("CreatableTypes: %v", err)
	}
	if want := []string{"User Story", "Bug"}; !slices.Equal(types, want) {
		t.Errorf("types = %v, want %v", types, want)
	}
}
