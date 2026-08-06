package pipeline

import (
	"context"

	"github.com/RomkaLTU/trau/internal/forge/bitbucketapi"
)

// BitbucketCredentials is the Atlassian account a Bitbucket Cloud repository is
// delivered as: BITBUCKET_EMAIL and BITBUCKET_API_TOKEN, used as HTTP Basic auth
// exactly as the Jira pair is. The credential belongs to an account rather than
// to a repository, so one set covers every Bitbucket repository a run ships to,
// a Folder repo's children included.
type BitbucketCredentials struct {
	Email    string
	APIToken string
}

// DeliveryReady is the pre-spend readiness a Delivery may answer for itself: the
// reason it cannot open a pull request for this repository, or "" when it can. It
// exists so a missing credential is stated before a ticket is picked rather than
// discovered at PR time, with the build, handoff and verify agents already paid
// for. A Delivery that does not implement it is assumed ready — which is how the
// GitHub path stays exactly as it was, with `gh auth` checked by doctor.
type DeliveryReady interface {
	NotReady() string
}

// NewDelivery picks the backend one repository's pull requests go through, read
// off the forge its own remote names — declared overrides the remote, the same
// precedence forge.Resolve applies everywhere else. A repository on a forge with
// no delivery backend falls back to the GitHub one, which is what the run's own
// forge refusal has already stopped before anything is spent.
func NewDelivery(ctx context.Context, repo, declared, remote string, bb BitbucketCredentials) Delivery {
	if client, ok := bitbucketapi.ClientFor(ctx, repo, declared, remote, bb.Email, bb.APIToken); ok {
		return Bitbucket{Client: client}
	}
	return ExecGitHub{Repo: repo}
}

// NotReady names the missing half of a Bitbucket delivery: the API token pair, or
// a remote whose workspace and repository slug could not be read.
func (b Bitbucket) NotReady() string {
	switch {
	case b.Client == nil:
		return "no Bitbucket client is configured for it"
	case b.Client.Workspace() == "" || b.Client.RepoSlug() == "":
		return "its bitbucket.org remote names no workspace and repository — check the remote URL"
	case !b.Client.Enabled():
		return "BITBUCKET_EMAIL and BITBUCKET_API_TOKEN are not both set, and Bitbucket Cloud accepts no other credential — mint a token at " + bitbucketapi.TokenHelpURL
	}
	return ""
}
