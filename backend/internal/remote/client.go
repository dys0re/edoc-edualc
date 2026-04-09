package remote

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Client 代表一个 WebSocket 连接。
// 一个 RemoteSession 可以有多个 Client（多端观看同一 session）。
type Client struct {
	ID        string
	SessionID string
	ViewerOnly bool // true = 只读，不能发 prompt/权限响应

	conn   *websocket.Conn
	sendCh chan EventEnvelope
	once   sync.Once
	done   chan struct{}
}

func newClient(id, sessionID string, conn *websocket.Conn, viewerOnly bool) *Client {
	return &Client{
		ID:         id,
		SessionID:  sessionID,
		ViewerOnly: viewerOnly,
		conn:       conn,
		sendCh:     make(chan EventEnvelope, 64),
		done:       make(chan struct{}),
	}
}

// send 非阻塞地将事件推入发送队列。
func (c *Client) send(env EventEnvelope) {
	select {
	case c.sendCh <- env:
	case <-c.done:
	default:
		// 队列满，丢弃（客户端太慢）
	}
}

// writePump 从 sendCh 读取事件并写入 WebSocket。
// 在独立 goroutine 中运行。
func (c *Client) writePump() {
	defer c.conn.Close()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case env, ok := <-c.sendCh:
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteJSON(env); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

// close 关闭客户端。
func (c *Client) close() {
	c.once.Do(func() {
		close(c.done)
	})
}

// readPump 从 WebSocket 读取客户端消息，分发给 session。
// 在独立 goroutine 中运行，退出时调用 onClose。
func (c *Client) readPump(sess *RemoteSession, onClose func()) {
	defer func() {
		c.close()
		onClose()
	}()

	c.conn.SetReadLimit(64 * 1024)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))

		var msg ClientMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			c.send(EventEnvelope{
				Type:      "error",
				SessionID: sess.ID,
				Payload:   map[string]string{"error": fmt.Sprintf("invalid message: %v", err)},
			})
			continue
		}

		if c.ViewerOnly && msg.Type != "ping" {
			c.send(EventEnvelope{
				Type:      "error",
				SessionID: sess.ID,
				Payload:   map[string]string{"error": "viewer-only connection"},
			})
			continue
		}

		switch msg.Type {
		case "prompt":
			if msg.Prompt == "" {
				c.send(EventEnvelope{
					Type:      "error",
					SessionID: sess.ID,
					Payload:   map[string]string{"error": "prompt is empty"},
				})
				continue
			}
			if err := sess.sendPrompt(msg.Prompt); err != nil {
				c.send(EventEnvelope{
					Type:      "error",
					SessionID: sess.ID,
					Payload:   map[string]string{"error": err.Error()},
				})
			}

		case "permission_response":
			if err := sess.replyPermRequest(msg.RequestID, msg.Allow); err != nil {
				c.send(EventEnvelope{
					Type:      "error",
					SessionID: sess.ID,
					Payload:   map[string]string{"error": err.Error()},
				})
			}

		case "interrupt":
			sess.interrupt()

		case "ping":
			c.send(EventEnvelope{Type: "pong", SessionID: sess.ID})
		}
	}
}
