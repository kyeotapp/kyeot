package server

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

const sendQueue = 256

// peer is one websocket connection. Sends are queued and a dedicated goroutine writes them in order.
type peer struct {
	conn    *websocket.Conn
	out     chan []byte
	ctx     context.Context
	cancel  context.CancelFunc
	closing atomic.Bool
}

func newPeer(parent context.Context, conn *websocket.Conn) *peer {
	ctx, cancel := context.WithCancel(parent)
	return &peer{conn: conn, out: make(chan []byte, sendQueue), ctx: ctx, cancel: cancel}
}

// send never blocks. If the queue fills, the connection is treated as slow and dropped.
func (p *peer) send(v any) {
	data, _ := json.Marshal(v)
	p.sendRaw(data)
}

func (p *peer) sendRaw(data []byte) {
	if p.closing.Load() {
		return
	}
	select {
	case p.out <- data:
	case <-p.ctx.Done():
	default:
		p.close(websocket.StatusPolicyViolation, "send queue full")
	}
}

// close does not wait (called under the hub lock). The close code is only delivered if cancel runs after the handshake.
func (p *peer) close(code websocket.StatusCode, reason string) {
	if !p.closing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		p.conn.Close(code, reason)
		p.cancel()
	}()
}

func (p *peer) writeLoop() {
	for {
		select {
		case <-p.ctx.Done():
			return
		case data := <-p.out:
			ctx, cancel := context.WithTimeout(p.ctx, 10*time.Second)
			err := p.conn.Write(ctx, websocket.MessageText, data)
			cancel()
			if err != nil {
				p.cancel()
				return
			}
		}
	}
}

// pingLoop drops unresponsive connections; when a Mac sleeps, TCP doesn't notice the break.
func (p *peer) pingLoop(every time.Duration, onPong func()) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(p.ctx, every)
			err := p.conn.Ping(ctx)
			cancel()
			if err != nil {
				p.close(websocket.StatusGoingAway, "ping timeout")
				return
			}
			if onPong != nil {
				onPong()
			}
		}
	}
}
