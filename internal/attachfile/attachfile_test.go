package attachfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bytesFetcher(bodies map[int64]string) Fetcher {
	return func(_ context.Context, id int64) ([]byte, error) {
		body, ok := bodies[id]
		if !ok {
			return nil, errors.New("no such attachment")
		}
		return []byte(body), nil
	}
}

// Materialize writes each attachment into the ticket's directory and reports the
// path it landed on, so the prompt can name a file the agent can open.
func TestMaterializeWritesFiles(t *testing.T) {
	ticket := "COD-1041-" + t.Name()
	t.Cleanup(func() { Remove(ticket) })

	files := Materialize(context.Background(), ticket, []Ref{
		{ID: 7, Filename: "shot.png", MimeType: "image/png", IsImage: true},
		{ID: 8, Filename: "run.log", MimeType: "text/plain"},
	}, bytesFetcher(map[int64]string{7: "PNGDATA", 8: "boom"}))

	if len(files) != 2 {
		t.Fatalf("files = %d, want 2", len(files))
	}
	for _, f := range files {
		if f.Err != nil {
			t.Fatalf("attachment %d: %v", f.ID, f.Err)
		}
		if filepath.Dir(f.Path) != Dir(ticket) {
			t.Errorf("path %q is outside the ticket directory %q", f.Path, Dir(ticket))
		}
		if _, err := os.Stat(f.Path); err != nil {
			t.Errorf("materialized file missing: %v", err)
		}
	}
	if got := files[0].Size; got != int64(len("PNGDATA")) {
		t.Errorf("size = %d, want the bytes actually written", got)
	}

	Remove(ticket)
	if _, err := os.Stat(Dir(ticket)); !os.IsNotExist(err) {
		t.Errorf("Remove left %q behind", Dir(ticket))
	}
}

// A file that will not fetch degrades the prompt rather than failing the run: it
// comes back carrying its error, and the section says it is unavailable.
func TestMaterializeFetchFailureIsNotFatal(t *testing.T) {
	ticket := "COD-1041-" + t.Name()
	t.Cleanup(func() { Remove(ticket) })

	files := Materialize(context.Background(), ticket, []Ref{
		{ID: 1, Filename: "ok.png"},
		{ID: 2, Filename: "gone.png"},
	}, bytesFetcher(map[int64]string{1: "data"}))

	if files[0].Err != nil || files[0].Path == "" {
		t.Fatalf("reachable attachment did not materialize: %+v", files[0])
	}
	if files[1].Err == nil || files[1].Path != "" {
		t.Fatalf("unreachable attachment = %+v, want an error and no path", files[1])
	}
	section := Section(files)
	if !strings.Contains(section, "gone.png unavailable:") {
		t.Errorf("section does not report the unavailable file:\n%s", section)
	}
	if !strings.Contains(section, files[0].Path) {
		t.Errorf("section does not list the materialized path:\n%s", section)
	}
}

// A tracker filename is never trusted to stay inside the ticket directory, and two
// attachments sharing a name must not overwrite each other.
func TestMaterializeNamesAreSafeAndUnique(t *testing.T) {
	ticket := "COD-1041-" + t.Name()
	t.Cleanup(func() { Remove(ticket) })

	files := Materialize(context.Background(), ticket, []Ref{
		{ID: 1, Filename: "../../etc/passwd"},
		{ID: 2, Filename: "shot.png"},
		{ID: 3, Filename: "shot.png"},
		{ID: 4, Filename: ""},
	}, bytesFetcher(map[int64]string{1: "a", 2: "b", 3: "c", 4: "d"}))

	names := make([]string, 0, len(files))
	for _, f := range files {
		if f.Err != nil {
			t.Fatalf("attachment %d: %v", f.ID, f.Err)
		}
		if filepath.Dir(f.Path) != Dir(ticket) {
			t.Fatalf("path %q escaped the ticket directory", f.Path)
		}
		names = append(names, filepath.Base(f.Path))
	}
	want := []string{"passwd", "shot.png", "shot-2.png", "attachment-4"}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("name[%d] = %q, want %q", i, names[i], w)
		}
	}
}

// A ticket carrying a file literally named shot-2.png alongside two called
// shot.png must still land three distinct files: the de-duplicated name cannot be
// one another attachment already claimed.
func TestMaterializeUniqueNameSkipsRealSuffix(t *testing.T) {
	ticket := "COD-1041-" + t.Name()
	t.Cleanup(func() { Remove(ticket) })

	files := Materialize(context.Background(), ticket, []Ref{
		{ID: 1, Filename: "shot.png"},
		{ID: 2, Filename: "shot-2.png"},
		{ID: 3, Filename: "shot.png"},
	}, bytesFetcher(map[int64]string{1: "one", 2: "two", 3: "three"}))

	paths := map[string]bool{}
	for _, f := range files {
		if f.Err != nil {
			t.Fatalf("attachment %d: %v", f.ID, f.Err)
		}
		if paths[f.Path] {
			t.Fatalf("attachment %d reuses path %q", f.ID, f.Path)
		}
		paths[f.Path] = true
	}
	for i, want := range []string{"one", "two", "three"} {
		body, err := os.ReadFile(files[i].Path)
		if err != nil {
			t.Fatalf("attachment %d: %v", files[i].ID, err)
		}
		if string(body) != want {
			t.Errorf("%s = %q, want %q", files[i].Path, body, want)
		}
	}
}

// A row the hub has not cached yet carries no mime type, so the type is read back
// off the bytes — otherwise the first prompt for a freshly synced screenshot calls
// it an unknown type and only a later run gets it right.
func TestMaterializeLearnsMimeFromBytes(t *testing.T) {
	ticket := "COD-1041-" + t.Name()
	t.Cleanup(func() { Remove(ticket) })

	png := string([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0})
	files := Materialize(context.Background(), ticket, []Ref{
		{ID: 1, Filename: "shot.png"},
		{ID: 2, Filename: "run.log"},
		{ID: 3, Filename: "kept.png", MimeType: "image/png", IsImage: true},
	}, bytesFetcher(map[int64]string{1: png, 2: "boom", 3: png}))

	if !files[0].IsImage || files[0].MimeType != "image/png" {
		t.Errorf("pending screenshot = %q image=%v, want image/png", files[0].MimeType, files[0].IsImage)
	}
	if !strings.HasPrefix(files[1].MimeType, "text/plain") || files[1].IsImage {
		t.Errorf("pending log = %q image=%v, want text/plain", files[1].MimeType, files[1].IsImage)
	}
	if files[2].MimeType != "image/png" {
		t.Errorf("cached row = %q, want the stored type untouched", files[2].MimeType)
	}
	if section := Section(files); strings.Contains(section, "unknown type") {
		t.Errorf("section still calls a materialized file an unknown type:\n%s", section)
	}
}

// Rewrite repoints both reference forms an issue body can carry — the tracker's
// own URL and the hub's attachment route — at the local copy.
func TestRewriteRepointsBothURLForms(t *testing.T) {
	files := []File{
		{Ref: Ref{ID: 42, Filename: "shot.png", SourceURL: "https://uploads.linear.app/abc/shot.png"}, Path: "/tmp/x/shot.png"},
		{Ref: Ref{ID: 43, Filename: "paste.png"}, Path: "/tmp/x/paste.png"},
		{Ref: Ref{ID: 44, Filename: "lost.png", SourceURL: "https://uploads.linear.app/def/lost.png"}, Err: errors.New("gone")},
	}
	body := strings.Join([]string{
		"![shot](https://uploads.linear.app/abc/shot.png)",
		"![paste](/api/v1/repos/loop/attachments/43)",
		"![abs](http://127.0.0.1:8728/api/v1/repos/loop/attachments/43)",
		"![lost](https://uploads.linear.app/def/lost.png)",
		"![other](/api/v1/repos/loop/attachments/99)",
	}, "\n")

	got := Rewrite(body, files)
	for _, want := range []string{
		"![shot](/tmp/x/shot.png)",
		"![paste](/tmp/x/paste.png)",
		"![abs](/tmp/x/paste.png)",
		"![lost](https://uploads.linear.app/def/lost.png)",
		"![other](/api/v1/repos/loop/attachments/99)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// Section describes each file and closes with the instruction to read images; no
// files means no section at all.
func TestSection(t *testing.T) {
	got := Section([]File{
		{Ref: Ref{ID: 1, Filename: "shot.png", MimeType: "image/png", Size: 2048, IsImage: true}, Path: "/tmp/x/shot.png"},
	})
	for _, want := range []string{"--- Attachments ---", "/tmp/x/shot.png", "shot.png", "image/png", "2.0KB", "read them"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if empty := Section(nil); empty != "" {
		t.Errorf("Section(nil) = %q, want empty", empty)
	}
}

// IDsIn finds the hub attachments a pasted answer references, which is how a
// mid-interview upload is discovered — it hangs off no issue yet.
func TestIDsIn(t *testing.T) {
	got := IDsIn("here: ![a](/api/v1/repos/loop/attachments/5) and again /repos/loop/attachments/5 plus /api/v1/repos/loop/attachments/6")
	if len(got) != 2 || got[0] != 5 || got[1] != 6 {
		t.Fatalf("IDsIn = %v, want [5 6] deduped in first-seen order", got)
	}
	if ids := IDsIn("no attachments here"); len(ids) != 0 {
		t.Errorf("IDsIn = %v, want none", ids)
	}
}
