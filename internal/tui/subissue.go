package tui

// SubIssue is a lightweight identifier+title pair shown in the epic preview.
// Done marks a child the tracker has already finished, so the list can flag work
// that will not run.
type SubIssue struct {
	ID          string
	Title       string
	Done        bool
	HasChildren bool
}
