package webserver

import (
	"context"
	"fmt"
	"io"

	"github.com/RomkaLTU/trau/internal/attachfile"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
)

// attachmentBytes returns an attachment's cached bytes, downloading them from the
// tracker first when the row has never been read — the same lazy fetch the serve
// route performs, without the HTTP round trip.
func (s *Server) attachmentBytes(ctx context.Context, repo registry.Repo, id int64) ([]byte, error) {
	att, found, err := s.stores.Attachments().Get(repo.Root, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("unknown attachment %d", id)
	}
	if att, err = s.cachedAttachment(ctx, repo, att); err != nil {
		return nil, err
	}
	f, err := s.stores.Attachments().Blobs().Open(att.SHA256)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if err := s.stores.Attachments().MarkServed(att.ID); err != nil {
		logger.Verbosef("mark attachment %d served: %v", att.ID, err)
	}
	return io.ReadAll(f)
}

// materializeIssueAttachments writes an issue's files to disk so a grilling child
// can look at a screenshot while it interviews. Best-effort throughout: a file
// that will not fetch is described as unavailable in the prompt rather than
// stalling the interview.
func (s *Server) materializeIssueAttachments(ctx context.Context, repo registry.Repo, identifier string) []attachfile.File {
	rows, err := s.stores.Attachments().ForIssue(repo.Root, identifier)
	if err != nil {
		logger.Verbosef("attachments %s %s: list for grill: %v", repo.Root, identifier, err)
		return nil
	}
	return s.materializeAttachments(ctx, repo, identifier, rows)
}

// materializeAttachmentIDs does the same for the rows named by ids — the images a
// user pasted into an answer, which are bound to no issue until the session lands.
func (s *Server) materializeAttachmentIDs(ctx context.Context, repo registry.Repo, ticket string, ids []int64) []attachfile.File {
	rows := make([]hubstore.Attachment, 0, len(ids))
	for _, id := range ids {
		att, found, err := s.stores.Attachments().Get(repo.Root, id)
		if err != nil || !found {
			continue
		}
		rows = append(rows, att)
	}
	return s.materializeAttachments(ctx, repo, ticket, rows)
}

func (s *Server) materializeAttachments(ctx context.Context, repo registry.Repo, ticket string, rows []hubstore.Attachment) []attachfile.File {
	refs := make([]attachfile.Ref, 0, len(rows))
	for _, att := range rows {
		refs = append(refs, attachfile.Ref{
			ID:        att.ID,
			Filename:  att.Filename,
			MimeType:  att.MimeType,
			Size:      att.SizeBytes,
			IsImage:   hubstore.AttachmentIsImage(att.MimeType),
			SourceURL: att.SourceURL,
		})
	}
	return attachfile.Materialize(ctx, ticket, refs, func(ctx context.Context, id int64) ([]byte, error) {
		return s.attachmentBytes(ctx, repo, id)
	})
}

// withPastedFiles repoints hub upload references at local copies the child can
// open, and closes with the file list.
func (s *Server) withPastedFiles(ctx context.Context, repo registry.Repo, sess hubstore.GrillSession, text string) string {
	ids := attachfile.IDsIn(text)
	if len(ids) == 0 {
		return text
	}
	files := s.materializeAttachmentIDs(ctx, repo, grillAttachTicket(sess), ids)
	return attachfile.Rewrite(text, files) + attachfile.Section(files)
}

// grillPastedAnswer does the same for the MCP layer, which holds only a session id.
func (s *Server) grillPastedAnswer(ctx context.Context, sid int64, answer string) string {
	sess, found, err := s.stores.Grill().Session(sid)
	if err != nil || !found {
		return answer
	}
	repo, ok := s.findRepoByRoot(sess.Repo)
	if !ok {
		return answer
	}
	return s.withPastedFiles(ctx, repo, sess, answer)
}
