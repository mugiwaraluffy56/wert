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
	s.mux.HandleFunc("/api/results/", s.handleAPIResultByID)
	s.mux.HandleFunc("/api/context", s.handleAPIContext)
	s.mux.HandleFunc("/api/pipelines", s.handleAPIPipelines)
	s.mux.HandleFunc("/api/pipelines/", s.handleAPIPipelineByName)
	s.mux.HandleFunc("/api/pipeline-runs", s.handleAPIPipelineRuns)
	s.mux.HandleFunc("/api/pipeline-runs/", s.handleAPIPipelineRunByID)
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

// ---- REST API ----

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
		data, _ := protocol.NewEnvelope(protocol.MsgTaskCreate, protocol.TaskCreatePayload{Task: *task})
		s.hub.Broadcast(data)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, task)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAPITaskByID(w http.ResponseWriter, r *http.Request) {
	// path: /api/tasks/<id-prefix>[/action]
	rest := r.URL.Path[len("/api/tasks/"):]
	if rest == "" {
		http.Error(w, "missing task id", http.StatusBadRequest)
		return
	}

	parts := strings.SplitN(rest, "/", 2)
	prefix := parts[0]

	if len(parts) == 2 {
		switch parts[1] {
		case "claim", "unclaim":
			s.handleTaskClaimAction(w, r, prefix, parts[1])
		case "comments":
			s.handleAPITaskComments(w, r, prefix)
		case "dependencies":
			s.handleAPITaskDependencies(w, r, prefix)
		case "handoff":
			s.handleAPITaskHandoff(w, r, prefix)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
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

func (s *Server) handleAPITaskComments(w http.ResponseWriter, r *http.Request, prefix string) {
	switch r.Method {
	case http.MethodGet:
		task := s.hub.store.GetTaskByPrefix(prefix)
		if task == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		comments := task.Comments
		if comments == nil {
			comments = []protocol.TaskComment{}
		}
		writeJSON(w, comments)

	case http.MethodPost:
		var body struct {
			Author  string `json:"author"`
			Content string `json:"content"`
			IsAgent bool   `json:"is_agent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Content == "" {
			http.Error(w, "content required", http.StatusBadRequest)
			return
		}
		if body.Author == "" {
			body.Author = "agent"
		}
		task := s.hub.store.GetTaskByPrefix(prefix)
		if task == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		comment, ok := s.hub.store.AddTaskComment(task.ID, body.Author, body.Content, body.IsAgent)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		// Broadcast updated task (upsert) so clients see new comment count.
		updatedTask := s.hub.store.GetTaskByPrefix(task.ID)
		if updatedTask != nil {
			if taskData, err := protocol.NewEnvelope(protocol.MsgTaskCreate, protocol.TaskCreatePayload{Task: *updatedTask}); err == nil {
				s.hub.Broadcast(taskData)
			}
		}
		// Broadcast comment event.
		if data, err := protocol.NewEnvelope(protocol.MsgTaskComment, protocol.TaskCommentPayload{Comment: *comment}); err == nil {
			s.hub.Broadcast(data)
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, comment)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAPITaskDependencies(w http.ResponseWriter, r *http.Request, prefix string) {
	switch r.Method {
	case http.MethodGet:
		task := s.hub.store.GetTaskByPrefix(prefix)
		if task == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		deps := task.Dependencies
		if deps == nil {
			deps = []string{}
		}
		writeJSON(w, deps)

	case http.MethodPost:
		var body struct {
			DependsOn string `json:"depends_on"` // task ID prefix of the dependency
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.DependsOn == "" {
			http.Error(w, "depends_on required", http.StatusBadRequest)
			return
		}
		task, ok := s.hub.store.AddTaskDependency(prefix, body.DependsOn)
		if !ok {
			http.Error(w, "task not found", http.StatusNotFound)
			return
		}
		// Broadcast full task update.
		if data, err := protocol.NewEnvelope(protocol.MsgTaskCreate, protocol.TaskCreatePayload{Task: *task}); err == nil {
			s.hub.Broadcast(data)
		}
		writeJSON(w, task)

	case http.MethodDelete:
		var body struct {
			DependsOn string `json:"depends_on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.DependsOn == "" {
			http.Error(w, "depends_on required", http.StatusBadRequest)
			return
		}
		task, ok := s.hub.store.RemoveTaskDependency(prefix, body.DependsOn)
		if !ok {
			http.Error(w, "task not found", http.StatusNotFound)
			return
		}
		if data, err := protocol.NewEnvelope(protocol.MsgTaskCreate, protocol.TaskCreatePayload{Task: *task}); err == nil {
			s.hub.Broadcast(data)
		}
		writeJSON(w, task)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAPITaskHandoff(w http.ResponseWriter, r *http.Request, prefix string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		From    string `json:"from"`
		To      string `json:"to"`
		Context string `json:"context"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.To == "" {
		http.Error(w, "to field required", http.StatusBadRequest)
		return
	}
	if body.From == "" {
		body.From = "agent"
	}
	task := s.hub.store.GetTaskByPrefix(prefix)
	if task == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	p := protocol.AgentHandoffPayload{
		TaskID:    task.ID,
		From:      body.From,
		To:        body.To,
		Context:   body.Context,
		Timestamp: time.Now(),
	}
	// Store as agent message so it shows in chat history.
	content := fmt.Sprintf("handed off task %s to %s", task.ID[:8], body.To)
	if body.Context != "" {
		content += ": " + body.Context
	}
	msg := s.hub.store.AddAgentMessage(body.From, content, "handoff", body.To)
	// Deliver DM to recipient.
	dmPayload := protocol.DirectMsgPayload{
		ID:        msg.ID,
		From:      body.From,
		To:        body.To,
		Content:   fmt.Sprintf("[Handoff] Task %s — %s", task.ID[:8], body.Context),
		Timestamp: msg.Timestamp,
		IsAgent:   true,
	}
	if dmData, err := protocol.NewEnvelope(protocol.MsgDirectMsg, dmPayload); err == nil {
		s.hub.sendDirect(body.To, dmData)
	}
	// Broadcast handoff event.
	if data, err := protocol.NewEnvelope(protocol.MsgAgentHandoff, p); err == nil {
		s.hub.Broadcast(data)
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, p)
}

func (s *Server) handleAPIMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		From      string `json:"from"`
		Content   string `json:"content"`
		ReplyTo   string `json:"reply_to"`
		ReplyFrom string `json:"reply_from"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Content == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.From == "" {
		body.From = "mcp"
	}
	msg := s.hub.store.AddMessage(body.From, body.Content, body.ReplyTo, body.ReplyFrom)
	data, _ := protocol.NewEnvelope(protocol.MsgChat, protocol.ChatPayload{Message: *msg})
	s.hub.Broadcast(data)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, msg)
}

// handleAPIWatch streams Server-Sent Events for every broadcast event.
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
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case data := <-ch:
			if filterSet != nil {
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

// handleAPIDirect delivers a private message to a specific recipient.
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
	s.hub.sendDirect(body.To, data)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, msg)
}

// handleAPIResults posts a structured AI result to the team.
// If pipeline_run_id is set, the server automatically advances the pipeline run.
func (s *Server) handleAPIResults(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Agent         string `json:"agent"`
		TaskID        string `json:"task_id"`
		Title         string `json:"title"`
		Content       string `json:"content"`
		PipelineRunID string `json:"pipeline_run_id"`
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
		Agent:         body.Agent,
		TaskID:        body.TaskID,
		Title:         body.Title,
		Content:       body.Content,
		Timestamp:     msg.Timestamp,
		MessageID:     msg.ID,
		PipelineRunID: body.PipelineRunID,
	}
	data, _ := protocol.NewEnvelope(protocol.MsgAgentResult, p)
	s.hub.Broadcast(data)

	// Auto-advance pipeline if this result belongs to a run.
	if body.PipelineRunID != "" {
		nextAgent, done, run, ok := s.hub.store.AdvancePipelineRun(body.PipelineRunID, body.Content)
		if ok {
			event := "advanced"
			if done {
				event = "done"
			} else {
				// DM the next agent with full context.
				stepNum := run.CurrentStep + 1 // 1-indexed for display
				dmContent := fmt.Sprintf("[Pipeline: %s  run:%s]\nStep %d/%d — task: %s\n\nPrevious result from %s:\n%s",
					run.Pipeline, run.ID[:8], stepNum, len(run.Steps), run.TaskID, body.Agent, body.Content)
				dmMsg := s.hub.store.AddAgentMessage(body.Agent, dmContent, "dm", nextAgent)
				dmPayload := protocol.DirectMsgPayload{
					ID:        dmMsg.ID,
					From:      body.Agent,
					To:        nextAgent,
					Content:   dmContent,
					Timestamp: dmMsg.Timestamp,
					IsAgent:   true,
				}
				if dmData, err := protocol.NewEnvelope(protocol.MsgDirectMsg, dmPayload); err == nil {
					s.hub.sendDirect(nextAgent, dmData)
				}
			}
			if runData, err := protocol.NewEnvelope(protocol.MsgPipelineRun, protocol.PipelineRunPayload{Run: run, Event: event}); err == nil {
				s.hub.Broadcast(runData)
			}
		}
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]string{"id": uuid.New().String(), "message_id": msg.ID})
}

// handleAPIResultByID handles /api/results/:id/react
func (s *Server) handleAPIResultByID(w http.ResponseWriter, r *http.Request) {
	rest := r.URL.Path[len("/api/results/"):]
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[1] != "react" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	msgID := parts[0]
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Reactor  string `json:"reactor"`
		Reaction string `json:"reaction"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Reaction == "" || body.Reactor == "" {
		http.Error(w, "reactor and reaction required", http.StatusBadRequest)
		return
	}
	validReactions := map[string]bool{"approve": true, "ack": true, "reject": true}
	if !validReactions[body.Reaction] {
		http.Error(w, "invalid reaction: use approve, ack, or reject", http.StatusBadRequest)
		return
	}
	if !s.hub.store.AddReaction(msgID, body.Reactor, body.Reaction) {
		http.Error(w, "message not found", http.StatusNotFound)
		return
	}
	p := protocol.ResultReactionPayload{
		MessageID: msgID,
		Reactor:   body.Reactor,
		Reaction:  body.Reaction,
		At:        time.Now(),
	}
	data, _ := protocol.NewEnvelope(protocol.MsgResultReaction, p)
	s.hub.Broadcast(data)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, p)
}

// handleAPIContext manages the shared agent scratchpad.
func (s *Server) handleAPIContext(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		key := r.URL.Query().Get("key")
		if key != "" {
			val, ok := s.hub.store.GetScratchpad(key)
			if !ok {
				http.Error(w, "key not found", http.StatusNotFound)
				return
			}
			writeJSON(w, map[string]string{"key": key, "value": val})
		} else {
			writeJSON(w, s.hub.store.GetAllScratchpad())
		}
	case http.MethodPost:
		var body struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Key == "" {
			http.Error(w, "key required", http.StatusBadRequest)
			return
		}
		s.hub.store.SetScratchpad(body.Key, body.Value)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]string{"key": body.Key, "value": body.Value})
	case http.MethodDelete:
		var body struct {
			Key string `json:"key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Key == "" {
			http.Error(w, "key required", http.StatusBadRequest)
			return
		}
		s.hub.store.DeleteScratchpad(body.Key)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleAPIPipelines manages pipeline registration.
func (s *Server) handleAPIPipelines(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.hub.store.ListPipelines())
	case http.MethodPost:
		var body struct {
			Name  string   `json:"name"`
			Steps []string `json:"steps"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" || len(body.Steps) == 0 {
			http.Error(w, "name and steps required", http.StatusBadRequest)
			return
		}
		info := s.hub.store.RegisterPipeline(body.Name, body.Steps)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, info)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleAPIPipelineByName handles DELETE and /trigger for a named pipeline.
func (s *Server) handleAPIPipelineByName(w http.ResponseWriter, r *http.Request) {
	rest := r.URL.Path[len("/api/pipelines/"):]
	parts := strings.SplitN(rest, "/", 2)
	name := parts[0]
	if name == "" {
		http.Error(w, "missing pipeline name", http.StatusBadRequest)
		return
	}

	if len(parts) == 2 && parts[1] == "trigger" {
		s.handleAPIPipelineTrigger(w, r, name)
		return
	}

	if r.Method == http.MethodDelete {
		s.hub.store.DeletePipeline(name)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Error(w, "not found", http.StatusNotFound)
}

func (s *Server) handleAPIPipelineTrigger(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pl, ok := s.hub.store.GetPipeline(name)
	if !ok {
		http.Error(w, "pipeline not found", http.StatusNotFound)
		return
	}
	var body struct {
		TaskID      string `json:"task_id"`
		Context     string `json:"context"`
		TriggeredBy string `json:"triggered_by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.TriggeredBy == "" {
		body.TriggeredBy = "agent"
	}

	// Create a run record.
	run := s.hub.store.CreatePipelineRun(name, pl.Steps, body.TaskID, body.TriggeredBy)

	// DM first agent with run context.
	if len(pl.Steps) > 0 {
		firstAgent := pl.Steps[0]
		content := fmt.Sprintf("[Pipeline: %s  run:%s]\nStep 1/%d — task: %s\n\n%s",
			name, run.ID[:8], len(pl.Steps), body.TaskID, body.Context)
		msg := s.hub.store.AddAgentMessage(body.TriggeredBy, content, "dm", firstAgent)
		dmPayload := protocol.DirectMsgPayload{
			ID:        msg.ID,
			From:      body.TriggeredBy,
			To:        firstAgent,
			Content:   content,
			Timestamp: msg.Timestamp,
			IsAgent:   true,
		}
		if dmData, err := protocol.NewEnvelope(protocol.MsgDirectMsg, dmPayload); err == nil {
			s.hub.sendDirect(firstAgent, dmData)
		}
	}

	// Broadcast run started.
	if data, err := protocol.NewEnvelope(protocol.MsgPipelineRun, protocol.PipelineRunPayload{Run: run, Event: "started"}); err == nil {
		s.hub.Broadcast(data)
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]interface{}{
		"pipeline":    name,
		"run_id":      run.ID,
		"task_id":     body.TaskID,
		"steps":       len(pl.Steps),
		"first_agent": pl.Steps[0],
	})
}

// handleAPIPipelineRuns lists all active pipeline runs.
func (s *Server) handleAPIPipelineRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.hub.store.ListPipelineRuns())
}

// handleAPIPipelineRunByID handles GET and POST /cancel for a specific run.
func (s *Server) handleAPIPipelineRunByID(w http.ResponseWriter, r *http.Request) {
	rest := r.URL.Path[len("/api/pipeline-runs/"):]
	parts := strings.SplitN(rest, "/", 2)
	runID := parts[0]
	if runID == "" {
		http.Error(w, "missing run id", http.StatusBadRequest)
		return
	}

	if len(parts) == 2 && parts[1] == "cancel" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		run, ok := s.hub.store.CancelPipelineRun(runID)
		if !ok {
			http.Error(w, "run not found or already finished", http.StatusNotFound)
			return
		}
		if data, err := protocol.NewEnvelope(protocol.MsgPipelineRun, protocol.PipelineRunPayload{Run: run, Event: "cancelled"}); err == nil {
			s.hub.Broadcast(data)
		}
		writeJSON(w, run)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	run, ok := s.hub.store.GetPipelineRun(runID)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, run)
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
