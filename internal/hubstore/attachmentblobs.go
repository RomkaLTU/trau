package hubstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// ErrAttachmentTooLarge reports a stream that exceeded the caller's byte cap. The
// partial write is discarded, so nothing lands in the store.
var ErrAttachmentTooLarge = errors.New("attachment too large")

// AttachmentBlobs is the content-addressed store of attachment bytes on disk:
// each file lives at <root>/<sha256[:2]>/<sha256>, so identical content written
// twice occupies one file and the name is its own integrity check. The extension
// is deliberately absent — the mime type belongs to the attachments table, not the
// filename. Because two attachment rows may address one blob, removal is the
// caller's decision once it has established nothing else references the digest.
type AttachmentBlobs struct {
	root string
}

// NewAttachmentBlobs returns a blob store rooted at root, created on first write.
func NewAttachmentBlobs(root string) *AttachmentBlobs { return &AttachmentBlobs{root: root} }

// Root returns the directory the blobs live under.
func (b *AttachmentBlobs) Root() string { return b.root }

// Put streams r into the store and returns its digest and byte count. A stream
// longer than maxBytes yields ErrAttachmentTooLarge and writes nothing; a
// non-positive maxBytes is unbounded. Content already present is left alone —
// the digest matching is proof the bytes match — so a re-fetch of the same file
// costs a hash and nothing more.
func (b *AttachmentBlobs) Put(r io.Reader, maxBytes int64) (sha string, size int64, err error) {
	if b.root == "" {
		return "", 0, errors.New("no attachments root resolved")
	}
	if err := os.MkdirAll(b.root, 0o755); err != nil {
		return "", 0, err
	}
	tmp, err := os.CreateTemp(b.root, ".incoming-*")
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	src := r
	if maxBytes > 0 {
		// One byte past the cap, so an oversized stream is detected rather than
		// silently truncated into a valid-looking blob.
		src = io.LimitReader(r, maxBytes+1)
	}
	sum := sha256.New()
	size, err = io.Copy(io.MultiWriter(tmp, sum), src)
	err = errors.Join(err, tmp.Close())
	if err != nil {
		return "", 0, err
	}
	if maxBytes > 0 && size > maxBytes {
		return "", 0, ErrAttachmentTooLarge
	}

	sha = hex.EncodeToString(sum.Sum(nil))
	path := b.Path(sha)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", 0, err
	}
	if _, err := os.Stat(path); err == nil {
		return sha, size, nil
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", 0, err
	}
	return sha, size, nil
}

// Open returns the blob's contents for reading. The caller closes the file.
func (b *AttachmentBlobs) Open(sha string) (*os.File, error) {
	if sha == "" {
		return nil, os.ErrNotExist
	}
	return os.Open(b.Path(sha))
}

// Remove deletes the blob. An absent file is not an error: the row may have been
// registered but never fetched, and a double delete must stay harmless.
func (b *AttachmentBlobs) Remove(sha string) error {
	if sha == "" {
		return nil
	}
	if err := os.Remove(b.Path(sha)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Path returns where the digest's bytes live, whether or not they are there yet.
func (b *AttachmentBlobs) Path(sha string) string {
	return filepath.Join(b.root, sha[:2], sha)
}
