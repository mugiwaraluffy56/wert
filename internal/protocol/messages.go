package protocol

import (
	"encoding/json"
	"time"
)

type MessageType string

const (
	MsgRegister    MessageType = "register"
	MsgChat        MessageType = "chat"
	MsgTaskCreate  MessageType = "task_create"
	MsgTaskUpdate  MessageType = "task_update"
	MsgTaskDelete  MessageType = "task_delete"
	MsgSync        MessageType = "sync"
	MsgMemberJoin  MessageType = "member_join"
	MsgMemberLeave MessageType = "member_leave"
	MsgError       MessageType = "error"
)

type TaskStatus string

const (
	StatusTodo       TaskStatus = "todo"
	StatusInProgress TaskStatus = "in_progress"
	StatusDone       TaskStatus = "done"
	StatusBlocked    TaskStatus = "blocked"
)

type Task struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Assignee    string     `json:"assignee"`
	Status      TaskStatus `json:"status"`
	Priority    string     `json:"priority"` // low | medium | high
	DueDate     string     `json:"due_date,omitempty"` // "2006-01-02" format, optional
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	UpdatedBy   string     `json:"updated_by"`
}

type Member struct {
	Username string    `json:"username"`
	Role     string    `json:"role"` // admin | member
	JoinedAt time.Time `json:"joined_at"`
	Online   bool      `json:"online"`
}

type ChatMessage struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// Envelope wraps every WebSocket message.
// Payload is raw JSON so receivers can decode into the correct struct.
type Envelope struct {
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

func NewEnvelope(t MessageType, payload any) ([]byte, error) {
	p, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Envelope{Type: t, Payload: p})
}

// ---- Payload structs ----

type RegisterPayload struct {
	Username   string `json:"username"`
	AdminToken string `json:"admin_token,omitempty"`
}

type ChatPayload struct {
	Message ChatMessage `json:"message"`
}

type TaskCreatePayload struct {
	Task Task `json:"task"`
}

type TaskUpdatePayload struct {
	TaskID    string     `json:"task_id"`
	Status    TaskStatus `json:"status"`
	UpdatedBy string     `json:"updated_by"`
}

type TaskDeletePayload struct {
	TaskID string `json:"task_id"`
}

type SyncPayload struct {
	Tasks    []Task        `json:"tasks"`
	Members  []Member      `json:"members"`
	Messages []ChatMessage `json:"messages"`
	Role     string        `json:"role"`
}

type MemberEventPayload struct {
	Member Member `json:"member"`
}

type ErrorPayload struct {
	Message string `json:"message"`
}
