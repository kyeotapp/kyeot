package server_test

import (
	"encoding/binary"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/server"
)

func TestAttachmentsReachAgentAndCanBeFetched(t *testing.T) {
	h := decktest.New(t)
	a := h.CreateAgent("A")
	c := h.ConnectClient()
	h.StartFakeAgent(a.Token)
	h.WaitFor(c, "idle", decktest.AgentState(a.Agent.ID, "idle"))

	var up protocol.Attachment
	if s := h.Post("/api/uploads?name="+"../../etc/사진.png", "image/png", []byte("PNGDATA"), &up); s != http.StatusCreated {
		t.Fatalf("upload: %d", s)
	}
	if up.Name != "사진.png" || up.Mime != "image/png" || up.Size != 7 || !strings.HasPrefix(up.URL, "/api/uploads/"+up.ID+"?exp=") {
		t.Fatalf("upload meta: %+v", up)
	}
	res, err := http.Get(h.URL + up.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(body) != "PNGDATA" || res.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("fetch: %q %s", body, res.Header.Get("Content-Type"))
	}
	for _, bad := range []string{"/api/uploads/" + up.ID, "/api/uploads/" + up.ID + "?sig=0000", "/api/uploads/" + up.ID + "?sig=" + up.URL[len(up.URL)-32:] + "x", "/api/uploads/" + up.ID + "?token=" + decktest.OwnerToken} {
		if res, _ := http.Get(h.URL + bad); res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s without a valid sig: %d", bad, res.StatusCode)
		}
	}
	if res, _ := http.Get(h.URL + "/api/uploads/nope"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown id: %d", res.StatusCode)
	}

	// Sending only a file, with no text, still starts a conversation, and the agent receives the file
	var task protocol.Task
	if s := h.API("POST", "/api/tasks", map[string]any{"agentId": a.Agent.ID, "attachments": []string{up.ID}}, &task); s != http.StatusCreated {
		t.Fatalf("start with file only: %d", s)
	}
	if task.Title != "사진.png" {
		t.Fatalf("title: %q", task.Title)
	}
	var again protocol.Attachment
	h.Post("/api/uploads?name=사진.png", "image/png", []byte("PNGDATA"), &again) // an already attached file can't be attached again
	if s := h.API("POST", "/api/tasks/"+task.ID+"/messages", map[string]any{"text": "이것도", "attachments": []string{again.ID}}, nil); s != http.StatusNoContent {
		t.Fatalf("message with file: %d", s)
	}
	h.WaitFor(c, "agent saw the file", func(m protocol.ClientMessage) bool {
		return m.Type == "event" && strings.Contains(string(m.Event.Payload), "들었습니다: 이것도 (첨부: 사진.png)")
	})

	for _, bad := range []map[string]any{
		{"agentId": a.Agent.ID},
		{"agentId": a.Agent.ID, "text": "x", "attachments": []string{"not-mine"}},
	} {
		if s := h.API("POST", "/api/tasks", bad, nil); s != http.StatusBadRequest {
			t.Fatalf("%v: %d", bad, s)
		}
	}
	if s := h.Post("/api/uploads", "text/plain", nil, nil); s != http.StatusBadRequest {
		t.Fatalf("empty upload: %d", s)
	}
}

func TestTranscribeRunsLocalWhisper(t *testing.T) {
	bin := t.TempDir()
	write := func(name, script string) string {
		p := filepath.Join(bin, name)
		os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755)
		return p
	}
	convert := write("afconvert", `cp "$7" "$8"`)
	whisper := write("whisper-cli", `echo "  remind me"; echo " at nine  "`)
	h := decktest.NewWith(t, func(o *server.Options) {
		o.WhisperModel, o.WhisperBin, o.ConvertBin = "model.bin", whisper, convert
	})
	var out map[string]string
	if s := h.Post("/api/transcribe", "audio/m4a", []byte("AUDIO"), &out); s != http.StatusOK || out["text"] != "remind me at nine" {
		t.Fatalf("transcribe: %d %q", s, out["text"])
	}

	echo := write("whisper-lang", `echo "$4"`) // -m model -l language
	lh := decktest.NewWith(t, func(o *server.Options) {
		o.WhisperModel, o.WhisperBin, o.ConvertBin = "model.bin", echo, convert
	})
	for q, want := range map[string]string{"?lang=en": "en", "?lang=ko": "ko", "": "auto", "?lang=fr;rm": "auto", "?lang=fr": "auto"} {
		out = nil
		if s := lh.Post("/api/transcribe"+q, "audio/m4a", []byte("AUDIO"), &out); s != http.StatusOK || out["text"] != want {
			t.Fatalf("lang %q: %d %q", q, s, out["text"])
		}
	}

	off := decktest.New(t)
	if s := off.Post("/api/transcribe", "audio/m4a", []byte("AUDIO"), nil); s != http.StatusNotImplemented {
		t.Fatalf("without model: %d", s)
	}
}

func TestTranscribeDropsSilenceAndHallucination(t *testing.T) {
	bin := t.TempDir()
	write := func(name, script string) string {
		p := filepath.Join(bin, name)
		os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755)
		return p
	}
	wav := func(name string, amp int16) string {
		le := binary.LittleEndian
		b := []byte("RIFF\x00\x00\x00\x00WAVEfmt ")
		b = le.AppendUint32(b, 16)
		b = append(b, 1, 0, 1, 0)
		b = le.AppendUint32(b, 16000)
		b = le.AppendUint32(b, 32000)
		b = append(b, 2, 0, 16, 0)
		b = append(b, "data"...)
		b = le.AppendUint32(b, 32000)
		for i := 0; i < 16000; i++ {
			b = le.AppendUint16(b, uint16(amp*int16(1-2*(i%2))))
		}
		le.PutUint32(b[4:], uint32(len(b)-8))
		p := filepath.Join(bin, name)
		os.WriteFile(p, b, 0o644)
		return p
	}
	called := filepath.Join(bin, "called")
	for _, c := range []struct{ name, src, said, want string }{
		{"silence", wav("silent.wav", 0), "MBC 뉴스 이준범입니다.", ""},
		{"hallucination", wav("loud.wav", 3000), "MBC 뉴스 이준범입니다.", ""},
		{"speech", wav("loud2.wav", 3000), "내일 아홉 시에 알려 줘", "내일 아홉 시에 알려 줘"},
	} {
		os.Remove(called)
		convert := write("conv-"+c.name, `cp "`+c.src+`" "$8"`)
		whisper := write("whisper-"+c.name, `touch "`+called+`"; echo "`+c.said+`"`)
		h := decktest.NewWith(t, func(o *server.Options) {
			o.WhisperModel, o.WhisperBin, o.ConvertBin = "model.bin", whisper, convert
		})
		var out map[string]string
		if s := h.Post("/api/transcribe", "audio/m4a", []byte("AUDIO"), &out); s != http.StatusOK || out["text"] != c.want {
			t.Fatalf("%s: %d %q", c.name, s, out["text"])
		}
		_, err := os.Stat(called)
		if ran := err == nil; ran != (c.name != "silence") {
			t.Fatalf("%s: whisper ran=%v", c.name, ran)
		}
	}
}
