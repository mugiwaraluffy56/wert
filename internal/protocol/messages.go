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

	// join approval flow
	MsgJoinRequest MessageType = "join_request"
	MsgJoinApprove MessageType = "join_approve"
	MsgJoinReject  MessageType = "join_reject"
	MsgJoinPending MessageType = "join_pending"

	// agent communication
	MsgTaskClaim   MessageType = "task_claim"    // task claimed by an agent
	MsgTaskUnclaim MessageType = "task_unclaim"  // task unclaimed
	MsgAgentResult MessageType = "agent_result"  // structured AI output
	MsgDirectMsg   MessageType = "direct_message" // private agent-to-agent or agent-to-human
	MsgAgentOnline MessageType = "agent_online"  // agent registered / went offline
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
	DueDate     string     `json:"due_date,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	UpdatedBy   string     `json:"updated_by"`
	ClaimedBy   string     `json:"claimed_by,omitempty"` // agent that claimed this task
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
	IsAgent   bool      `json:"is_agent,omitempty"`
	Kind      string    `json:"kind,omitempty"` // "" | "dm" | "result"
	Meta      string    `json:"meta,omitempty"` // dm: recipient name; result: title
}

// AgentInfo describes a registered AI agent.
type AgentInfo struct {
	Name         string    `json:"name"`
	Capabilities []string  `json:"capabilities"`
	RegisteredAt time.Time `json:"registered_at"`
	Online       bool      `json:"online"`
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

type JoinRequestPayload struct {
	Username string `json:"username"`
}

type JoinApprovePayload struct {
	Username string `json:"username"`
}

type JoinRejectPayload struct {
	Username string `json:"username"`
}

type TaskClaimPayload struct {
	TaskID    string `json:"task_id"`
	ClaimedBy string `json:"claimed_by"`
}

type AgentResultPayload struct {
	Agent     string    `json:"agent"`
	TaskID    string    `json:"task_id,omitempty"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

type DirectMsgPayload struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
	IsAgent   bool      `json:"is_agent,omitempty"`
}

type AgentOnlinePayload struct {
	Agent  AgentInfo `json:"agent"`
	Online bool      `json:"online"`
}
