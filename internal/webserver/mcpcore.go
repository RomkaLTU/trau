package webserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// The hub speaks MCP over Streamable HTTP: JSON-RPC 2.0 requests arrive by POST
// and are answered with application/json, or with an SSE stream when a tool
// blocks. mcpServer holds what differs between endpoints — the name initialize
// reports, the declared tools, and the tools/call dispatch — so the grill's
// per-session server and the hub's general-purpose one share one envelope.
const (
	jsonrpcVersion     = "2.0"
	mcpProtocolVersion = "2025-06-18"

	rpcParseError     = -32700
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternalError  = -32603
)

var nullID = json.RawMessage("null")

type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type jsonrpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations *mcpAnnotations `json:"annotations,omitempty"`
}

// mcpAnnotations are the behaviour hints a client may act on when deciding
// whether a tool needs confirmation: readOnlyHint marks the tools that change
// nothing, destructiveHint the ones whose write takes something away.
type mcpAnnotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint,omitempty"`
}

var (
	readOnlyTool    = &mcpAnnotations{ReadOnlyHint: true}
	destructiveTool = &mcpAnnotations{DestructiveHint: true}
)

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolResult struct {
	Content           []mcpContent `json:"content"`
	StructuredContent any          `json:"structuredContent,omitempty"`
	IsError           bool         `json:"isError,omitempty"`
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Meta      struct {
		ProgressToken json.RawMessage `json:"progressToken"`
	} `json:"_meta"`
}

type mcpServer struct {
	name     string
	version  string
	tools    []mcpTool
	callTool func(w http.ResponseWriter, r *http.Request, rpcID json.RawMessage, p toolsCallParams)
}

// serve answers one JSON-RPC request, handing tools/call to the endpoint's own
// dispatch. A notification — a request carrying no id — is acknowledged and
// nothing is sent back.
func (m mcpServer) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req jsonrpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondRPCError(w, nullID, rpcParseError, "parse error")
		return
	}
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch req.Method {
	case "initialize":
		m.initialize(w, req)
	case "ping":
		respondRPCJSON(w, req.ID, map[string]any{})
	case "tools/list":
		respondRPCJSON(w, req.ID, map[string]any{"tools": m.tools})
	case "tools/call":
		var p toolsCallParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			respondRPCError(w, req.ID, rpcInvalidParams, "invalid params")
			return
		}
		m.callTool(w, r, req.ID, p)
	default:
		respondRPCError(w, req.ID, rpcMethodNotFound, "method not found: "+req.Method)
	}
}

// initialize echoes the client's protocol version so a client pinned to an older
// revision is not forced onto ours, falling back to the version the hub speaks.
func (m mcpServer) initialize(w http.ResponseWriter, req jsonrpcRequest) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(req.Params, &p)
	version := p.ProtocolVersion
	if version == "" {
		version = mcpProtocolVersion
	}
	respondRPCJSON(w, req.ID, map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": m.name, "version": m.version},
	})
}

// mcpToolJSON renders a tool's payload as both the JSON text every MCP client can
// read and the structured content typed clients prefer.
func mcpToolJSON(v any) mcpToolResult {
	data, err := json.Marshal(v)
	if err != nil {
		return mcpToolError("encode result: " + err.Error())
	}
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(data)}}, StructuredContent: v}
}

func mcpToolError(msg string) mcpToolResult {
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: msg}}, IsError: true}
}

func mcpToolSuccess(msg string) mcpToolResult {
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: msg}}}
}

func respondRPCJSON(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(jsonrpcResponse{JSONRPC: jsonrpcVersion, ID: id, Result: result})
}

func respondRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(jsonrpcResponse{JSONRPC: jsonrpcVersion, ID: id, Error: &jsonrpcError{Code: code, Message: msg}})
}

func writeMCPMessage(w io.Writer, msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	return err
}

// writeMCPProgress emits an MCP progress notification, keeping the client's tool
// idle timer fed while a tool blocks. Progress references the token from the tool
// call; with no token there is nothing to correlate, so it is a no-op and the SSE
// keepalive comment carries the connection alone.
func writeMCPProgress(w io.Writer, token json.RawMessage, progress int) error {
	if len(token) == 0 {
		return nil
	}
	return writeMCPMessage(w, jsonrpcNotification{
		JSONRPC: jsonrpcVersion,
		Method:  "notifications/progress",
		Params: map[string]any{
			"progressToken": token,
			"progress":      progress,
			"message":       "waiting for the user to answer",
		},
	})
}
