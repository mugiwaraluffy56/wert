package server

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"wert/internal/protocol"
)

var upgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// Server wraps the Hub and HTTP mux.
type Server struct {
	hub  *Hub
	mux  *http.ServeMux
	addr string
}

func New(addr, dataFile, joinToken, adminSecret string) *Server {
	store := NewStore(dataFile)
	hub := NewHub(store, joinToken, adminSecret)
	s := &Server{hub: hub, mux: http.NewServeMux(), addr: addr}
	s.routes()
	return s
}

func (s *Server) Start() {
	go s.hub.Run()
	_ = http.ListenAndServe(s.addr, s.mux)
}

func (s *Server) routes() {
	s.mux.HandleFunc("/ws", s.handleWS)
	s.mux.HandleFunc("/api/members", s.handleAPIMembers)
	s.mux.HandleFunc("/api/tasks", s.handleAPITasks)
	s.mux.HandleFunc("/api/tasks/", s.handleAPITaskByID)
	s.mux.HandleFunc("/api/messages", s.handleAPIMessages)
	s.mux.HandleFunc("/api/watch", s.handleAPIWatch)
	s.mux.HandleFunc("/api/agents", s.handleAPIAgents)
	s.mux.HandleFunc("/api/direct", s.handleAPIDirect)
	s.mux.HandleFunc("/api/results", s.handleAPIResults)
	s.mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
}

// ---- WebSocket ----

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &client{hub: s.hub, conn: conn, send: make(chan []byte, 256)}
	s.hub.register <- c
	go c.writePump()
	c.readPump()
}

// ---- REST API (for MCP server) ----

func (s *Server) handleAPIMembers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	members := s.hub.store.GetMembers()
	writeJSON(w, members)
}

func (s *Server) handleAPITasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		tasks := s.hub.store.GetTasks()
		assignee := r.URL.Query().Get("assignee")
		status := r.URL.Query().Get("status")
		if assignee != "" || status != "" {
			filtered := tasks[:0]
			for _, t := range tasks {
				if assignee != "" && t.Assignee != assignee {
					continue
				}
				if status != "" && string(t.Status) != status {
					continue
				}
				filtered = append(filtered, t)
			}
			tasks = filtered
		}
		writeJSON(w, tasks)

	case http.MethodPost:
		var body struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Assignee    string `json:"assignee"`
			Priority    string `json:"priority"`
			DueDate     string `json:"due_date"`
			CreatedBy   string `json:"created_by"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Title == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if body.Priority == "" {
			body.Priority = "medium"
		}
		createdBy := body.CreatedBy
		if createdBy == "" {
			createdBy = "mcp"
		}
		task := s.hub.store.CreateTask(body.Title, body.Description, body.Assignee, body.Priority, body.DueDate, createdBy)
		// Broadcast to all connected clients.
		data, _ := protocol.NewEnvelope(protocol.MsgTaskCreate, protocol.TaskCreatePayload{Task: *task})
		s.hub.Broadcast(data)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, task)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAPITaskByID(w http.ResponseWriter, r *http.Request) {
	// path: /api/tasks/<id-prefix>[/claim|/unclaim]
	rest := r.URL.Path[len("/api/tasks/"):]
	if rest == "" {
		http.Error(w, "missing task id", http.StatusBadRequest)
		return
	}

	// Handle /api/tasks/{id}/claim and /api/tasks/{id}/unclaim
	if strings.HasSuffix(rest, "/claim") || strings.HasSuffix(rest, "/unclaim") {
		parts := strings.SplitN(rest, "/", 2)
		prefix := parts[0]
		action := parts[1]
		s.handleTaskClaimAction(w, r, prefix, action)
		return
	}

	prefix := rest

	switch r.Method {
	case http.MethodPut:
		var body struct {
			Status    string `json:"status"`
			UpdatedBy string `json:"updated_by"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		task := s.hub.store.GetTaskByPrefix(prefix)
		if task == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		updatedBy := body.UpdatedBy
		if updatedBy == "" {
			updatedBy = "mcp"
		}
		updated, ok := s.hub.store.UpdateTaskStatus(task.ID, protocol.TaskStatus(body.Status), updatedBy)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		data, _ := protocol.NewEnvelope(protocol.MsgTaskUpdate, protocol.TaskUpdatePayload{
			TaskID:    updated.ID,
			Status:    updated.Status,
			UpdatedBy: updatedBy,
		})
		s.hub.Broadcast(data)
		writeJSON(w, updated)

	case http.MethodDelete:
		task := s.hub.store.GetTaskByPrefix(prefix)
		if task == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if s.hub.store.DeleteTask(task.ID) {
			data, _ := protocol.NewEnvelope(protocol.MsgTaskDelete, protocol.TaskDeletePayload{TaskID: task.ID})
			s.hub.Broadcast(data)
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleTaskClaimAction(w http.ResponseWriter, r *http.Request, prefix, action string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Agent string `json:"agent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Agent == "" {
		http.Error(w, "agent field required", http.StatusBadRequest)
		return
	}
	switch action {
	case "claim":
		task, ok := s.hub.store.ClaimTask(prefix, body.Agent)
		if !ok {
			http.Error(w, "task not found or already claimed by another agent", http.StatusConflict)
			return
		}
		data, _ := protocol.NewEnvelope(protocol.MsgTaskClaim, protocol.TaskClaimPayload{
			TaskID:    task.ID,
			ClaimedBy: task.ClaimedBy,
		})
		s.hub.Broadcast(data)
		writeJSON(w, task)
	case "unclaim":
		task, ok := s.hub.store.UnclaimTask(prefix, body.Agent)
		if !ok {
			http.Error(w, "task not found or not claimed by this agent", http.StatusConflict)
			return
		}
		data, _ := protocol.NewEnvelope(protocol.MsgTaskUnclaim, protocol.TaskClaimPayload{
			TaskID:    task.ID,
			ClaimedBy: "",
		})
		s.hub.Broadcast(data)
		writeJSON(w, task)
	}
}

func (s *Server) handleAPIMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		From    string `json:"from"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Content == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.From == "" {
		body.From = "mcp"
	}
	msg := s.hub.store.AddMessage(body.From, body.Content)
	data, _ := protocol.NewEnvelope(protocol.MsgChat, protocol.ChatPayload{Message: *msg})
	s.hub.Broadcast(data)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, msg)
}

// handleAPIWatch streams Server-Sent Events for every broadcast event.
// Query param: ?filter=type1,type2 to receive only matching message types.
func (s *Server) handleAPIWatch(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	filterParam := r.URL.Query().Get("filter")
	var filterSet map[string]bool
	if filterParam != "" {
		filterSet = make(map[string]bool)
		for _, t := range strings.Split(filterParam, ",") {
			filterSet[strings.TrimSpace(t)] = true
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := make(chan []byte, 64)
	s.hub.AddSSEWatcher(ch)
	defer s.hub.RemoveSSEWatcher(ch)

	// Send initial keepalive comment.
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	ctx := r.Context()
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// heartbeat to keep connection alive
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case data := <-ch:
			if filterSet != nil {
				// Peek at type field to decide whether to forward.
				var env protocol.Envelope
				if err := json.Unmarshal(data, &env); err != nil || !filterSet[string(env.Type)] {
					continue
				}
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// handleAPIAgents manages the capability registry.
func (s *Server) handleAPIAgents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		agents := s.hub.store.GetAgents()
		writeJSON(w, agents)

	case http.MethodPost:
		var body struct {
			Name         string   `json:"name"`
			Capabilities []string `json:"capabilities"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
			http.Error(w, "name field required", http.StatusBadRequest)
			return
		}
		info := s.hub.store.RegisterAgent(body.Name, body.Capabilities)
		// Broadcast agent-online event.
		data, _ := protocol.NewEnvelope(protocol.MsgAgentOnline, protocol.AgentOnlinePayload{
			Agent:  *info,
			Online: true,
		})
		s.hub.Broadcast(data)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, info)

	case http.MethodDelete:
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
			http.Error(w, "name field required", http.StatusBadRequest)
			return
		}
		s.hub.store.UnregisterAgent(body.Name)
		data, _ := protocol.NewEnvelope(protocol.MsgAgentOnline, protocol.AgentOnlinePayload{
			Agent:  protocol.AgentInfo{Name: body.Name},
			Online: false,
		})
		s.hub.Broadcast(data)
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleAPIDirect delivers a private message from one agent to a specific recipient.
func (s *Server) handleAPIDirect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		From    string `json:"from"`
		To      string `json:"to"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Content == "" || body.To == "" {
		http.Error(w, "from, to, content required", http.StatusBadRequest)
		return
	}
	if body.From == "" {
		body.From = "agent"
	}
	msg := s.hub.store.AddAgentMessage(body.From, body.Content, "dm", body.To)
	p := protocol.DirectMsgPayload{
		ID:        msg.ID,
		From:      body.From,
		To:        body.To,
		Content:   body.Content,
		Timestamp: msg.Timestamp,
		IsAgent:   true,
	}
	data, _ := protocol.NewEnvelope(protocol.MsgDirectMsg, p)
	// Deliver only to the recipient; sender gets the REST response.
	s.hub.sendDirect(body.To, data)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, msg)
}

// handleAPIResults posts a structured AI result to the team.
func (s *Server) handleAPIResults(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Agent   string `json:"agent"`
		TaskID  string `json:"task_id"`
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Content == "" {
		http.Error(w, "content required", http.StatusBadRequest)
		return
	}
	if body.Agent == "" {
		body.Agent = "agent"
	}
	if body.Title == "" {
		body.Title = "Result"
	}
	msg := s.hub.store.AddAgentMessage(body.Agent, body.Content, "result", body.Title)
	p := protocol.AgentResultPayload{
		Agent:     body.Agent,
		TaskID:    body.TaskID,
		Title:     body.Title,
		Content:   body.Content,
		Timestamp: msg.Timestamp,
	}
	data, _ := protocol.NewEnvelope(protocol.MsgAgentResult, p)
	s.hub.Broadcast(data)
	w.WriteHeader(http.StatusCreated)
	// Return envelope ID for tracking.
	writeJSON(w, map[string]string{"id": uuid.New().String(), "message_id": msg.ID})
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// LocalIPs returns non-loopback IPv4 addresses on this machine.
func LocalIPs() []string {
	var ips []string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				ips = append(ips, ipnet.IP.String())
			}
		}
	}
	return ips
}
