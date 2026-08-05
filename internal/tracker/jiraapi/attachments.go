package jiraapi

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
)

// AddAttachment uploads data as an attachment on an issue and returns the new
// attachment's id. The file travels as a multipart form under the "file" field,
// and Jira rejects the request without the X-Atlassian-Token: no-check header its
// XSRF guard demands on this endpoint.
func (c *Client) AddAttachment(ctx context.Context, key, filename string, data []byte) (string, error) {
	if !c.enabled() {
		return "", ErrNotEnabled
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", ErrNotFound
	}
	body, contentType, err := multipartFile(filename, data)
	if err != nil {
		return "", err
	}
	header := http.Header{
		"Content-Type":      {contentType},
		"X-Atlassian-Token": {"no-check"},
	}
	var dst []struct {
		ID string `json:"id"`
	}
	path := "/issue/" + url.PathEscape(key) + "/attachments"
	if err := c.send(ctx, http.MethodPost, path, body, header, &dst); err != nil {
		return "", err
	}
	if len(dst) == 0 || dst[0].ID == "" {
		return "", fmt.Errorf("jira: attachment %q on %s returned no id", filename, key)
	}
	return dst[0].ID, nil
}

// multipartFile renders one file as a multipart form body, returning it with the
// content type carrying the writer's boundary.
func multipartFile(filename string, data []byte) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(data); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// MediaID resolves an attachment's Media Services id — the handle an ADF media
// node embeds it by. No REST payload carries it: the attachment content route
// answers with a redirect to the media host and the id sits in that Location URL,
// so the request stops at the redirect rather than following it. This is the
// documented workaround for JRACLOUD-96384.
func (c *Client) MediaID(ctx context.Context, attachmentID string) (string, error) {
	if !c.enabled() {
		return "", ErrNotEnabled
	}
	endpoint := c.baseURL + apiPrefix + "/attachment/content/" + url.PathEscape(attachmentID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", c.auth)

	res, err := c.stopAtRedirect().Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()

	id := mediaUUID(res.Header.Get("Location"))
	if id == "" {
		return "", fmt.Errorf("jira: attachment %s carries no media id (HTTP %d)", attachmentID, res.StatusCode)
	}
	return id, nil
}

// stopAtRedirect returns a client that reports a redirect instead of following
// it, sharing the transport — and so the connection pool and the test suite's
// network guard — with the client's own.
func (c *Client) stopAtRedirect() *http.Client {
	return &http.Client{
		Timeout:       c.http.Timeout,
		Transport:     c.http.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// mediaUUID reads the Media Services id out of an attachment content redirect,
// where it sits between the /file/ and /binary path segments.
func mediaUUID(location string) string {
	_, rest, ok := strings.Cut(location, "/file/")
	if !ok {
		return ""
	}
	id, _, ok := strings.Cut(rest, "/binary")
	if !ok {
		return ""
	}
	return id
}
