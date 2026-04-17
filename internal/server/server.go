package server

import (
	"encoding/json"
	"net"
	"net/http"

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

func New(addr, dataFile, adminToken string) *Server {
	store := NewStore(dataFile)
	hub := NewHub(store, adminToken)
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
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Title == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if body.Priority == "" {
			body.Priority = "medium"
		}
		task := s.hub.store.CreateTask(body.Title, body.Description, body.Assignee, body.Priority, "mcp")
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
	// path: /api/tasks/<id-prefix>
	prefix := r.URL.Path[len("/api/tasks/"):]
	if prefix == "" {
		http.Error(w, "missing task id", http.StatusBadRequest)
		return
	}

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
