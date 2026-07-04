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

	// issueUpdateMutation replaces the issue's state and label set.
	issueUpdateMutation = `
mutation IssueUpdate($id: ID!, $stateId: ID, $labelIds: [ID!]) {
  issueUpdate(id: $id, input: { stateId: $stateId, labelIds: $labelIds }) {
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
mutation CommentCreate($issueId: ID!, $body: String!) {
  commentCreate(input: { issueId: $issueId, body: $body }) {
    success
  }
}
`

	// issueLabelCreateMutation creates a label inside a team.
	issueLabelCreateMutation = `
mutation IssueLabelCreate($name: String!, $teamId: ID!) {
  issueLabelCreate(input: { name: $name, teamId: $teamId }) {
    success
    issueLabel {
      id
      name
    }
  }
}
`

	// issueCreateMutation creates a new issue.
	issueCreateMutation = `
mutation IssueCreate($teamId: ID!, $title: String!, $description: String, $labelIds: [ID!]) {
  issueCreate(input: { teamId: $teamId, title: $title, description: $description, labelIds: $labelIds }) {
    success
    issue {
      id
      identifier
      url
    }
  }
}
`
)
