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
	MsgTaskClaim   MessageType = "task_claim"
	MsgTaskUnclaim MessageType = "task_unclaim"
	MsgAgentResult MessageType = "agent_result"
	MsgDirectMsg   MessageType = "direct_message"
	MsgAgentOnline MessageType = "agent_online"

	// teamwork & agent collab
	MsgTaskComment    MessageType = "task_comment"    // comment added to a task
	MsgAgentHandoff   MessageType = "agent_handoff"   // task handed off between agents
	MsgResultReaction MessageType = "result_reaction" // reaction to an agent result
	MsgPipelineEvent  MessageType = "pipeline_event"  // pipeline triggered or advanced
	MsgPipelineRun    MessageType = "pipeline_run"    // pipeline run state change
)

type TaskStatus string

const (
	StatusTodo       TaskStatus = "todo"
	StatusInProgress TaskStatus = "in_progress"
	StatusDone       TaskStatus = "done"
	StatusBlocked    TaskStatus = "blocked"
)

// TaskComment is a note attached to a task.
type TaskComment struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	Author    string    `json:"author"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
	IsAgent   bool      `json:"is_agent,omitempty"`
}

// ResultReaction is a reaction (approve/ack/reject) on an agent result message.
type ResultReaction struct {
	Reactor  string    `json:"reactor"`
	Reaction string    `json:"reaction"` // "approve" | "ack" | "reject"
	At       time.Time `json:"at"`
}

// PipelineInfo describes a registered agent pipeline.
type PipelineInfo struct {
	Name  string   `json:"name"`
	Steps []string `json:"steps"` // ordered list of agent names
}

// PipelineRun tracks the state of an active pipeline execution.
type PipelineRun struct {
	ID          string    `json:"id"`
	Pipeline    string    `json:"pipeline"`
	Steps       []string  `json:"steps"`
	CurrentStep int       `json:"current_step"` // 0-indexed; == len(steps) when done
	Status      string    `json:"status"`        // "running" | "done" | "failed" | "cancelled"
	TaskID      string    `json:"task_id,omitempty"`
	StepResults []string  `json:"step_results,omitempty"`
	StartedBy   string    `json:"started_by"`
	StartedAt   time.Time `json:"started_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Task struct {
	ID           string        `json:"id"`
	Title        string        `json:"title"`
	Description  string        `json:"description"`
	Assignee     string        `json:"assignee"`
	Status       TaskStatus    `json:"status"`
	Priority     string        `json:"priority"` // low | medium | high
	DueDate      string        `json:"due_date,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
	UpdatedBy    string        `json:"updated_by"`
	ClaimedBy    string        `json:"claimed_by,omitempty"`
	Dependencies []string      `json:"dependencies,omitempty"` // full task IDs this task depends on
	Comments     []TaskComment `json:"comments,omitempty"`
}

type Member struct {
	Username string    `json:"username"`
	Role     string    `json:"role"` // admin | member
	JoinedAt time.Time `json:"joined_at"`
	Online   bool      `json:"online"`
}

type ChatMessage struct {
	ID        string           `json:"id"`
	From      string           `json:"from"`
	Content   string           `json:"content"`
	Timestamp time.Time        `json:"timestamp"`
	IsAgent   bool             `json:"is_agent,omitempty"`
	Kind      string           `json:"kind,omitempty"`    // "" | "dm" | "result" | "handoff"
	Meta      string           `json:"meta,omitempty"`    // dm: recipient; result: title; handoff: to-agent
	ReplyTo   string           `json:"reply_to,omitempty"`   // message ID being replied to
	ReplyFrom string           `json:"reply_from,omitempty"` // sender of the replied-to message
	Reactions []ResultReaction `json:"reactions,omitempty"`
}

// AgentInfo describes a registered AI agent.
type AgentInfo struct {
	Name         string    `json:"name"`
	Capabilities []string  `json:"capabilities"`
	RegisteredAt time.Time `json:"registered_at"`
	Online       bool      `json:"online"`
}

// Envelope wraps every WebSocket message.
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
	Agent         string    `json:"agent"`
	TaskID        string    `json:"task_id,omitempty"`
	Title         string    `json:"title"`
	Content       string    `json:"content"`
	Timestamp     time.Time `json:"timestamp"`
	MessageID     string    `json:"message_id,omitempty"`      // stored ChatMessage ID, for reactions
	PipelineRunID string    `json:"pipeline_run_id,omitempty"` // if part of an autonomous pipeline run
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

type TaskCommentPayload struct {
	Comment TaskComment `json:"comment"`
}

type AgentHandoffPayload struct {
	TaskID    string    `json:"task_id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Context   string    `json:"context"`
	Timestamp time.Time `json:"timestamp"`
}

type ResultReactionPayload struct {
	MessageID string    `json:"message_id"`
	Reactor   string    `json:"reactor"`
	Reaction  string    `json:"reaction"`
	At        time.Time `json:"at"`
}

type PipelineEventPayload struct {
	Name   string `json:"name"`
	Agent  string `json:"agent"`
	TaskID string `json:"task_id,omitempty"`
	Event  string `json:"event"` // "triggered" | "done"
	Step   int    `json:"step"`
	Total  int    `json:"total"`
}

type PipelineRunPayload struct {
	Run   PipelineRun `json:"run"`
	Event string      `json:"event"` // "started" | "advanced" | "done" | "failed" | "cancelled"
}
