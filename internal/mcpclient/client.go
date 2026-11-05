// Package mcpclient is tuner's MCP client: the JSON-RPC half of the feature
// browser (tuner#37, features/tuner-feature-browser.feature).
//
// It speaks the streamable-HTTP transport the lab's servers serve — JSON-RPC
// 2.0 over POST, answered either as application/json or as an SSE frame on the
// POST's own response (adhd/internal/mcpserver: "POST /mcp", "GET /mcp") — and
// nothing else: initialize, tools/list, tools/call.
//
// Identity: the client sends tuner's instance id with the handshake, and the
// server decides what that instance may see. Tuner never declares its own
// trust rung; rung-gating is adhd's registry lookup, and a response that omits
// a field is the gate working, not a client error.
//
// Absence: a tool that is not in tools/list — or a tools/call answered with
// -32601 — is "declared but not served", a normal answer in this lab
// (adhd#24 closed @red, adhd#39 open, lab-safety sheets/adhd.md §16). The
// client reports it as ErrToolNotServed so callers can publish the absence
// honestly instead of failing on it.
package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// ProtocolVersion is the MCP revision tuner speaks. The lab pins this one
// (smoke-alarm/internal/engine/mcp_handshake.go); servers negotiate down —
// adhd answers 2024-11-05 and treats the version as not the gate (adhd#27).
const ProtocolVersion = "2025-06-18"

const (
	// MaxBody bounds one JSON-RPC response. A discovered instance is not
	// trusted to size its replies (same rule as channelsource's statusLimit).
	MaxBody = 1 << 20

	// DefaultTimeout bounds one round trip.
	DefaultTimeout = 5 * time.Second

	// DefaultMCPPath is the conventional MCP route: adhd's isotope TXT record
	// carries v/role/trust_rung but no path, so a discovered endpoint's MCP
	// URL is its advertised host:port plus this.
	DefaultMCPPath = "/mcp"
)

// ErrToolNotServed reports a tool that the endpoint does not serve: absent
// from tools/list, or refused with JSON-RPC -32601. It is the wire shape of
// "declared but not served".
var ErrToolNotServed = errors.New("mcp: tool not served")

// RPCError is a JSON-RPC error answer.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("mcp: JSON-RPC error %d: %s", e.Code, e.Message)
}

// Options configures a Client.
type Options struct {
	// Name is the clientInfo name. Defaults to "tuner".
	Name string
	// Version is the clientInfo version.
	Version string
	// Instance is tuner's instance id, sent alongside clientInfo at
	// initialize. The server looks this identity up in its own registry to
	// rung-gate the response; the client never sends a rung.
	Instance string
	// Timeout bounds one round trip. Defaults to DefaultTimeout.
	Timeout time.Duration
}

// Client sends JSON-RPC requests to one MCP endpoint.
type Client struct {
	endpoint string
	opts     Options
	http     *http.Client
	nextID   atomic.Int64
}

// New creates a client for an MCP endpoint URL (e.g. http://host:9090/mcp).
func New(endpoint string, opts Options) *Client {
	if opts.Name == "" {
		opts.Name = "tuner"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	return &Client{
		endpoint: endpoint,
		opts:     opts,
		http: &http.Client{
			Timeout: opts.Timeout,
			// Follow a discovered endpoint only where it advertised itself: a
			// redirect would take the handshake somewhere the mDNS record
			// never named (same rule as channelsource's status client).
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// ServerInfo is what the initialize handshake reported.
type ServerInfo struct {
	ProtocolVersion string `json:"protocolVersion"`
	Name            string `json:"name"`
	Version         string `json:"version"`
	Instructions    string `json:"instructions,omitempty"`
}

type jsonrpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id,omitempty"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Initialize performs the MCP handshake: an initialize call carrying tuner's
// instance identity, then the notifications/initialized notification.
func (c *Client) Initialize(ctx context.Context) (ServerInfo, error) {
	params := map[string]interface{}{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    c.opts.Name,
			"version": c.opts.Version,
		},
	}
	// The instance id travels with the handshake so the server can rung-gate
	// by registry lookup. It is an identity, not a capability claim.
	if c.opts.Instance != "" {
		params["instance"] = c.opts.Instance
	}

	raw, err := c.call(ctx, "initialize", params)
	if err != nil {
		return ServerInfo{}, err
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(raw, &init); err != nil {
		return ServerInfo{}, fmt.Errorf("mcp: decode initialize result: %w", err)
	}
	info := ServerInfo{
		ProtocolVersion: init.ProtocolVersion,
		Name:            init.ServerInfo.Name,
		Version:         init.ServerInfo.Version,
		Instructions:    init.Instructions,
	}

	// The completion notification. Servers may answer it with an empty result
	// or accept it silently; either way a failure here is not fatal — the
	// session is usable, and the next call reports a real problem if any.
	if _, err := c.call(ctx, "notifications/initialized", map[string]interface{}{}); err != nil {
		var rpcErr *RPCError
		if errors.As(err, &rpcErr) {
			return info, nil // answered with an error object: tolerated, the surface still lists
		}
	}
	return info, nil
}

// ListTools returns the names of every tool the endpoint serves. A tool the
// contract declares but this list omits is declared-absent, not broken.
func (c *Client) ListTools(ctx context.Context) ([]string, error) {
	raw, err := c.call(ctx, "tools/list", map[string]interface{}{})
	if err != nil {
		return nil, err
	}
	var list struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("mcp: decode tools/list result: %w", err)
	}
	names := make([]string, 0, len(list.Tools))
	for _, tool := range list.Tools {
		if tool.Name != "" {
			names = append(names, tool.Name)
		}
	}
	return names, nil
}

// CallTool invokes tools/call and returns the result payload as JSON.
//
// Both result shapes the lab serves are accepted: adhd answers tools/call
// with the handler's raw JSON, while a spec-strict server wraps it in MCP
// content blocks — when result.content[] carries text that parses as JSON,
// that JSON is the payload. A tool the endpoint does not serve answers
// ErrToolNotServed (wrapped, so errors.Is works).
func (c *Client) CallTool(ctx context.Context, name string, args map[string]interface{}) (json.RawMessage, error) {
	if args == nil {
		args = map[string]interface{}{}
	}
	raw, err := c.call(ctx, "tools/call", map[string]interface{}{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		var rpcErr *RPCError
		if errors.As(err, &rpcErr) && rpcErr.Code == -32601 {
			return nil, fmt.Errorf("%w: %s", ErrToolNotServed, name)
		}
		return nil, err
	}
	return toolPayload(raw)
}

// toolPayload unwraps a tools/call result: raw JSON stays as it is; an MCP
// content envelope yields the first text block that parses as JSON.
func toolPayload(raw json.RawMessage) (json.RawMessage, error) {
	var envelope struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Content != nil {
		var firstText string
		for _, block := range envelope.Content {
			if block.Type != "" && block.Type != "text" {
				continue
			}
			if json.Valid([]byte(block.Text)) {
				return json.RawMessage(block.Text), nil
			}
			if firstText == "" {
				firstText = block.Text
			}
		}
		if envelope.IsError {
			return nil, fmt.Errorf("mcp: tool reported an error: %s", firstText)
		}
		if firstText != "" {
			// Non-JSON text content: hand it back as a JSON string so callers
			// have one shape to decode.
			quoted, err := json.Marshal(firstText)
			if err == nil {
				return quoted, nil
			}
		}
	}
	return raw, nil
}

// call sends one JSON-RPC request and returns the result payload.
func (c *Client) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	reqBody := jsonrpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	if strings.HasPrefix(method, "notifications/") {
		reqBody.ID = 0 // a notification carries no id and expects no answer
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("mcp: encode %s: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mcp: request %s: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	// The endpoint is either discovered via mDNS or supplied by the operator;
	// calling it is the point of the client.
	resp, err := c.http.Do(req) //nolint:gosec // G107: the discovered/configured endpoint is the data source by design
	if err != nil {
		return nil, fmt.Errorf("mcp: post %s: %w", c.endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusAccepted && reqBody.ID == 0 {
		return json.RawMessage("{}"), nil // notification accepted, no body
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mcp: %s returned %s", c.endpoint, resp.Status)
	}

	limited := io.LimitReader(resp.Body, MaxBody)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("mcp: read %s response: %w", method, err)
	}
	if reqBody.ID == 0 && len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage("{}"), nil
	}

	msg, err := decodeResponse(raw, id)
	if err != nil {
		return nil, fmt.Errorf("mcp: %s: %w", method, err)
	}
	if msg.Error != nil {
		return nil, msg.Error
	}
	if msg.Result == nil {
		return json.RawMessage("{}"), nil
	}
	return msg.Result, nil
}

// decodeResponse parses a JSON-RPC answer that arrived either as JSON or as an
// SSE frame on the POST's own response (adhd serves both). For SSE it takes
// the first data payload carrying this request's id, else the first response.
func decodeResponse(raw []byte, id int64) (*jsonrpcResponse, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, errors.New("empty response")
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		var msg jsonrpcResponse
		if err := json.Unmarshal(trimmed, &msg); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		return &msg, nil
	}

	var fallback *jsonrpcResponse
	scanner := bufio.NewScanner(bytes.NewReader(trimmed))
	scanner.Buffer(make([]byte, 0, 8<<10), MaxBody)
	for scanner.Scan() {
		payload, ok := strings.CutPrefix(scanner.Text(), "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" || !json.Valid([]byte(payload)) {
			continue
		}
		var msg jsonrpcResponse
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			continue
		}
		if msg.ID == id {
			return &msg, nil
		}
		if fallback == nil {
			fallback = &msg
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read event stream: %w", err)
	}
	if fallback != nil {
		return fallback, nil
	}
	return nil, errors.New("response is neither JSON nor an SSE frame carrying one")
}
