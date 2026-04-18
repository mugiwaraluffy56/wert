package server

import (
	"encoding/json"
	"log"
	"sync"

	"github.com/gorilla/websocket"
	"wert/internal/protocol"
)

// client represents one connected WebSocket peer.
type client struct {
	hub        *Hub
	conn       *websocket.Conn
	send       chan []byte
	username   string
	role       string
	registered bool
}

// Hub manages all active connections and message routing.
type Hub struct {
	mu         sync.RWMutex
	clients    map[*client]bool
	register   chan *client
	unregister chan *client
	broadcast  chan []byte
	store      *Store
	adminToken string
}

func NewHub(store *Store, adminToken string) *Hub {
	return &Hub{
		clients:    make(map[*client]bool),
		register:   make(chan *client, 16),
		unregister: make(chan *client, 16),
		broadcast:  make(chan []byte, 512),
		store:      store,
		adminToken: adminToken,
	}
}

func (h *Hub) Run() {
	for {
		select {
		case c := <-h.register:
			h.mu.Lock()
			h.clients[c] = true
			h.mu.Unlock()

		case c := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				close(c.send)
				if c.registered {
					h.store.SetOnline(c.username, c.role, false)
					h.broadcastMemberEvent(protocol.MsgMemberLeave, c.username, c.role)
				}
			}
			h.mu.Unlock()

		case data := <-h.broadcast:
			h.mu.RLock()
			for c := range h.clients {
				select {
				case c.send <- data:
				default:
					// slow client — drop the connection
					close(c.send)
					delete(h.clients, c)
				}
			}
			h.mu.RUnlock()
		}
	}
}

// Broadcast sends bytes to every connected client.
func (h *Hub) Broadcast(data []byte) {
	h.broadcast <- data
}

// OnlineUsernames returns usernames of currently connected clients.
func (h *Hub) OnlineUsernames() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []string
	for c := range h.clients {
		if c.registered {
			out = append(out, c.username)
		}
	}
	return out
}

func (h *Hub) broadcastMemberEvent(t protocol.MessageType, username, role string) {
	m := protocol.Member{Username: username, Role: role, Online: t == protocol.MsgMemberJoin}
	data, err := protocol.NewEnvelope(t, protocol.MemberEventPayload{Member: m})
	if err != nil {
		return
	}
	h.broadcast <- data
}

// ---- per-client pump goroutines ----

func (c *client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
		var env protocol.Envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			log.Printf("wert/hub: bad message from %s: %v", c.username, err)
			continue
		}
		c.hub.handleEnvelope(c, env)
	}
}

func (c *client) writePump() {
	defer c.conn.Close()
	for msg := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}
	_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
}

// ---- message routing ----

func (h *Hub) handleEnvelope(c *client, env protocol.Envelope) {
	switch env.Type {

	case protocol.MsgRegister:
		var p protocol.RegisterPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.Username == "" {
			h.sendError(c, "invalid register payload")
			return
		}
		role := "member"
		if h.adminToken != "" && p.AdminToken == h.adminToken {
			role = "admin"
		}
		c.username = p.Username
		c.role = role
		c.registered = true
		h.store.SetOnline(p.Username, role, true)

		// Send full state to the new client.
		tasks := h.store.GetTasks()
		flatTasks := make([]protocol.Task, len(tasks))
		for i, t := range tasks {
			flatTasks[i] = *t
		}
		members := h.store.GetMembers()
		flatMembers := make([]protocol.Member, len(members))
		for i, m := range members {
			flatMembers[i] = *m
		}
		msgs := h.store.RecentMessages(100)
		flatMsgs := make([]protocol.ChatMessage, len(msgs))
		for i, m := range msgs {
			flatMsgs[i] = *m
		}
		syncData, err := protocol.NewEnvelope(protocol.MsgSync, protocol.SyncPayload{
			Tasks:    flatTasks,
			Members:  flatMembers,
			Messages: flatMsgs,
			Role:     role,
		})
		if err == nil {
			c.send <- syncData
		}

		// Announce to everyone else.
		h.broadcastMemberEvent(protocol.MsgMemberJoin, c.username, role)

	case protocol.MsgChat:
		if !c.registered {
			return
		}
		var p protocol.ChatPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return
		}
		p.Message.From = c.username
		msg := h.store.AddMessage(c.username, p.Message.Content)
		p.Message = *msg
		data, err := protocol.NewEnvelope(protocol.MsgChat, p)
		if err == nil {
			h.Broadcast(data)
		}

	case protocol.MsgTaskCreate:
		if !c.registered || c.role != "admin" {
			h.sendError(c, "only admins can create tasks")
			return
		}
		var p protocol.TaskCreatePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return
		}
		priority := p.Task.Priority
		if priority == "" {
			priority = "medium"
		}
		task := h.store.CreateTask(p.Task.Title, p.Task.Description, p.Task.Assignee, priority, p.Task.DueDate, c.username)
		data, err := protocol.NewEnvelope(protocol.MsgTaskCreate, protocol.TaskCreatePayload{Task: *task})
		if err == nil {
			h.Broadcast(data)
		}

	case protocol.MsgTaskUpdate:
		if !c.registered {
			return
		}
		var p protocol.TaskUpdatePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return
		}
		// Find full task ID from prefix.
		task := h.store.GetTaskByPrefix(p.TaskID)
		if task == nil {
			h.sendError(c, "task not found: "+p.TaskID)
			return
		}
		// Members can only update their own tasks.
		if c.role != "admin" && task.Assignee != c.username {
			h.sendError(c, "you can only update your own tasks")
			return
		}
		updated, ok := h.store.UpdateTaskStatus(task.ID, p.Status, c.username)
		if !ok {
			return
		}
		data, err := protocol.NewEnvelope(protocol.MsgTaskUpdate, protocol.TaskUpdatePayload{
			TaskID:    updated.ID,
			Status:    updated.Status,
			UpdatedBy: c.username,
		})
		if err == nil {
			h.Broadcast(data)
		}

	case protocol.MsgTaskDelete:
		if !c.registered || c.role != "admin" {
			h.sendError(c, "only admins can delete tasks")
			return
		}
		var p protocol.TaskDeletePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return
		}
		task := h.store.GetTaskByPrefix(p.TaskID)
		if task == nil {
			h.sendError(c, "task not found: "+p.TaskID)
			return
		}
		if h.store.DeleteTask(task.ID) {
			data, err := protocol.NewEnvelope(protocol.MsgTaskDelete, protocol.TaskDeletePayload{TaskID: task.ID})
			if err == nil {
				h.Broadcast(data)
			}
		}
	}
}

func (h *Hub) sendError(c *client, msg string) {
	data, err := protocol.NewEnvelope(protocol.MsgError, protocol.ErrorPayload{Message: msg})
	if err != nil {
		return
	}
	select {
	case c.send <- data:
	default:
	}
}
