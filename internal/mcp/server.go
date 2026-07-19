package mcp

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"moos/kernel/internal/graph"
	"moos/kernel/internal/kernel"
)

// Server provides two MCP transports:
//   - SSE over HTTP (GET /sse + POST /message)
//   - Newline-delimited JSON-RPC on stdio (HandleStdio)
//
// Tools: graph_state, node_lookup, apply_rewrite, apply_program, operad_registry
type Server struct {
	inspect  kernel.InspectKernel
	write    kernel.WriteKernel
	mu       sync.Mutex
	sessions map[string]chan []byte // sessionId → SSE write channel

	// authToken, if non-empty, is the bearer token required to call the write
	// tools (apply_rewrite, apply_program) over the HTTP-facing MCP transports
	// (POST /message, POST /sse). Empty (the default) leaves them
	// unauthenticated. Read tools are never gated, and the stdio transport is
	// treated as local-trust (a subprocess the operator launched — no header).
	authToken string
}

func NewServer(rt *kernel.Runtime) *Server {
	return &Server{
		inspect:  rt,
		write:    rt,
		sessions: make(map[string]chan []byte),
	}
}

// SetAuthToken records the bearer token required for the MCP write tools over
// HTTP transports. Empty (default) leaves them unauthenticated. Call before
// Handler() is attached.
func (s *Server) SetAuthToken(v string) { s.authToken = v }

// isWriteCall reports whether a request is a tools/call for a mutating tool
// (apply_rewrite / apply_program) — the calls that must carry a bearer when a
// token is configured. Read tools and non-tools methods return false.
func (s *Server) isWriteCall(req Request) bool {
	if req.Method != "tools/call" {
		return false
	}
	var params ToolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return false
	}
	return params.Name == "apply_rewrite" || params.Name == "apply_program"
}

// validBearer reports whether the Authorization header carries the expected
// token, using a constant-time comparison.
func validBearer(header, token string) bool {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	got := strings.TrimSpace(header[len(prefix):])
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /sse", s.handleSSE)
	mux.HandleFunc("POST /sse", s.handleStreamableHTTP) // MCP 2025-03-26 Streamable HTTP
	mux.HandleFunc("POST /message", s.handleMessage)
	return mux
}

// handleSSE opens an SSE session. The client receives an "endpoint" event
// with the POST url to use for sending messages.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusNotImplemented)
		return
	}

	sessionID := fmt.Sprintf("s%d", time.Now().UnixNano())
	ch := make(chan []byte, 64)

	s.mu.Lock()
	s.sessions[sessionID] = ch
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.sessions, sessionID)
		s.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	// Send endpoint event — use absolute URL so all MCP clients can resolve it
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	fmt.Fprintf(w, "event: endpoint\ndata: %s://%s/message?sessionId=%s\n\n", scheme, r.Host, sessionID)
	flusher.Flush()

	for {
		select {
		case msg, open := <-ch:
			if !open {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// handleMessage receives a JSON-RPC request, dispatches it, and sends the
// response back over the SSE session channel.
func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("sessionId")

	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if s.authToken != "" && s.isWriteCall(req) && !validBearer(r.Header.Get("Authorization"), s.authToken) {
		data, _ := json.Marshal(errResponse(req.ID, RPCInvalidRequest, "unauthorized: bearer token required for write tools"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write(data)
		return
	}

	resp := s.dispatch(req)
	data, _ := json.Marshal(resp)

	if sessionID != "" {
		s.mu.Lock()
		ch, ok := s.sessions[sessionID]
		s.mu.Unlock()
		if ok {
			select {
			case ch <- data:
			default:
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// handleStreamableHTTP handles MCP 2025-03-26 Streamable HTTP transport.
// Clients (e.g. Antigravity) POST JSON-RPC directly to /sse and read the
// response from the HTTP body (no prior SSE session required).
func (s *Server) handleStreamableHTTP(w http.ResponseWriter, r *http.Request) {
	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// Notifications (no id) — acknowledge without a body.
	if req.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if s.authToken != "" && s.isWriteCall(req) && !validBearer(r.Header.Get("Authorization"), s.authToken) {
		data, _ := json.Marshal(errResponse(req.ID, RPCInvalidRequest, "unauthorized: bearer token required for write tools"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write(data)
		return
	}
	resp := s.dispatch(req)
	data, _ := json.Marshal(resp)
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// HandleStdio runs MCP over stdin/stdout using newline-delimited JSON-RPC.
func (s *Server) HandleStdio(ctx context.Context, in io.Reader, out io.Writer) {
	scanner := bufio.NewScanner(in)
	enc := json.NewEncoder(out)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if !scanner.Scan() {
			return
		}
		var req Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			enc.Encode(errResponse(nil, RPCParseError, err.Error()))
			continue
		}
		// JSON-RPC 2.0: notifications have no id — never respond to them.
		if req.ID == nil {
			continue
		}
		enc.Encode(s.dispatch(req))
	}
}

// dispatch routes a JSON-RPC request to the appropriate tool handler.
func (s *Server) dispatch(req Request) Response {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "ping":
		return okResponse(req.ID, map[string]any{})
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(req)
	default:
		// Silently ignore notification-style methods that may arrive with an id.
		if len(req.Method) > 14 && req.Method[:14] == "notifications/" {
			return okResponse(req.ID, map[string]any{})
		}
		return errResponse(req.ID, RPCMethodNotFound, "method not found: "+req.Method)
	}
}

func (s *Server) handleInitialize(req Request) Response {
	return okResponse(req.ID, InitializeResult{
		ProtocolVersion: MCPProtocolVersion,
		ServerInfo: map[string]any{
			"name":    "moos-kernel",
			"version": "1.0.0",
		},
		Capabilities: ServerCapabilities{
			Tools: &ToolsCapability{ListChanged: false},
		},
	})
}

func (s *Server) handleToolsList(req Request) Response {
	tools := []ToolDefinition{
		{
			Name:        "graph_state",
			Description: "Return the full current graph state (all nodes and relations).",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "node_lookup",
			Description: "Fetch a single node by URN.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"urn": map[string]any{"type": "string"},
				},
				"required": []string{"urn"},
			},
		},
		{
			Name:        "apply_rewrite",
			Description: "Submit a single rewrite Envelope (ADD, LINK, MUTATE, UNLINK).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"envelope": map[string]any{"type": "object"},
				},
				"required": []string{"envelope"},
			},
		},
		{
			Name:        "apply_program",
			Description: "Submit an atomic batch of rewrite Envelopes. All or nothing.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"envelopes": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "object"},
					},
				},
				"required": []string{"envelopes"},
			},
		},
		{
			Name:        "operad_registry",
			Description: "Introspect the type system: node types, WF01-WF15 rewrite categories, port colors.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
	return okResponse(req.ID, map[string]any{"tools": tools})
}

func (s *Server) handleToolsCall(req Request) Response {
	var params ToolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errResponse(req.ID, RPCInvalidParams, err.Error())
	}

	var result any
	var toolErr error

	switch params.Name {
	case "graph_state":
		result = s.inspect.State()
	case "node_lookup":
		urnStr, _ := params.Arguments["urn"].(string)
		node, ok := s.inspect.Node(graph.URN(urnStr))
		if !ok {
			return okResponse(req.ID, ToolCallResult{
				Content: []ContentBlock{{Type: "text", Text: "node not found: " + urnStr}},
				IsError: true,
			})
		}
		result = node
	case "apply_rewrite":
		envRaw, _ := json.Marshal(params.Arguments["envelope"])
		var env graph.Envelope
		if err := json.Unmarshal(envRaw, &env); err != nil {
			return errResponse(req.ID, RPCInvalidParams, err.Error())
		}
		result, toolErr = s.write.Apply(env)
	case "apply_program":
		envsRaw, _ := json.Marshal(params.Arguments["envelopes"])
		var envs []graph.Envelope
		if err := json.Unmarshal(envsRaw, &envs); err != nil {
			return errResponse(req.ID, RPCInvalidParams, err.Error())
		}
		result, toolErr = s.write.ApplyProgram(envs)
	case "operad_registry":
		result = map[string]string{"note": "use GET /operad/* HTTP routes for registry introspection"}
	default:
		return errResponse(req.ID, RPCMethodNotFound, "unknown tool: "+params.Name)
	}

	if toolErr != nil {
		text, _ := json.Marshal(map[string]string{"error": toolErr.Error()})
		return okResponse(req.ID, ToolCallResult{
			Content: []ContentBlock{{Type: "text", Text: string(text)}},
			IsError: true,
		})
	}

	text, _ := json.Marshal(result)
	return okResponse(req.ID, ToolCallResult{
		Content: []ContentBlock{{Type: "text", Text: string(text)}},
	})
}
