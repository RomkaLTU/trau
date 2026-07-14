package linearapi

const (
	endpoint = "https://api.linear.app/graphql"

	// issueQuery loads everything the tracker needs for a single issue. Linear's
	// IssueFilter has no "identifier" field, so the human id (COD-493) is split into
	// its team key (COD) and number (493) and matched on those.
	issueQuery = `
query Issue($number: Float!, $teamKey: String!) {
  issues(first: 1, filter: { number: { eq: $number }, team: { key: { eq: $teamKey } } }) {
    nodes {
      id
      identifier
      title
      description
      priority
      dueDate
      state {
        id
        name
        type
      }
      team {
        id
        key
        name
      }
      project {
        id
        name
      }
      parent {
        id
        identifier
      }
      labels {
        nodes {
          id
          name
        }
      }
      children {
        nodes {
          id
          identifier
          title
          priority
          dueDate
          state {
            type
          }
          children {
            nodes {
              id
            }
          }
        }
      }
      inverseRelations(first: 50) {
        nodes {
          type
          issue {
            id
            identifier
            state {
              type
            }
          }
        }
      }
    }
  }
}
`

	// pickQuery loads candidate issues for the loop picker.
	pickQuery = `
query PickIssues($teamId: ID!, $labelName: String!) {
  issues(first: 250, filter: { team: { id: { eq: $teamId } }, labels: { name: { eq: $labelName } } }) {
    nodes {
      id
      identifier
      title
      priority
      dueDate
      state {
        id
        name
        type
      }
      project {
        id
        name
      }
      parent {
        id
        identifier
      }
      labels {
        nodes {
          id
          name
        }
      }
      children {
        nodes {
          id
        }
      }
      inverseRelations(first: 50) {
        nodes {
          type
          issue {
            id
            identifier
            state {
              type
            }
          }
        }
      }
    }
  }
}
`

	// backlogQuery loads a team's full issue set for the backlog board — every
	// issue, not just the ready-labelled queue — with the fields the board needs:
	// workflow state, project (for the owned-project filter), parent (epic), labels,
	// and whether the issue is itself a parent. It pages with a cursor so the whole
	// backlog is returned, not just the first page.
	backlogQuery = `
query Backlog($teamId: ID!, $after: String) {
  issues(first: 100, after: $after, filter: { team: { id: { eq: $teamId } } }) {
    pageInfo {
      hasNextPage
      endCursor
    }
    nodes {
      identifier
      title
      state {
        name
        type
      }
      project {
        name
      }
      parent {
        identifier
      }
      labels {
        nodes {
          id
          name
        }
      }
      children {
        nodes {
          id
        }
      }
    }
  }
}
`

	// syncQuery pulls a Project's (or a whole team's) issues with the full content
	// the hub's issue store keeps: description, comments, and timestamps. The
	// filter is passed as an IssueFilter variable so the same query serves a
	// project-scoped and a team-scoped pull, and it pages the cursor to the end.
	syncQuery = `
query SyncIssues($filter: IssueFilter!, $after: String) {
  issues(first: 100, after: $after, filter: $filter) {
    pageInfo {
      hasNextPage
      endCursor
    }
    nodes {
      id
      identifier
      title
      description
      priority
      dueDate
      url
      createdAt
      updatedAt
      state {
        name
        type
      }
      project {
        id
        name
      }
      parent {
        identifier
      }
      labels {
        nodes {
          name
        }
      }
      children {
        nodes {
          id
        }
      }
      comments {
        nodes {
          id
          body
          createdAt
          updatedAt
          user {
            name
          }
        }
      }
    }
  }
}
`

	// identifiersQuery pulls only the human identifier of a Project's (or team's)
	// issues — the cheap full set a reconciliation sweep diffs against the local
	// store. The filter is an IssueFilter variable so it serves a project-scoped
	// and a team-scoped pull, and it pages the cursor to the end.
	identifiersQuery = `
query ProjectIdentifiers($filter: IssueFilter!, $after: String) {
  issues(first: 250, after: $after, filter: $filter) {
    pageInfo {
      hasNextPage
      endCursor
    }
    nodes {
      identifier
    }
  }
}
`

	// teamsQuery lists teams the key can see.
	teamsQuery = `
query Teams {
  teams {
    nodes {
      id
      key
      name
    }
  }
}
`

	// projectsByNameQuery resolves a project's node id from its name, so the create
	// path can place an issue inside the bound PROJECT.
	projectsByNameQuery = `
query ProjectsByName($name: String!) {
  projects(filter: { name: { eq: $name } }) {
    nodes {
      id
      name
    }
  }
}
`

	// workflowStatesQuery maps status names to state IDs for a team.
	workflowStatesQuery = `
query WorkflowStates($teamId: ID!) {
  workflowStates(filter: { team: { id: { eq: $teamId } } }) {
    nodes {
      id
      name
      type
    }
  }
}
`

	// issueUpdateMutation replaces the issue's state and label set. Linear's
	// mutation arguments and input fields are String-typed, not ID-typed — an
	// ID-typed variable fails GraphQL validation ("Variable of type ID! used in
	// position expecting type String!"); only filter comparators take ID.
	issueUpdateMutation = `
mutation IssueUpdate($id: String!, $stateId: String, $labelIds: [String!]) {
  issueUpdate(id: $id, input: { stateId: $stateId, labelIds: $labelIds }) {
    success
    issue {
      id
      identifier
    }
  }
}
`

	// issueDescriptionMutation replaces the issue's description. The same String!
	// typing note as issueUpdateMutation applies.
	issueDescriptionMutation = `
mutation IssueUpdateDescription($id: String!, $description: String!) {
  issueUpdate(id: $id, input: { description: $description }) {
    success
    issue {
      id
      identifier
    }
  }
}
`

	// commentCreateMutation adds a comment to an issue.
	commentCreateMutation = `
mutation CommentCreate($issueId: String!, $body: String!) {
  commentCreate(input: { issueId: $issueId, body: $body }) {
    success
  }
}
`

	// issueLabelCreateMutation creates a label inside a team.
	issueLabelCreateMutation = `
mutation IssueLabelCreate($name: String!, $teamId: String!) {
  issueLabelCreate(input: { name: $name, teamId: $teamId }) {
    success
    issueLabel {
      id
      name
    }
  }
}
`

	// issueCreateMutation creates a new issue. parentId nests it under an epic and
	// projectId places it in a project; both are optional and omitted when empty.
	issueCreateMutation = `
mutation IssueCreate($teamId: String!, $title: String!, $description: String, $labelIds: [String!], $parentId: String, $projectId: String) {
  issueCreate(input: { teamId: $teamId, title: $title, description: $description, labelIds: $labelIds, parentId: $parentId, projectId: $projectId }) {
    success
    issue {
      id
      identifier
      url
    }
  }
}
`

	// issueRelationCreateMutation links two issues. type "blocks" means issueId
	// blocks relatedIssueId, so relatedIssueId reads issueId as a blocker in its
	// inverseRelations — the direction blockers() interprets. The same String!
	// typing note as issueUpdateMutation applies.
	issueRelationCreateMutation = `
mutation IssueRelationCreate($issueId: String!, $relatedIssueId: String!, $type: String!) {
  issueRelationCreate(input: { issueId: $issueId, relatedIssueId: $relatedIssueId, type: $type }) {
    success
  }
}
`

	// documentCreateMutation creates a project document from markdown content.
	documentCreateMutation = `
mutation DocumentCreate($projectId: String!, $title: String!, $content: String!) {
  documentCreate(input: { projectId: $projectId, title: $title, content: $content }) {
    success
    document {
      id
      url
    }
  }
}
`
)
