package wsconversation

import (
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Wire constants — deployment tuning, not contract. Bounds exist so no
// client can grow server memory unboundedly: one socket owns at most
// maxInboundFrameBytes of read surface plus outboundQueueCap queued
// frames.
const (
	maxInboundFrameBytes = 64 << 10
	outboundQueueCap     = 64
	pingPeriod           = 25 * time.Second
	pongWait             = 60 * time.Second
	writeWait            = 10 * time.Second
)

// conn is one socket. A single writer goroutine serializes every write
// (Gorilla permits one concurrent writer), fed by a bounded queue the
// hub's event path can never block on. A slow consumer that fills the
// queue is disconnected — the turn and other subscribers keep going.
type conn struct {
	hub     *Hub
	ws      *websocket.Conn
	out     chan []byte
	done    chan struct{}
	flushed chan struct{} // closed when writeLoop exits — drain completion
	closed  atomic.Bool
}

func newConn(h *Hub, ws *websocket.Conn) *conn {
	return &conn{
		hub:     h,
		ws:      ws,
		out:     make(chan []byte, outboundQueueCap),
		done:    make(chan struct{}),
		flushed: make(chan struct{}),
	}
}

// enqueue offers a frame without blocking — called only under hub.mu so
// subscribers get a consistent interleaving per turn. A full queue means
// this socket can't keep up: it's killed, not the turn.
func (c *conn) enqueue(raw []byte) {
	select {
	case <-c.done:
		return
	default:
	}
	select {
	case c.out <- raw:
	case <-c.done:
	default:
		c.kill()
	}
}

// push answers a command-level reply (error frames) — same non-blocking
// queue discipline as turn frames.
func (c *conn) push(f serverFrame) {
	if raw, ok := marshalFrame(f); ok {
		c.enqueue(raw)
	}
}

// kill tears the socket down once; the read/write loops unwind and
// dropConn removes it from every turn's subscriber set.
func (c *conn) kill() {
	if c.closed.CompareAndSwap(false, true) {
		close(c.done)
		_ = c.ws.Close()
	}
}

// shutdown drains queued frames then closes politely — the nil sentinel
// tells the writer to flush, send a close frame, and exit. A socket too
// far behind to take the sentinel is killed instead.
func (c *conn) shutdown() {
	select {
	case <-c.done:
		return
	default:
	}
	select {
	case c.out <- nil:
	default:
		c.kill()
	}
}

// readLoop is the socket's single reader. It enforces the frame size cap
// (Gorilla closes with 1009 over the limit) and the pong deadline, and
// routes each JSON command to the hub.
func (c *conn) readLoop() {
	defer func() {
		c.hub.dropConn(c)
		c.kill()
	}()
	c.ws.SetReadLimit(maxInboundFrameBytes)
	_ = c.ws.SetReadDeadline(time.Now().Add(pongWait))
	c.ws.SetPongHandler(func(string) error {
		return c.ws.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		kind, raw, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		if kind == websocket.BinaryMessage {
			c.push(errorFrame("", "invalid_argument", "binary frames are not supported"))
			// Let the writer flush the error and a polite close before
			// the deferred teardown drops the socket.
			c.shutdown()
			select {
			case <-c.flushed:
			case <-time.After(writeWait):
			}
			return
		}
		if kind != websocket.TextMessage {
			continue
		}
		c.hub.handleCommand(c, raw)
	}
}

// writeLoop is the socket's single writer: queued frames, protocol pings
// on a fixed interval, and the graceful close sentinel — each write
// under a bounded deadline.
func (c *conn) writeLoop() {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	defer close(c.flushed)
	for {
		select {
		case raw := <-c.out:
			_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			if raw == nil {
				// Drained close: tell the peer, then unwind.
				_ = c.ws.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "server shutting down"))
				return
			}
			if err := c.ws.WriteMessage(websocket.TextMessage, raw); err != nil {
				c.kill()
				return
			}
		case <-ticker.C:
			_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				c.kill()
				return
			}
		case <-c.done:
			return
		}
	}
}

// serve runs the socket inside the gin handler: reader on the handler
// goroutine (blocking until disconnect), writer alongside.
func (c *conn) serve() {
	go c.writeLoop()
	c.readLoop()
}
