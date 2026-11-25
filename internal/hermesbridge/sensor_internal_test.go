package hermesbridge

import (
	"context"
	"testing"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

// The ack of a sensor data.put does not enter the one-slot queue of a request waiting at the local address; if it did, the real reply would be dropped.
func TestSensorAckSkipsUIWait(t *testing.T) {
	b := New(Config{})
	wait := make(chan protocol.ServerMessage, 1)
	b.uiWait = wait
	ok := true
	b.handle(context.Background(), protocol.ServerMessage{Type: "ui.ack", ID: "netwatch", OK: &ok, Req: sensorReq})
	if len(wait) != 0 {
		t.Fatal("sensor ack took the queue")
	}
	b.handle(context.Background(), protocol.ServerMessage{Type: "ui.ack", ID: "revenue", OK: &ok})
	if len(wait) != 1 {
		t.Fatal("a normal ack goes to the queue")
	}
}
