package server

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"wert/internal/protocol"
)

type pipeline struct {
	Name  string
	Steps []string
}

type pipelineRun struct {
	ID          string
	Pipeline    string
	Steps       []string
	CurrentStep int    // index of the step currently executing
	Status      string // "running" | "done" | "failed" | "cancelled"
	TaskID      string
	StepResults []string
	StartedBy   string
	StartedAt   time.Time
	UpdatedAt   time.Time
}

func (r *pipelineRun) toProtocol() protocol.PipelineRun {
	results := make([]string, len(r.StepResults))
	copy(results, r.StepResults)
	steps := make([]string, len(r.Steps))
	copy(steps, r.Steps)
	return protocol.PipelineRun{
		ID:          r.ID,
		Pipeline:    r.Pipeline,
		Steps:       steps,
		CurrentStep: r.CurrentStep,
		Status:      r.Status,
		TaskID:      r.TaskID,
		StepResults: results,
		StartedBy:   r.StartedBy,
		StartedAt:   r.StartedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

type Store struct {
	mu           sync.RWMutex
	tasks        map[string]*protocol.Task
	messages     []*protocol.ChatMessage
	members      map[string]*protocol.Member
	approved     map[string]bool
	agents       map[string]*protocol.AgentInfo // registered AI agents (not persisted)
	scratchpad   map[string]string              // shared key-value store for agents
	pipelines    map[string]*pipeline           // registered agent pipelines (not persisted)
	pipelineRuns map[string]*pipelineRun        // active pipeline run state (not persisted)
	dataFile     string
}

type diskData struct {
	Tasks         []*protocol.Task        `json:"tasks"`
	Messages      []*protocol.ChatMessage `json:"messages"`
	ApprovedUsers []string               `json:"approved_users,omitempty"`
	Scratchpad    map[string]string      `json:"scratchpad,omitempty"`
}

func NewStore(dataFile string) *Store {
	s := &Store{
		tasks:        make(map[string]*protocol.Task),
		messages:     make([]*protocol.ChatMessage, 0),
		members:      make(map[string]*protocol.Member),
		approved:     make(map[string]bool),
		agents:       make(map[string]*protocol.AgentInfo),
		scratchpad:   make(map[string]string),
		pipelines:    make(map[string]*pipeline),
		pipelineRuns: make(map[string]*pipelineRun),
		dataFile:     dataFile,
	}
	s.load()
	return s
}

func (s *Store) load() {
	data, err := os.ReadFile(s.dataFile)
	if err != nil {
		return
	}
	var dd diskData
	if err := json.Unmarshal(data, &dd); err != nil {
		return
	}
	for _, t := range dd.Tasks {
		s.tasks[t.ID] = t
	}
	if dd.Messages != nil {
		s.messages = dd.Messages
	}
	for _, u := range dd.ApprovedUsers {
		s.approved[u] = true
	}
	if dd.Scratchpad != nil {
		s.scratchpad = dd.Scratchpad
	}
}

func (s *Store) persist() {
	tasks := make([]*protocol.Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		tasks = append(tasks, t)
	}
	msgs := s.messages
	if len(msgs) > 1000 {
		msgs = msgs[len(msgs)-1000:]
	}
	approved := make([]string, 0, len(s.approved))
	for u := range s.approved {
		approved = append(approved, u)
	}
	dd := diskData{
		Tasks:         tasks,
		Messages:      msgs,
		ApprovedUsers: approved,
		Scratchpad:    s.scratchpad,
	}
	data, err := json.MarshalIndent(dd, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.dataFile, data, 0o644)
}

// ---- Tasks ----

func (s *Store) CreateTask(title, description, assignee, priority, dueDate, createdBy string) *protocol.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &protocol.Task{
		ID:          uuid.New().String(),
		Title:       title,
		Description: description,
		Assignee:    assignee,
		Status:      protocol.StatusTodo,
		Priority:    priority,
		DueDate:     dueDate,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		UpdatedBy:   createdBy,
	}
	s.tasks[t.ID] = t
	go s.persist()
	return t
}

func (s *Store) UpdateTaskStatus(taskID string, status protocol.TaskStatus, updatedBy string) (*protocol.Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return nil, false
	}
	t.Status = status
	t.UpdatedAt = time.Now()
	t.UpdatedBy = updatedBy
	go s.persist()
	cp := *t
	return &cp, true
}

func (s *Store) DeleteTask(taskID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[taskID]; !ok {
		return false
	}
	delete(s.tasks, taskID)
	go s.persist()
	return true
}

func (s *Store) GetTasks() []*protocol.Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tasks := make([]*protocol.Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		cp := *t
		if cp.Comments != nil {
			cps := make([]protocol.TaskComment, len(cp.Comments))
			copy(cps, cp.Comments)
			cp.Comments = cps
		}
		if cp.Dependencies != nil {
			deps := make([]string, len(cp.Dependencies))
			copy(deps, cp.Dependencies)
			cp.Dependencies = deps
		}
		tasks = append(tasks, &cp)
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})
	return tasks
}

func (s *Store) GetTaskByPrefix(prefix string) *protocol.Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for id, t := range s.tasks {
		if len(id) >= len(prefix) && id[:len(prefix)] == prefix {
			cp := *t
			if cp.Comments != nil {
				cps := make([]protocol.TaskComment, len(cp.Comments))
				copy(cps, cp.Comments)
				cp.Comments = cps
			}
			if cp.Dependencies != nil {
				deps := make([]string, len(cp.Dependencies))
				copy(deps, cp.Dependencies)
				cp.Dependencies = deps
			}
			return &cp
		}
	}
	return nil
}

// AddTaskComment appends a comment to a task identified by full ID. Returns the comment and ok.
func (s *Store) AddTaskComment(taskID, author, content string, isAgent bool) (*protocol.TaskComment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return nil, false
	}
	c := protocol.TaskComment{
		ID:        uuid.New().String(),
		TaskID:    taskID,
		Author:    author,
		Content:   content,
		Timestamp: time.Now(),
		IsAgent:   isAgent,
	}
	t.Comments = append(t.Comments, c)
	t.UpdatedAt = time.Now()
	go s.persist()
	cp := c
	return &cp, true
}

// AddTaskDependency adds a dependency to a task (both looked up by prefix). Returns updated task.
func (s *Store) AddTaskDependency(taskPrefix, depPrefix string) (*protocol.Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var task, dep *protocol.Task
	for id, t := range s.tasks {
		if len(id) >= len(taskPrefix) && id[:len(taskPrefix)] == taskPrefix {
			task = t
		}
		if len(id) >= len(depPrefix) && id[:len(depPrefix)] == depPrefix {
			dep = t
		}
	}
	if task == nil || dep == nil {
		return nil, false
	}
	// Avoid duplicate.
	for _, d := range task.Dependencies {
		if d == dep.ID {
			cp := *task
			return &cp, true
		}
	}
	task.Dependencies = append(task.Dependencies, dep.ID)
	task.UpdatedAt = time.Now()
	go s.persist()
	cp := *task
	return &cp, true
}

// RemoveTaskDependency removes a dependency from a task (both looked up by prefix).
func (s *Store) RemoveTaskDependency(taskPrefix, depPrefix string) (*protocol.Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var task *protocol.Task
	var depID string
	for id, t := range s.tasks {
		if len(id) >= len(taskPrefix) && id[:len(taskPrefix)] == taskPrefix {
			task = t
		}
		if len(id) >= len(depPrefix) && id[:len(depPrefix)] == depPrefix {
			depID = id
		}
	}
	if task == nil {
		return nil, false
	}
	filtered := task.Dependencies[:0]
	for _, d := range task.Dependencies {
		if d != depID {
			filtered = append(filtered, d)
		}
	}
	task.Dependencies = filtered
	task.UpdatedAt = time.Now()
	go s.persist()
	cp := *task
	return &cp, true
}

// ---- Messages ----

func (s *Store) AddMessage(from, content, replyTo, replyFrom string) *protocol.ChatMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := &protocol.ChatMessage{
		ID:        uuid.New().String(),
		From:      from,
		Content:   content,
		Timestamp: time.Now(),
		ReplyTo:   replyTo,
		ReplyFrom: replyFrom,
	}
	s.messages = append(s.messages, msg)
	go s.persist()
	return msg
}

func (s *Store) AddAgentMessage(from, content, kind, meta string) *protocol.ChatMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := &protocol.ChatMessage{
		ID:        uuid.New().String(),
		From:      from,
		Content:   content,
		Timestamp: time.Now(),
		IsAgent:   true,
		Kind:      kind,
		Meta:      meta,
	}
	s.messages = append(s.messages, msg)
	go s.persist()
	return msg
}

func (s *Store) RecentMessages(n int) []*protocol.ChatMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.messages) <= n {
		out := make([]*protocol.ChatMessage, len(s.messages))
		copy(out, s.messages)
		return out
	}
	out := make([]*protocol.ChatMessage, n)
	copy(out, s.messages[len(s.messages)-n:])
	return out
}

// AddReaction adds or updates a reaction on a message. Returns false if message not found.
func (s *Store) AddReaction(msgID, reactor, reaction string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, msg := range s.messages {
		if msg.ID == msgID {
			for i, r := range msg.Reactions {
				if r.Reactor == reactor {
					msg.Reactions[i].Reaction = reaction
					msg.Reactions[i].At = time.Now()
					go s.persist()
					return true
				}
			}
			msg.Reactions = append(msg.Reactions, protocol.ResultReaction{
				Reactor:  reactor,
				Reaction: reaction,
				At:       time.Now(),
			})
			go s.persist()
			return true
		}
	}
	return false
}

// GetMessageByPrefix returns the first message whose ID starts with prefix.
func (s *Store) GetMessageByPrefix(prefix string) *protocol.ChatMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, msg := range s.messages {
		if strings.HasPrefix(msg.ID, prefix) {
			cp := *msg
			return &cp
		}
	}
	return nil
}

// ---- Members ----

func (s *Store) SetOnline(username, role string, online bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.members[username]; ok {
		m.Online = online
	} else {
		s.members[username] = &protocol.Member{
			Username: username,
			Role:     role,
			JoinedAt: time.Now(),
			Online:   online,
		}
	}
}

func (s *Store) ClaimTask(prefix, agent string) (*protocol.Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, t := range s.tasks {
		if len(id) >= len(prefix) && id[:len(prefix)] == prefix {
			if t.ClaimedBy != "" && t.ClaimedBy != agent {
				return nil, false
			}
			t.ClaimedBy = agent
			t.UpdatedAt = time.Now()
			go s.persist()
			cp := *t
			return &cp, true
		}
	}
	return nil, false
}

func (s *Store) UnclaimTask(prefix, agent string) (*protocol.Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, t := range s.tasks {
		if len(id) >= len(prefix) && id[:len(prefix)] == prefix {
			if t.ClaimedBy != "" && t.ClaimedBy != agent {
				return nil, false
			}
			t.ClaimedBy = ""
			t.UpdatedAt = time.Now()
			go s.persist()
			cp := *t
			return &cp, true
		}
	}
	return nil, false
}

// ---- Agents ----

func (s *Store) RegisterAgent(name string, caps []string) *protocol.AgentInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if caps == nil {
		caps = []string{}
	}
	info := &protocol.AgentInfo{
		Name:         name,
		Capabilities: caps,
		RegisteredAt: time.Now(),
		Online:       true,
	}
	s.agents[name] = info
	return info
}

func (s *Store) UnregisterAgent(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.agents, name)
}

func (s *Store) GetAgents() []*protocol.AgentInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*protocol.AgentInfo, 0, len(s.agents))
	for _, a := range s.agents {
		cp := *a
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ---- Scratchpad ----

func (s *Store) SetScratchpad(key, value string) {
	s.mu.Lock()
	s.scratchpad[key] = value
	s.mu.Unlock()
	go s.persist()
}

func (s *Store) GetScratchpad(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.scratchpad[key]
	return v, ok
}

func (s *Store) DeleteScratchpad(key string) {
	s.mu.Lock()
	delete(s.scratchpad, key)
	s.mu.Unlock()
	go s.persist()
}

func (s *Store) GetAllScratchpad() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.scratchpad))
	for k, v := range s.scratchpad {
		out[k] = v
	}
	return out
}

// ---- Pipelines ----

func (s *Store) RegisterPipeline(name string, steps []string) *protocol.PipelineInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pipelines[name] = &pipeline{Name: name, Steps: steps}
	return &protocol.PipelineInfo{Name: name, Steps: steps}
}

func (s *Store) GetPipeline(name string) (*protocol.PipelineInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.pipelines[name]
	if !ok {
		return nil, false
	}
	return &protocol.PipelineInfo{Name: p.Name, Steps: p.Steps}, true
}

func (s *Store) ListPipelines() []*protocol.PipelineInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*protocol.PipelineInfo, 0, len(s.pipelines))
	for _, p := range s.pipelines {
		out = append(out, &protocol.PipelineInfo{Name: p.Name, Steps: p.Steps})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Store) DeletePipeline(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pipelines, name)
}

// ---- Pipeline Runs ----

func (s *Store) CreatePipelineRun(pipelineName string, steps []string, taskID, startedBy string) protocol.PipelineRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := &pipelineRun{
		ID:          uuid.New().String(),
		Pipeline:    pipelineName,
		Steps:       steps,
		CurrentStep: 0,
		Status:      "running",
		TaskID:      taskID,
		StepResults: []string{},
		StartedBy:   startedBy,
		StartedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	s.pipelineRuns[r.ID] = r
	return r.toProtocol()
}

func (s *Store) GetPipelineRun(id string) (protocol.PipelineRun, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.pipelineRuns[id]
	if !ok {
		return protocol.PipelineRun{}, false
	}
	return r.toProtocol(), true
}

// AdvancePipelineRun appends stepResult for the completed step and advances to the next.
// Returns (nextAgent, isDone, run, ok). nextAgent is "" when isDone is true.
func (s *Store) AdvancePipelineRun(id, stepResult string) (nextAgent string, done bool, run protocol.PipelineRun, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, exists := s.pipelineRuns[id]
	if !exists || r.Status != "running" {
		return "", false, protocol.PipelineRun{}, false
	}
	r.StepResults = append(r.StepResults, stepResult)
	r.CurrentStep++
	r.UpdatedAt = time.Now()
	if r.CurrentStep >= len(r.Steps) {
		r.Status = "done"
		return "", true, r.toProtocol(), true
	}
	return r.Steps[r.CurrentStep], false, r.toProtocol(), true
}

func (s *Store) CancelPipelineRun(id string) (protocol.PipelineRun, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.pipelineRuns[id]
	if !ok {
		return protocol.PipelineRun{}, false
	}
	r.Status = "cancelled"
	r.UpdatedAt = time.Now()
	return r.toProtocol(), true
}

func (s *Store) FailPipelineRun(id string) (protocol.PipelineRun, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.pipelineRuns[id]
	if !ok {
		return protocol.PipelineRun{}, false
	}
	r.Status = "failed"
	r.UpdatedAt = time.Now()
	return r.toProtocol(), true
}

func (s *Store) ListPipelineRuns() []protocol.PipelineRun {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]protocol.PipelineRun, 0, len(s.pipelineRuns))
	for _, r := range s.pipelineRuns {
		out = append(out, r.toProtocol())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// ---- Approval ----

func (s *Store) IsApproved(username string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.approved[username]
}

func (s *Store) ApproveUser(username string) {
	s.mu.Lock()
	s.approved[username] = true
	s.mu.Unlock()
	go s.persist()
}

func (s *Store) GetMembers() []*protocol.Member {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*protocol.Member, 0, len(s.members))
	for _, m := range s.members {
		cp := *m
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Username < out[j].Username
	})
	return out
}
