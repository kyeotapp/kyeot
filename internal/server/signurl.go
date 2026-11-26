package server

import (
	"bytes"
	"encoding/json"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

// signEvent re-signs the attachment URLs in a person's event against the current time. The stored record is unchanged.
func (s *Server) signEvent(e protocol.Event) protocol.Event {
	if e.Source != "user" || !bytes.Contains(e.Payload, []byte(`"attachments"`)) {
		return e
	}
	var m map[string]json.RawMessage
	var atts []protocol.Attachment
	if json.Unmarshal(e.Payload, &m) != nil || json.Unmarshal(m["attachments"], &atts) != nil {
		return e
	}
	for i := range atts {
		atts[i].URL = s.fileURL(atts[i].ID)
	}
	m["attachments"], _ = json.Marshal(atts)
	if raw, err := json.Marshal(m); err == nil {
		e.Payload = raw
	}
	return e
}

func (s *Server) signEvents(es []protocol.Event) []protocol.Event {
	for i := range es {
		es[i] = s.signEvent(es[i])
	}
	return es
}
