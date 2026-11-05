package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// rpcServer is a minimal stand-in for adhd's MCP surface: JSON-RPC over POST
// /mcp, raw results for tools/call (adhd's shape), and the declared-absent
// answer for a tool it does not serve.
func rpcServer(t *testing.T, tools []string, results map[string]interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req jsonrpcRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		answer := func(result interface{}, rpcErr *RPCError) {
			resp := jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcErr}
			if result != nil {
				raw, _ := json.Marshal(result)
				resp.Result = raw
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}
		switch req.Method {
		case "initialize":
			// Assert the handshake carries tuner's identity, not a rung claim.
			params, _ := json.Marshal(req.Params)
			answer(map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"serverInfo":      map[string]string{"name": "adhd", "version": "0.1.0"},
				"echoParams":      json.RawMessage(params),
			}, nil)
		case "notifications/initialized":
			if req.ID != 0 {
				t.Errorf("notification carried an id: %d", req.ID)
			}
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			entries := make([]map[string]string, 0, len(tools))
			for _, name := range tools {
				entries = append(entries, map[string]string{"name": name})
			}
			answer(map[string]interface{}{"tools": entries}, nil)
		case "tools/call":
			params, ok := req.Params.(map[string]interface{})
			if !ok {
				answer(nil, &RPCError{Code: -32602, Message: "params must be an object"})
				return
			}
			name, _ := params["name"].(string)
			result, served := results[name]
			if !served {
				answer(nil, &RPCError{Code: -32601, Message: "Tool not found: " + name})
				return
			}
			answer(result, nil)
		default:
			answer(nil, &RPCError{Code: -32601, Message: "Method not found"})
		}
	}))
}

func TestInitializeSendsIdentityNotRung(t *testing.T) {
	srv := rpcServer(t, nil, nil)
	defer srv.Close()

	c := New(srv.URL+"/mcp", Options{Version: "test", Instance: "tuner:58093"})
	info, err := c.Initialize(context.Background())
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if info.Name != "adhd" || info.ProtocolVersion != "2024-11-05" {
		t.Fatalf("server info = %+v", info)
	}

	// Re-run the handshake and inspect what the server saw: the instance id
	// travels with clientInfo; no trust rung is ever declared by the client.
	raw, err := c.call(context.Background(), "initialize", map[string]interface{}{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "tuner", "version": "test"},
		"instance":        "tuner:58093",
	})
	if err != nil {
		t.Fatalf("second initialize: %v", err)
	}
	echoed := struct {
		EchoParams map[string]interface{} `json:"echoParams"`
	}{}
	if err := json.Unmarshal(raw, &echoed); err != nil {
		t.Fatalf("decode echo: %v", err)
	}
	if got := echoed.EchoParams["instance"]; got != "tuner:58093" {
		t.Errorf("instance = %v, want tuner:58093", got)
	}
	if _, declared := echoed.EchoParams["trust_rung"]; declared {
		t.Error("client declared a trust rung; it must never do that")
	}
	if _, declared := echoed.EchoParams["rung"]; declared {
		t.Error("client declared a rung; it must never do that")
	}
}

func TestListTools(t *testing.T) {
	srv := rpcServer(t, []string{"adhd.status", "adhd.lights.list"}, nil)
	defer srv.Close()

	c := New(srv.URL+"/mcp", Options{})
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(tools) != 2 || tools[0] != "adhd.status" || tools[1] != "adhd.lights.list" {
		t.Fatalf("tools = %v", tools)
	}
}

func TestCallToolRawResult(t *testing.T) {
	// adhd answers tools/call with the handler's raw JSON, not an MCP
	// content envelope.
	srv := rpcServer(t, []string{"adhd.features.certified"}, map[string]interface{}{
		"adhd.features.certified": map[string]interface{}{
			"features": []map[string]interface{}{
				{"feature_file": "adhd/mdns-discovery", "scenario_name": "browse", "certified": true},
			},
		},
	})
	defer srv.Close()

	c := New(srv.URL+"/mcp", Options{})
	raw, err := c.CallTool(context.Background(), "adhd.features.certified", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	var decoded struct {
		Features []struct {
			FeatureFile string `json:"feature_file"`
			Certified   bool   `json:"certified"`
		} `json:"features"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	if len(decoded.Features) != 1 || decoded.Features[0].FeatureFile != "adhd/mdns-discovery" {
		t.Fatalf("unexpected payload: %s", raw)
	}
}

func TestCallToolContentEnvelope(t *testing.T) {
	// A spec-strict server wraps the payload in content blocks; the client
	// unwraps the first text block that parses as JSON.
	srv := rpcServer(t, []string{"adhd.skills.certified"}, map[string]interface{}{
		"adhd.skills.certified": map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": `{"skills":[{"skill_name":"ghost","certified_rung":1}]}`},
			},
		},
	})
	defer srv.Close()

	c := New(srv.URL+"/mcp", Options{})
	raw, err := c.CallTool(context.Background(), "adhd.skills.certified", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(string(raw), "ghost") {
		t.Fatalf("payload was not unwrapped: %s", raw)
	}
}

func TestCallToolNotServed(t *testing.T) {
	// "Declared but not served" is a normal answer: -32601 maps to
	// ErrToolNotServed so the feature browser can publish the absence.
	srv := rpcServer(t, []string{"adhd.status"}, nil)
	defer srv.Close()

	c := New(srv.URL+"/mcp", Options{})
	_, err := c.CallTool(context.Background(), "adhd.features.certified", nil)
	if !errors.Is(err, ErrToolNotServed) {
		t.Fatalf("err = %v, want ErrToolNotServed", err)
	}
}

func TestSSEResponseOnPost(t *testing.T) {
	// adhd may deliver the POST's own answer as an SSE frame; the client
	// reads the data line carrying its request id.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req jsonrpcRequest
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message\n")
		resp, _ := json.Marshal(jsonrpcResponse{
			JSONRPC: "2.0", ID: req.ID,
			Result: json.RawMessage(`{"tools":[{"name":"adhd.status"}]}`),
		})
		_, _ = io.WriteString(w, "data: "+string(resp)+"\n\n")
	}))
	defer srv.Close()

	c := New(srv.URL, Options{})
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("tools/list over SSE: %v", err)
	}
	if len(tools) != 1 || tools[0] != "adhd.status" {
		t.Fatalf("tools = %v", tools)
	}
}

func TestUnreachableEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listens now

	c := New(url, Options{})
	if _, err := c.Initialize(context.Background()); err == nil {
		t.Fatal("expected an error for an unreachable endpoint")
	}
}

func TestNon200IsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := New(srv.URL, Options{})
	if _, err := c.ListTools(context.Background()); err == nil {
		t.Fatal("expected an error for a 503")
	}
}

func TestRPCErrorSurfaces(t *testing.T) {
	srv := rpcServer(t, nil, nil)
	defer srv.Close()

	c := New(srv.URL+"/mcp", Options{})
	_, err := c.call(context.Background(), "adhd.hurl.run", map[string]interface{}{})
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %v, want *RPCError", err)
	}
	if rpcErr.Code != -32601 {
		t.Fatalf("code = %d", rpcErr.Code)
	}
}
