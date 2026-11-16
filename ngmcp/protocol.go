// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

// Package ngmcp implements a pure-Go, zero-dependency Model Context Protocol
// (MCP) JSON-RPC 2.0 server and client for authoring, inspecting, validating,
// and transactionally editing ntnodegraph specifications.
//
// ngmcp contains zero host I/O or runtime execution imports so it compiles
// cleanly to GOOS=js GOARCH=wasm and extracts alongside internal/nodegraph into
// nimbleterminal.dev/ntnodegraph/ngmcp. Runtime hosts such as Nebula layer
// execution tools (like "run") on top via Server.RegisterTool.
package ngmcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// defaultImplementationVersion tracks the library release, independently of the
// MCP protocol version. Hosts may override it through ImplementationInfo.
const defaultImplementationVersion = "0.2.0"

// MCP protocol version and JSON-RPC 2.0 error codes.
const (
	ProtocolVersion        = "2025-06-18"
	LegacyProtocolVersion  = "2024-11-05"
	JSONRPCVersion         = "2.0"
	DefaultMaxMessageBytes = 4 << 20 // 4 MiB
	DefaultMaxOpsPerBatch  = 512

	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Sentinel errors returned by framing and protocol helpers.
var (
	ErrMessageTooLarge = errors.New("ngmcp: JSON-RPC message exceeds byte limit")
	ErrInvalidFrame    = errors.New("ngmcp: invalid JSON-RPC stream frame")
	ErrClientClosed    = errors.New("ngmcp: client connection closed")
)

// Limits bounds untrusted JSON-RPC payloads and edit batch sizes.
type Limits struct {
	MaxMessageBytes int
	MaxOpsPerBatch  int
}

func (l Limits) withDefaults() Limits {
	if l.MaxMessageBytes <= 0 {
		l.MaxMessageBytes = DefaultMaxMessageBytes
	}
	if l.MaxOpsPerBatch <= 0 {
		l.MaxOpsPerBatch = DefaultMaxOpsPerBatch
	}
	return l
}

// Request is an inbound or outbound JSON-RPC 2.0 request or notification.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification reports whether the request omits an id (or is a notifications/* message).
func (r Request) IsNotification() bool {
	return len(bytes.TrimSpace(r.ID)) == 0
}

// Response is a JSON-RPC 2.0 response envelope.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error implements error.
func (e *RPCError) Error() string {
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// ImplementationInfo identifies an MCP server or client.
type ImplementationInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeParams carries the client's MCP handshake parameters.
type InitializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    map[string]any     `json:"capabilities,omitempty"`
	ClientInfo      ImplementationInfo `json:"clientInfo,omitempty"`
}

// ServerCapabilities advertises the MCP primitives supported by Server.
type ServerCapabilities struct {
	Tools     *ToolsCapability     `json:"tools,omitempty"`
	Resources *ResourcesCapability `json:"resources,omitempty"`
}

// ToolsCapability advertises MCP tool support.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ResourcesCapability advertises MCP resource support.
type ResourcesCapability struct {
	Subscribe   bool `json:"subscribe,omitempty"`
	ListChanged bool `json:"listChanged,omitempty"`
}

// InitializeResult is returned from the MCP "initialize" handshake.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      ImplementationInfo `json:"serverInfo"`
	Instructions    string             `json:"instructions,omitempty"`
}

// ToolAnnotations provides optional MCP behavioral hints about a tool.
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint,omitempty"`
	DestructiveHint bool   `json:"destructiveHint,omitempty"`
	IdempotentHint  bool   `json:"idempotentHint,omitempty"`
}

// ToolDef describes an MCP tool and its JSON Schema input specification.
type ToolDef struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	InputSchema map[string]any   `json:"inputSchema"`
	Annotations *ToolAnnotations `json:"annotations,omitempty"`
}

// ListToolsResult is returned by "tools/list".
type ListToolsResult struct {
	Tools []ToolDef `json:"tools"`
}

// CallToolParams is sent to "tools/call".
type CallToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// ContentBlock represents an item in CallToolResult.Content.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// CallToolResult is returned by "tools/call".
type CallToolResult struct {
	Content           []ContentBlock `json:"content"`
	StructuredContent any            `json:"structuredContent,omitempty"`
	IsError           bool           `json:"isError,omitempty"`
}

// TextResult formats v as deterministic indented JSON inside a text ContentBlock
// and also attaches it as StructuredContent.
func TextResult(v any) (CallToolResult, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return CallToolResult{}, err
	}
	txt := strings.TrimRight(buf.String(), "\n")
	return CallToolResult{
		Content:           []ContentBlock{{Type: "text", Text: txt}},
		StructuredContent: v,
	}, nil
}

// ErrorResult constructs a tool-level error CallToolResult (IsError: true) with
// optional structured diagnostic payload.
func ErrorResult(err error, structured any) CallToolResult {
	msg := "unknown error"
	if err != nil {
		msg = err.Error()
	}
	if structured != nil {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(structured); encErr == nil {
			msg = strings.TrimRight(buf.String(), "\n")
		}
	}
	return CallToolResult{
		Content:           []ContentBlock{{Type: "text", Text: msg}},
		StructuredContent: structured,
		IsError:           true,
	}
}

// ResourceDef describes an MCP resource URI exposed by Server.
type ResourceDef struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

// ListResourcesResult is returned by "resources/list".
type ListResourcesResult struct {
	Resources []ResourceDef `json:"resources"`
}

// ReadResourceParams is sent to "resources/read".
type ReadResourceParams struct {
	URI string `json:"uri"`
}

// ResourceContents holds a read MCP resource payload.
type ResourceContents struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
}

// ReadResourceResult is returned by "resources/read".
type ReadResourceResult struct {
	Contents []ResourceContents `json:"contents"`
}

// readBoundedFrame reads a single MCP JSON-RPC 2.0 message from br, supporting
// both standard MCP newline-delimited JSON and optional LSP-style
// "Content-Length: <n>\r\n\r\n" framing while enforcing maxBytes.
func readBoundedFrame(br *bufio.Reader, maxBytes int) ([]byte, error) {
	for {
		// Skip leading blank lines between messages.
		b, err := br.Peek(1)
		if err != nil {
			return nil, err
		}
		if b[0] == '\r' || b[0] == '\n' {
			_, _ = br.ReadByte()
			continue
		}

		// Detect optional Content-Length header framing.
		if b[0] == 'C' || b[0] == 'c' {
			hdrLine, err := readBoundedLine(br, 1024)
			if err != nil {
				return nil, err
			}
			trimmed := strings.TrimRight(string(hdrLine), "\r\n")
			lower := strings.ToLower(trimmed)
			if !strings.HasPrefix(lower, "content-length:") {
				return nil, fmt.Errorf("%w: unexpected header %q", ErrInvalidFrame, trimmed)
			}
			lenStr := strings.TrimSpace(trimmed[len("content-length:"):])
			n, convErr := strconv.Atoi(lenStr)
			if convErr != nil || n < 0 {
				return nil, fmt.Errorf("%w: invalid Content-Length %q", ErrInvalidFrame, lenStr)
			}
			if n > maxBytes {
				return nil, fmt.Errorf("%w: %d > %d", ErrMessageTooLarge, n, maxBytes)
			}
			// Drain remaining headers until blank line.
			for {
				nextHdr, err := readBoundedLine(br, 1024)
				if err != nil {
					return nil, err
				}
				if len(bytes.TrimRight(nextHdr, "\r\n")) == 0 {
					break
				}
			}
			payload := make([]byte, n)
			if _, err := io.ReadFull(br, payload); err != nil {
				return nil, err
			}
			return payload, nil
		}

		// Standard MCP stdio newline-delimited JSON-RPC 2.0 message.
		line, err := readBoundedLine(br, maxBytes)
		if err != nil && (!errors.Is(err, io.EOF) || len(line) == 0) {
			return nil, err
		}
		return bytes.TrimRight(line, "\r\n"), nil
	}
}

func readBoundedLine(br *bufio.Reader, maxBytes int) ([]byte, error) {
	var out []byte
	for {
		frag, isPrefix, err := br.ReadLine()
		out = append(out, frag...)
		if len(out) > maxBytes {
			return nil, fmt.Errorf("%w: exceeds %d bytes", ErrMessageTooLarge, maxBytes)
		}
		if err != nil {
			return out, err
		}
		if !isPrefix {
			return out, nil
		}
	}
}

func writeNewlineJSON(w io.Writer, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// Client is a concurrency-safe MCP JSON-RPC 2.0 client over an io.Reader and
// io.Writer pair (such as stdio pipes, in-memory io.Pipe, or a loopback socket).
type Client struct {
	r         *bufio.Reader
	w         io.Writer
	writeMu   sync.Mutex
	limits    Limits
	nextID    atomic.Int64
	pendMu    sync.Mutex
	pending   map[string]chan Response
	readDone  chan struct{}
	readErr   error
	closeOnce sync.Once
}

// NewClient constructs a streaming MCP Client over r and w.
func NewClient(r io.Reader, w io.Writer, limits ...Limits) *Client {
	var lim Limits
	if len(limits) > 0 {
		lim = limits[0]
	}
	lim = lim.withDefaults()
	c := &Client{
		r:        bufio.NewReaderSize(r, 64*1024),
		w:        w,
		limits:   lim,
		pending:  make(map[string]chan Response),
		readDone: make(chan struct{}),
	}
	go c.readLoop()
	return c
}

func (c *Client) readLoop() {
	defer close(c.readDone)
	for {
		raw, err := readBoundedFrame(c.r, c.limits.MaxMessageBytes)
		if err != nil {
			c.pendMu.Lock()
			c.readErr = err
			for k, ch := range c.pending {
				close(ch)
				delete(c.pending, k)
			}
			c.pendMu.Unlock()
			return
		}
		var resp Response
		if err := json.Unmarshal(raw, &resp); err != nil {
			continue
		}
		idKey := string(bytes.TrimSpace(resp.ID))
		if idKey == "" {
			continue
		}
		c.pendMu.Lock()
		ch, ok := c.pending[idKey]
		if ok {
			delete(c.pending, idKey)
		}
		c.pendMu.Unlock()
		if ok {
			ch <- resp
			close(ch)
		}
	}
}

// Call invokes an MCP JSON-RPC method and decodes its result into out (if non-nil).
func (c *Client) Call(ctx context.Context, method string, params any, out any) error {
	id := c.nextID.Add(1)
	idRaw := json.RawMessage(strconv.FormatInt(id, 10))
	idKey := string(idRaw)

	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("ngmcp: marshal params: %w", err)
		}
		rawParams = b
	}

	ch := make(chan Response, 1)
	c.pendMu.Lock()
	select {
	case <-c.readDone:
		err := c.readErr
		c.pendMu.Unlock()
		if err == nil || errors.Is(err, io.EOF) {
			return ErrClientClosed
		}
		return err
	default:
	}
	c.pending[idKey] = ch
	c.pendMu.Unlock()

	req := Request{
		JSONRPC: JSONRPCVersion,
		ID:      idRaw,
		Method:  method,
		Params:  rawParams,
	}
	c.writeMu.Lock()
	writeErr := writeNewlineJSON(c.w, req)
	c.writeMu.Unlock()
	if writeErr != nil {
		c.pendMu.Lock()
		delete(c.pending, idKey)
		c.pendMu.Unlock()
		return writeErr
	}

	select {
	case <-ctx.Done():
		c.pendMu.Lock()
		delete(c.pending, idKey)
		c.pendMu.Unlock()
		return ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			if c.readErr != nil && !errors.Is(c.readErr, io.EOF) {
				return c.readErr
			}
			return ErrClientClosed
		}
		if resp.Error != nil {
			return resp.Error
		}
		if out != nil && resp.Result != nil {
			rawRes, err := json.Marshal(resp.Result)
			if err != nil {
				return err
			}
			return json.Unmarshal(rawRes, out)
		}
		return nil
	}
}

// Notify sends a JSON-RPC 2.0 notification without waiting for a response.
func (c *Client) Notify(method string, params any) error {
	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		rawParams = b
	}
	req := Request{
		JSONRPC: JSONRPCVersion,
		Method:  method,
		Params:  rawParams,
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeNewlineJSON(c.w, req)
}

// Initialize performs the MCP handshake ("initialize" + "notifications/initialized").
func (c *Client) Initialize(ctx context.Context, clientInfo ImplementationInfo) (InitializeResult, error) {
	if clientInfo.Name == "" {
		clientInfo.Name = "ngmcp-client"
	}
	if clientInfo.Version == "" {
		clientInfo.Version = defaultImplementationVersion
	}
	var res InitializeResult
	err := c.Call(ctx, "initialize", InitializeParams{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    map[string]any{},
		ClientInfo:      clientInfo,
	}, &res)
	if err != nil {
		return InitializeResult{}, err
	}
	if notifyErr := c.Notify("notifications/initialized", map[string]any{}); notifyErr != nil {
		return InitializeResult{}, notifyErr
	}
	return res, nil
}

// ListTools queries "tools/list" on the connected MCP server.
func (c *Client) ListTools(ctx context.Context) ([]ToolDef, error) {
	var res ListToolsResult
	if err := c.Call(ctx, "tools/list", map[string]any{}, &res); err != nil {
		return nil, err
	}
	return res.Tools, nil
}

// CallTool invokes a named MCP tool with arguments and returns its CallToolResult.
func (c *Client) CallTool(ctx context.Context, name string, args any) (CallToolResult, error) {
	var rawArgs json.RawMessage
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return CallToolResult{}, err
		}
		rawArgs = b
	} else {
		rawArgs = json.RawMessage("{}")
	}
	var res CallToolResult
	if err := c.Call(ctx, "tools/call", CallToolParams{
		Name:      name,
		Arguments: rawArgs,
	}, &res); err != nil {
		return CallToolResult{}, err
	}
	return res, nil
}

// ListResources queries "resources/list" on the connected MCP server.
func (c *Client) ListResources(ctx context.Context) ([]ResourceDef, error) {
	var res ListResourcesResult
	if err := c.Call(ctx, "resources/list", map[string]any{}, &res); err != nil {
		return nil, err
	}
	return res.Resources, nil
}

// ReadResource reads a single MCP resource URI via "resources/read".
func (c *Client) ReadResource(ctx context.Context, uri string) (ReadResourceResult, error) {
	var res ReadResourceResult
	if err := c.Call(ctx, "resources/read", ReadResourceParams{URI: uri}, &res); err != nil {
		return ReadResourceResult{}, err
	}
	return res, nil
}

// Close closes the underlying writer if it implements io.Closer.
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		if closer, ok := c.w.(io.Closer); ok {
			err = closer.Close()
		}
	})
	return err
}
