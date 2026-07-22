package webserver

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/attachfile"
	"github.com/RomkaLTU/trau/internal/hubstore"
)

// pasteImage stores a PNG the way the upload endpoint does and returns the
// markdown reference a paste inserts for it.
func pasteImage(t *testing.T, stores *hubstore.Stores, root, name string) string {
	t.Helper()
	sha, size, err := stores.Attachments().Blobs().Put(strings.NewReader(attachmentPNG), 1<<20)
	if err != nil {
		t.Fatalf("store blob: %v", err)
	}
	att, err := stores.Attachments().Create(hubstore.Attachment{
		Repo:      root,
		Source:    hubstore.AttachmentSourceUpload,
		Filename:  name,
		MimeType:  "image/png",
		SizeBytes: size,
		SHA256:    sha,
		State:     hubstore.AttachmentCached,
	})
	if err != nil {
		t.Fatalf("register upload: %v", err)
	}
	return fmt.Sprintf("![%s](%s/repos/acme/attachments/%d)", name, APIPrefix, att.ID)
}

// assertMaterialized checks that text hands the agent the local copy of name
// rather than the hub URL.
func assertMaterialized(t *testing.T, text, ticket, name string) {
	t.Helper()
	for _, want := range []string{filepath.Join(attachfile.Dir(ticket), name), "--- Attachments ---", "image/png"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, APIPrefix+"/repos/acme/attachments/") {
		t.Errorf("kept the hub URL the agent cannot open:\n%s", text)
	}
}

func TestGrillAuthoringPromptMaterializesPastedImage(t *testing.T) {
	r, store, repo, _ := newGrillRunnerTest(t, grillStubScript)
	ref := pasteImage(t, r.srv.stores, repo.Root, "seed.png")

	sess, err := store.Create(hubstore.NewGrillSession{Repo: repo.Root})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() { attachfile.Remove(grillAttachTicket(sess)) })
	if _, _, err := store.AppendMessage(sess.ID, hubstore.NewGrillMessage{
		Role: hubstore.GrillRoleUser, Kind: hubstore.GrillKindInfo,
		Payload: fmt.Sprintf(`{"text":"The toolbar looks wrong: %s"}`, ref),
	}); err != nil {
		t.Fatalf("seed idea: %v", err)
	}

	prompt := r.firstPrompt(context.Background(), repo, sess)
	if !strings.Contains(prompt, "The idea to develop:") {
		t.Fatalf("authoring prompt lost its seed framing:\n%s", prompt)
	}
	assertMaterialized(t, prompt, grillAttachTicket(sess), "seed.png")
}

func TestGrillAnswerPromptMaterializesPastedImage(t *testing.T) {
	r, store, repo, _ := newGrillRunnerTest(t, grillStubScript)
	ref := pasteImage(t, r.srv.stores, repo.Root, "answer.png")

	sess, err := store.Create(hubstore.NewGrillSession{Repo: repo.Root, IssueID: "COD-1072"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() { attachfile.Remove(grillAttachTicket(sess)) })
	if _, _, err := store.AppendMessage(sess.ID, hubstore.NewGrillMessage{
		Role: hubstore.GrillRoleUser, Kind: hubstore.GrillKindAnswer,
		Payload: fmt.Sprintf(`{"text":"Like this: %s"}`, ref),
	}); err != nil {
		t.Fatalf("append answer: %v", err)
	}

	assertMaterialized(t, r.answerPrompt(context.Background(), repo, sess), grillAttachTicket(sess), "answer.png")
}

// TestGrillMCPAskUserMaterializesPastedImage covers the live path, where the answer
// reaches the child as the ask_user tool result rather than as a resume prompt.
func TestGrillMCPAskUserMaterializesPastedImage(t *testing.T) {
	home := t.TempDir()
	root, _ := checkpointRepo(t, home, "acme")
	stores := testStoresAt(t, home)
	ref := pasteImage(t, stores, root, "live.png")
	_, ts := controlServer(t, home, nil)

	sess := createGrill(t, ts, "acme", "COD-1072-live")
	t.Cleanup(func() { attachfile.Remove("COD-1072-live") })

	done := make(chan rpcMsg, 1)
	errc := make(chan error, 1)
	go func() {
		res, err := doMCPPost(mcpURL(ts, sess.ID), toolCall("ask_user", map[string]any{
			"question": "What does it look like?",
		}))
		if err != nil {
			errc <- err
			return
		}
		msg, err := readSSEResult(res)
		if err != nil {
			errc <- err
			return
		}
		done <- msg
	}()

	waitForGrillState(t, ts, sess.ID, hubstore.GrillWaiting)
	ans := postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/answer", GrillAnswerRequest{Text: "Like this: " + ref})
	_ = ans.Body.Close()
	if ans.StatusCode != http.StatusOK {
		t.Fatalf("answer status = %d, want 200", ans.StatusCode)
	}

	select {
	case err := <-errc:
		t.Fatalf("ask_user call failed: %v", err)
	case msg := <-done:
		tr := toolResult(t, msg)
		if len(tr.Content) != 1 {
			t.Fatalf("ask_user result = %+v, want one content block", tr.Content)
		}
		assertMaterialized(t, tr.Content[0].Text, "COD-1072-live", "live.png")
	case <-time.After(5 * time.Second):
		t.Fatal("ask_user did not return after the answer")
	}
}
