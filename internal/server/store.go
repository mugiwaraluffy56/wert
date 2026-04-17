package server

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"wert/internal/protocol"
)

type Store struct {
	mu       sync.RWMutex
	tasks    map[string]*protocol.Task
	messages []*protocol.ChatMessage
	members  map[string]*protocol.Member
	dataFile string
}

type diskData struct {
	Tasks    []*protocol.Task        `json:"tasks"`
	Messages []*protocol.ChatMessage `json:"messages"`
}

func NewStore(dataFile string) *Store {
	s := &Store{
		tasks:    make(map[string]*protocol.Task),
		messages: make([]*protocol.ChatMessage, 0),
		members:  make(map[string]*protocol.Member),
		dataFile: dataFile,
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
	dd := diskData{Tasks: tasks, Messages: msgs}
	data, err := json.MarshalIndent(dd, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.dataFile, data, 0o644)
}

// ---- Tasks ----

func (s *Store) CreateTask(title, description, assignee, priority, createdBy string) *protocol.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &protocol.Task{
		ID:          uuid.New().String(),
		Title:       title,
		Description: description,
		Assignee:    assignee,
		Status:      protocol.StatusTodo,
		Priority:    priority,
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
	return t, true
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
			return &cp
		}
	}
	return nil
}

// ---- Messages ----

func (s *Store) AddMessage(from, content string) *protocol.ChatMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := &protocol.ChatMessage{
		ID:        uuid.New().String(),
		From:      from,
		Content:   content,
		Timestamp: time.Now(),
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
