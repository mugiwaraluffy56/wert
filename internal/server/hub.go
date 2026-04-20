package server

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
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
	mu          sync.RWMutex
	clients     map[*client]bool
	register    chan *client
	unregister  chan *client
	broadcast   chan []byte
	store       *Store
	joinToken   string // join password (empty = anyone can join)
	adminSecret string // internal secret that grants admin role to the serve user
	pendingMu   sync.Mutex
	pending     map[string]*client // username → client waiting for approval
	watchMu     sync.RWMutex
	watchers    map[chan []byte]struct{} // SSE event watchers
}

func NewHub(store *Store, joinToken, adminSecret string) *Hub {
	return &Hub{
		clients:     make(map[*client]bool),
		register:    make(chan *client, 16),
		unregister:  make(chan *client, 16),
		broadcast:   make(chan []byte, 512),
		store:       store,
		joinToken:   joinToken,
		adminSecret: adminSecret,
		pending:     make(map[string]*client),
		watchers:    make(map[chan []byte]struct{}),
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
			h.broadcastToWatchers(data)
		}
	}
}

// Broadcast sends bytes to every connected client and SSE watchers.
func (h *Hub) Broadcast(data []byte) {
	h.broadcast <- data
}

// AddSSEWatcher registers a channel to receive every broadcast event.
func (h *Hub) AddSSEWatcher(ch chan []byte) {
	h.watchMu.Lock()
	h.watchers[ch] = struct{}{}
	h.watchMu.Unlock()
}

// RemoveSSEWatcher unregisters a previously added watcher.
func (h *Hub) RemoveSSEWatcher(ch chan []byte) {
	h.watchMu.Lock()
	delete(h.watchers, ch)
	h.watchMu.Unlock()
}

func (h *Hub) broadcastToWatchers(data []byte) {
	h.watchMu.RLock()
	defer h.watchMu.RUnlock()
	for ch := range h.watchers {
		select {
		case ch <- data:
		default: // slow watcher — skip, don't block
		}
	}
}

// sendDirect delivers data to a specific connected user by username.
func (h *Hub) sendDirect(to string, data []byte) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if c.registered && c.username == to {
			select {
			case c.send <- data:
				return true
			default:
			}
		}
	}
	return false
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

		// Determine role: admin secret always wins.
		role := "member"
		if p.AdminToken == h.adminSecret {
			role = "admin"
		} else if h.joinToken != "" && p.AdminToken != h.joinToken {
			// Wrong or missing join password.
			h.sendError(c, "wrong token — ask the admin for the correct join token")
			return
		}

		c.username = p.Username

		// Admin bypasses approval. Members need approval on first join.
		if role == "admin" || h.store.IsApproved(p.Username) {
			h.completeRegistration(c, role)
			return
		}

		// Hold this client pending admin approval.
		h.pendingMu.Lock()
		h.pending[p.Username] = c
		h.pendingMu.Unlock()

		// Tell the waiting client they're pending.
		if data, err := protocol.NewEnvelope(protocol.MsgJoinPending, protocol.JoinRequestPayload{Username: p.Username}); err == nil {
			c.send <- data
		}
		// Notify all admin clients.
		h.notifyAdmins(p.Username)

	case protocol.MsgJoinApprove:
		if c.role != "admin" {
			return
		}
		var p protocol.JoinApprovePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return
		}
		h.pendingMu.Lock()
		pending, ok := h.pending[p.Username]
		if ok {
			delete(h.pending, p.Username)
		}
		h.pendingMu.Unlock()
		if !ok {
			return
		}
		h.store.ApproveUser(p.Username)
		h.completeRegistration(pending, "member")

	case protocol.MsgJoinReject:
		if c.role != "admin" {
			return
		}
		var p protocol.JoinRejectPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return
		}
		h.pendingMu.Lock()
		pending, ok := h.pending[p.Username]
		if ok {
			delete(h.pending, p.Username)
		}
		h.pendingMu.Unlock()
		if !ok {
			return
		}
		h.sendError(pending, "your join request was rejected by the admin")
		pending.conn.Close()

	case protocol.MsgChat:
		if !c.registered {
			return
		}
		var p protocol.ChatPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return
		}
		p.Message.From = c.username
		msg := h.store.AddMessage(c.username, p.Message.Content, p.Message.ReplyTo, p.Message.ReplyFrom)
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

	case protocol.MsgTaskClaim:
		if !c.registered {
			return
		}
		var p protocol.TaskClaimPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return
		}
		task, ok := h.store.ClaimTask(p.TaskID, c.username)
		if !ok {
			h.sendError(c, "task not found or already claimed by another agent")
			return
		}
		data, err := protocol.NewEnvelope(protocol.MsgTaskClaim, protocol.TaskClaimPayload{
			TaskID:    task.ID,
			ClaimedBy: task.ClaimedBy,
		})
		if err == nil {
			h.Broadcast(data)
		}

	case protocol.MsgTaskUnclaim:
		if !c.registered {
			return
		}
		var p protocol.TaskClaimPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return
		}
		task, ok := h.store.UnclaimTask(p.TaskID, c.username)
		if !ok {
			h.sendError(c, "task not found or not claimed by you")
			return
		}
		data, err := protocol.NewEnvelope(protocol.MsgTaskUnclaim, protocol.TaskClaimPayload{
			TaskID:    task.ID,
			ClaimedBy: "",
		})
		if err == nil {
			h.Broadcast(data)
		}

	case protocol.MsgAgentResult:
		if !c.registered {
			return
		}
		var p protocol.AgentResultPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return
		}
		p.Agent = c.username
		p.Timestamp = time.Now()
		h.store.AddAgentMessage(c.username, p.Content, "result", p.Title)
		data, err := protocol.NewEnvelope(protocol.MsgAgentResult, p)
		if err == nil {
			h.Broadcast(data)
		}

	case protocol.MsgDirectMsg:
		if !c.registered {
			return
		}
		var p protocol.DirectMsgPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return
		}
		p.From = c.username
		p.IsAgent = true
		p.Timestamp = time.Now()
		p.ID = uuid.New().String()
		h.store.AddAgentMessage(c.username, p.Content, "dm", p.To)
		data, err := protocol.NewEnvelope(protocol.MsgDirectMsg, p)
		if err != nil {
			return
		}
		// Deliver only to recipient and echo to sender.
		h.sendDirect(p.To, data)
		select {
		case c.send <- data:
		default:
		}

	case protocol.MsgTaskComment:
		if !c.registered {
			return
		}
		var p protocol.TaskCommentPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return
		}
		// Find full task ID from prefix.
		task := h.store.GetTaskByPrefix(p.Comment.TaskID)
		if task == nil {
			h.sendError(c, "task not found: "+p.Comment.TaskID)
			return
		}
		comment, ok := h.store.AddTaskComment(task.ID, c.username, p.Comment.Content, false)
		if !ok {
			return
		}
		// Broadcast updated task so all clients see the new comment count.
		updatedTask := h.store.GetTaskByPrefix(task.ID)
		if updatedTask != nil {
			if taskData, err := protocol.NewEnvelope(protocol.MsgTaskCreate, protocol.TaskCreatePayload{Task: *updatedTask}); err == nil {
				h.Broadcast(taskData)
			}
		}
		// Also broadcast the comment event.
		p.Comment = *comment
		if data, err := protocol.NewEnvelope(protocol.MsgTaskComment, p); err == nil {
			h.Broadcast(data)
		}

	case protocol.MsgResultReaction:
		if !c.registered {
			return
		}
		var p protocol.ResultReactionPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return
		}
		p.Reactor = c.username
		p.At = time.Now()
		validReactions := map[string]bool{"approve": true, "ack": true, "reject": true}
		if !validReactions[p.Reaction] {
			h.sendError(c, "invalid reaction: use approve, ack, or reject")
			return
		}
		if !h.store.AddReaction(p.MessageID, p.Reactor, p.Reaction) {
			h.sendError(c, "message not found: "+p.MessageID)
			return
		}
		if data, err := protocol.NewEnvelope(protocol.MsgResultReaction, p); err == nil {
			h.Broadcast(data)
		}
	}
}

// completeRegistration finalises a client's join: marks registered, sends sync, broadcasts join.
func (h *Hub) completeRegistration(c *client, role string) {
	c.role = role
	c.registered = true
	h.store.SetOnline(c.username, role, true)

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
	if data, err := protocol.NewEnvelope(protocol.MsgSync, protocol.SyncPayload{
		Tasks:    flatTasks,
		Members:  flatMembers,
		Messages: flatMsgs,
		Role:     role,
	}); err == nil {
		c.send <- data
	}
	h.broadcastMemberEvent(protocol.MsgMemberJoin, c.username, role)
}

// notifyAdmins sends a MsgJoinRequest to every connected admin client.
func (h *Hub) notifyAdmins(username string) {
	data, err := protocol.NewEnvelope(protocol.MsgJoinRequest, protocol.JoinRequestPayload{Username: username})
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if c.registered && c.role == "admin" {
			select {
			case c.send <- data:
			default:
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
