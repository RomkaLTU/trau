package tracker

import "testing"

// A Jira project with no "In Review" status at all.
var readyForQAWorkflow = []WorkflowOption{
	{Name: "READY FOR QA", Category: "indeterminate"},
	{Name: "To Do", Category: "new"},
	{Name: "In Progress", Category: "indeterminate"},
	{Name: "Done", Category: "done"},
}

func TestResolveStageMatchesWorkflowNames(t *testing.T) {
	cases := []struct {
		name       string
		stage      Stage
		categories []string
		options    []WorkflowOption
		want       string
	}{
		{"review lands on the workflow's QA status", StageInReview, []string{"indeterminate"}, readyForQAWorkflow, "READY FOR QA"},
		{"done lands on done", StageDone, []string{"done"}, readyForQAWorkflow, "Done"},
		{"in progress lands on in progress", StageInProgress, []string{"indeterminate"}, readyForQAWorkflow, "In Progress"},
		{"todo lands on to do", StageTodo, []string{"new"}, readyForQAWorkflow, "To Do"},
		{
			"spelling differences fold away",
			StageInReview,
			[]string{"indeterminate"},
			[]WorkflowOption{{Name: "ready-for-qa", Category: "indeterminate"}},
			"ready-for-qa",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i, ok := ResolveStage(tc.stage, "", tc.categories, tc.options)
			if !ok {
				t.Fatalf("ResolveStage(%s) resolved nothing", tc.stage)
			}
			if got := tc.options[i].Name; got != tc.want {
				t.Errorf("ResolveStage(%s) = %q, want %q", tc.stage, got, tc.want)
			}
		})
	}
}

// When no name matches, the category decides — and within a category the status
// that reads like the stage wins over whatever happens to come first.
func TestResolveStageFallsBackToCategory(t *testing.T) {
	options := []WorkflowOption{
		{Name: "Blocked", Category: "indeterminate"},
		{Name: "Peer Testing", Category: "indeterminate"},
		{Name: "Shipped", Category: "done"},
	}
	i, ok := ResolveStage(StageInReview, "", []string{"indeterminate"}, options)
	if !ok || options[i].Name != "Peer Testing" {
		t.Errorf("ResolveStage(in-review) = %q (%v), want Peer Testing", options[i].Name, ok)
	}
}

// A Done category holds the abandonment statuses too. Closing a delivered ticket
// as "Won't Do" would be worse than not closing it at all.
func TestResolveStageNeverSettlesForAnAbandonment(t *testing.T) {
	both := []WorkflowOption{
		{Name: "Won't Do", Category: "done"},
		{Name: "Shipped", Category: "done"},
	}
	i, ok := ResolveStage(StageDone, "", []string{"done"}, both)
	if !ok || both[i].Name != "Shipped" {
		t.Errorf("ResolveStage(done) = %q (%v), want Shipped", both[i].Name, ok)
	}

	only := []WorkflowOption{{Name: "Cancelled", Category: "done"}}
	if i, ok := ResolveStage(StageDone, "", []string{"done"}, only); ok {
		t.Errorf("ResolveStage(done) = %q, want nothing on a workflow that only offers cancellation", only[i].Name)
	}
}

func TestResolveStageOverride(t *testing.T) {
	i, ok := ResolveStage(StageInReview, "In Progress", []string{"indeterminate"}, readyForQAWorkflow)
	if !ok || readyForQAWorkflow[i].Name != "In Progress" {
		t.Errorf("ResolveStage with an override = %q (%v), want In Progress", readyForQAWorkflow[i].Name, ok)
	}

	// An override the workflow does not offer must not strand the stage.
	i, ok = ResolveStage(StageInReview, "Awaiting Signoff", []string{"indeterminate"}, readyForQAWorkflow)
	if !ok || readyForQAWorkflow[i].Name != "READY FOR QA" {
		t.Errorf("ResolveStage with a stale override = %q (%v), want READY FOR QA", readyForQAWorkflow[i].Name, ok)
	}
}

// The stock Azure DevOps process templates each name the same stages differently,
// and Scrum and Basic have no Resolved category for review at all.
func TestResolveStageAcrossAzureTemplates(t *testing.T) {
	agile := []WorkflowOption{
		{Name: "New", Category: "Proposed"},
		{Name: "Active", Category: "InProgress"},
		{Name: "Resolved", Category: "Resolved"},
		{Name: "Closed", Category: "Completed"},
		{Name: "Removed", Category: "Removed"},
	}
	scrum := []WorkflowOption{
		{Name: "New", Category: "Proposed"},
		{Name: "Approved", Category: "Proposed"},
		{Name: "Committed", Category: "InProgress"},
		{Name: "Done", Category: "Completed"},
		{Name: "Removed", Category: "Removed"},
	}
	basic := []WorkflowOption{
		{Name: "To Do", Category: "Proposed"},
		{Name: "Doing", Category: "InProgress"},
		{Name: "Done", Category: "Completed"},
	}
	cases := []struct {
		name    string
		stage   Stage
		options []WorkflowOption
		want    string
	}{
		{"agile in progress", StageInProgress, agile, "Active"},
		{"agile in review", StageInReview, agile, "Resolved"},
		{"agile done", StageDone, agile, "Closed"},
		{"agile to do", StageTodo, agile, "New"},
		{"scrum in progress", StageInProgress, scrum, "Committed"},
		{"scrum in review has no resolved category", StageInReview, scrum, "Committed"},
		{"scrum done", StageDone, scrum, "Done"},
		{"scrum to do", StageTodo, scrum, "New"},
		{"basic in progress", StageInProgress, basic, "Doing"},
		{"basic in review has no resolved category", StageInReview, basic, "Doing"},
		{"basic done", StageDone, basic, "Done"},
		{"basic to do", StageTodo, basic, "To Do"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i, ok := ResolveStage(tc.stage, "", azureCategories(tc.stage), tc.options)
			if !ok {
				t.Fatalf("ResolveStage(%s) resolved nothing", tc.stage)
			}
			if got := tc.options[i].Name; got != tc.want {
				t.Errorf("ResolveStage(%s) = %q, want %q", tc.stage, got, tc.want)
			}
		})
	}
}

// A Linear team that renamed every state still resolves through the state types,
// which the product fixes.
func TestResolveStageAcrossRenamedLinearStates(t *testing.T) {
	states := []WorkflowOption{
		{Name: "Icebox", Category: "backlog"},
		{Name: "Up Next", Category: "unstarted"},
		{Name: "Building", Category: "started"},
		{Name: "Awaiting Review", Category: "started"},
		{Name: "Live", Category: "completed"},
		{Name: "Dropped", Category: "canceled"},
	}
	cases := []struct {
		stage Stage
		want  string
	}{
		{StageTodo, "Up Next"},
		{StageInProgress, "Building"},
		{StageInReview, "Awaiting Review"},
		{StageDone, "Live"},
	}
	for _, tc := range cases {
		t.Run(string(tc.stage), func(t *testing.T) {
			i, ok := ResolveStage(tc.stage, "", linearStateTypes(tc.stage), states)
			if !ok {
				t.Fatalf("ResolveStage(%s) resolved nothing", tc.stage)
			}
			if got := states[i].Name; got != tc.want {
				t.Errorf("ResolveStage(%s) = %q, want %q", tc.stage, got, tc.want)
			}
		})
	}
}

func TestStageConfigKey(t *testing.T) {
	cases := map[Stage]string{
		StageTodo:       "STATUS_TODO",
		StageInProgress: "STATUS_IN_PROGRESS",
		StageInReview:   "STATUS_IN_REVIEW",
		StageDone:       "STATUS_DONE",
	}
	for stage, want := range cases {
		if got := stage.ConfigKey(); got != want {
			t.Errorf("%s.ConfigKey() = %q, want %q", stage, got, want)
		}
	}
}
