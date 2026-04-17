package client

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
	"wert/internal/protocol"
)

// Client manages the WebSocket connection for both admin and member modes.
type Client struct {
	Send chan []byte           // TUI writes outgoing messages here
	Recv chan protocol.Envelope // TUI reads incoming messages from here

	conn     *websocket.Conn
	username string
	done     chan struct{}
}

// Connect dials the server and registers with the given username.
// If adminToken is non-empty the server will grant admin role.
func Connect(host, username, adminToken string) (*Client, error) {
	url := fmt.Sprintf("ws://%s/ws", host)
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to %s: %w", host, err)
	}

	c := &Client{
		Send: make(chan []byte, 256),
		Recv: make(chan protocol.Envelope, 256),
		conn: conn,
		username: username,
		done: make(chan struct{}),
	}

	// Register immediately.
	reg, err := protocol.NewEnvelope(protocol.MsgRegister, protocol.RegisterPayload{
		Username:   username,
		AdminToken: adminToken,
	})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.WriteMessage(websocket.TextMessage, reg); err != nil {
		conn.Close()
		return nil, fmt.Errorf("register failed: %w", err)
	}

	go c.readPump()
	go c.writePump()
	return c, nil
}

func (c *Client) Close() {
	close(c.done)
	c.conn.Close()
}

func (c *Client) readPump() {
	defer close(c.Recv)
	c.conn.SetReadDeadline(time.Time{}) // no deadline
	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var env protocol.Envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			continue
		}
		select {
		case c.Recv <- env:
		case <-c.done:
			return
		}
	}
}

func (c *Client) writePump() {
	for {
		select {
		case data := <-c.Send:
			if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

// Helper constructors that build and queue outgoing envelopes.

func (c *Client) SendChat(content string) {
	data, err := protocol.NewEnvelope(protocol.MsgChat, protocol.ChatPayload{
		Message: protocol.ChatMessage{Content: content},
	})
	if err == nil {
		c.Send <- data
	}
}

func (c *Client) SendTaskCreate(title, description, assignee, priority string) {
	data, err := protocol.NewEnvelope(protocol.MsgTaskCreate, protocol.TaskCreatePayload{
		Task: protocol.Task{
			Title:       title,
			Description: description,
			Assignee:    assignee,
			Priority:    priority,
		},
	})
	if err == nil {
		c.Send <- data
	}
}

func (c *Client) SendTaskUpdate(taskIDPrefix string, status protocol.TaskStatus) {
	data, err := protocol.NewEnvelope(protocol.MsgTaskUpdate, protocol.TaskUpdatePayload{
		TaskID:    taskIDPrefix,
		Status:    status,
		UpdatedBy: c.username,
	})
	if err == nil {
		c.Send <- data
	}
}

func (c *Client) SendTaskDelete(taskIDPrefix string) {
	data, err := protocol.NewEnvelope(protocol.MsgTaskDelete, protocol.TaskDeletePayload{
		TaskID: taskIDPrefix,
	})
	if err == nil {
		c.Send <- data
	}
}
