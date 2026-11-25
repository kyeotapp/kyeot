package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/store"
)

const (
	maxUpload               = 25 << 20
	maxAudio                = 10 << 20
	defaultUploadQuota      = 2 << 30
	defaultUploadTotalQuota = 20 << 30
	maxTranscribes          = 2
	staleUpload             = 24 * time.Hour // age at which attachments not attached to a message get deleted
	sweepEvery              = time.Hour
)

// sweepUploads deletes attachments uploaded before cutoff that are not attached to a message.
func (s *Server) sweepUploads(cutoff time.Time) {
	ids, err := s.store.StaleUploads(cutoff.UnixMilli())
	if err != nil {
		log.Printf("uploads sweep: %v", err)
		return
	}
	for _, id := range ids {
		if err := os.Remove(filepath.Join(s.uploadDir(), id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("uploads sweep %s: %v", id, err)
			continue
		}
		if err := s.store.DeleteUpload(id); err != nil {
			log.Printf("uploads sweep %s: %v", id, err)
		}
	}
	if len(ids) > 0 {
		log.Printf("uploads sweep: removed %d", len(ids))
	}
}

// removeUploadFiles deletes from disk the files of attachments already removed from the DB.
func (s *Server) removeUploadFiles(why string, ids []string) {
	for _, id := range ids {
		if err := os.Remove(filepath.Join(s.uploadDir(), id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("%s: attachment %s: %v", why, id, err)
		}
	}
}

func (s *Server) sweepLoop() {
	defer s.bg.Done()
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.sweepUploads(time.Now().Add(-staleUpload))
			s.sweepLinkPreviews(time.Now())
		}
	}
}

func (s *Server) uploadDir() string {
	if s.opts.UploadDir != "" {
		return s.opts.UploadDir
	}
	return filepath.Join(filepath.Dir(s.opts.DBPath), "uploads")
}

// message is a request body carrying text and a list of attachment ids. At least one of the two is required.
type message struct {
	Text        string   `json:"text"`
	AgentID     string   `json:"agentId"`
	QuoteID     int64    `json:"quoteId,string"`
	EditID      int64    `json:"editId,string"`
	Attachments []string `json:"attachments"`
	Lang        string   `json:"lang"`
	Ask         *bool    `json:"ask"` // room message: true asks the agent, false is a people message. Defaults to true for the owner
}

func (s *Server) readMessage(r *http.Request, owner string) (message, []protocol.Attachment, error) {
	var m message
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(&m); err != nil {
		return m, nil, reject(http.StatusBadRequest, "invalid json")
	}
	m.Text = strings.TrimSpace(m.Text)
	if len(m.Attachments) > 10 {
		return m, nil, reject(http.StatusBadRequest, "attachments: 10 max")
	}
	atts := []protocol.Attachment{}
	for _, id := range m.Attachments {
		a, who, err := s.store.Upload(id)
		if errors.Is(err, store.ErrNotFound) || (err == nil && who != owner) {
			return m, nil, reject(http.StatusBadRequest, "unknown attachment "+id)
		}
		if err != nil {
			return m, nil, err
		}
		a.URL = s.fileURL(a.ID)
		atts = append(atts, a)
	}
	if m.Text == "" && len(atts) == 0 {
		return m, nil, reject(http.StatusBadRequest, "text is required")
	}
	return m, atts, nil
}

// LegacyUploadKey is the attachment signing key that versions without a key file derived from the owner token. Used to keep old attachment URLs working.
func LegacyUploadKey(ownerToken string) []byte {
	key := sha256.Sum256([]byte("agent-deck uploads\x00" + ownerToken))
	return key[:]
}

// urlTTL is how long an attachment URL stays valid. Events are re-signed on every send, so people still in the room are unaffected
const urlTTL = 24 * time.Hour

func (s *Server) sign(id string, exp int64) string {
	mac := hmac.New(sha256.New, s.opts.UploadKey)
	mac.Write([]byte(id + "|" + strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

func (s *Server) fileURL(id string) string {
	exp := time.Now().Add(urlTTL).Unix()
	return "/api/uploads/" + id + "?exp=" + strconv.FormatInt(exp, 10) + "&sig=" + s.sign(id, exp)
}

func (s *Server) signedOK(r *http.Request, id string) bool {
	exp, err := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
	return err == nil && time.Now().Unix() < exp && hmac.Equal([]byte(r.URL.Query().Get("sig")), []byte(s.sign(id, exp)))
}

func cleanName(name string) string {
	name = strings.TrimSpace(filepath.Base(strings.ReplaceAll(name, "\\", "/")))
	if name == "." || name == "/" || name == "" {
		name = "file"
	}
	if r := []rune(name); len(r) > 100 {
		name = string(r[len(r)-100:])
	}
	return name
}

func (s *Server) uploadRoutes() {
	m := s.mux

	// The body is the file itself. Name comes from ?name=, type from Content-Type.
	m.HandleFunc("POST /api/uploads", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		kind, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if kind == "" || strings.HasPrefix(kind, "multipart/") {
			kind = "application/octet-stream"
		}
		if err := os.MkdirAll(s.uploadDir(), 0o700); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(s.uploadDir(), ".part-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		size, err := io.Copy(tmp, http.MaxBytesReader(w, r.Body, maxUpload))
		tmp.Close()
		if err != nil {
			return reject(http.StatusRequestEntityTooLarge, "file too large (25MB max)")
		}
		if size == 0 {
			return reject(http.StatusBadRequest, "empty file")
		}
		used, err := s.store.UploadBytes(owner)
		if err != nil {
			return err
		}
		if used+size > s.opts.UploadQuota {
			return reject(http.StatusRequestEntityTooLarge, "보내지 않은 첨부가 너무 많아요. 먼저 보내거나 잠시 뒤에 다시 올려 주세요")
		}
		total, err := s.store.UploadTotalBytes(owner)
		if err != nil {
			return err
		}
		if total+size > s.opts.UploadTotalQuota {
			return reject(http.StatusInsufficientStorage, "첨부를 더 올릴 자리가 없어요. 첨부가 있는 대화나 글을 지우거나 서버 관리자에게 알려 주세요")
		}
		a, err := s.store.CreateUpload(owner, cleanName(r.URL.Query().Get("name")), kind, size)
		if err != nil {
			return err
		}
		if err := os.Rename(tmp.Name(), filepath.Join(s.uploadDir(), a.ID)); err != nil {
			if derr := s.store.DeleteUpload(a.ID); derr != nil {
				log.Printf("upload %s: row rollback: %v", a.ID, derr)
			}
			return err
		}
		a.URL = s.fileURL(a.ID)
		writeJSON(w, http.StatusCreated, a)
		return nil
	}))

	// The app's <Image> and the bridge can't attach headers, so they authenticate with the URL signature (sig). The owner token works too.
	m.HandleFunc("GET /api/uploads/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		a, uploader, err := s.store.Upload(id)
		owner, byToken := s.ownerOf(r)
		ok := s.signedOK(r, id) || (byToken && owner == uploader)
		if !ok { // an agent fetches files attached to its own tasks with its own token, no need to know signed URL expiry
			if ag, err := s.store.AgentByToken(bearer(r)); err == nil && s.store.AgentCanFetchUpload(id, ag) {
				ok = true
			}
		}
		if !ok || err != nil {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(filepath.Join(s.uploadDir(), a.ID))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", a.Mime)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": a.Name}))
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox")
		http.ServeContent(w, r, "", time.Time{}, f)
	})

	// Turns a recording into text. Runs on the Mac's whisper.cpp, so audio never leaves the machine.
	m.HandleFunc("POST /api/transcribe", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if s.opts.WhisperModel == "" {
			return reject(http.StatusNotImplemented, "음성 인식이 설정되지 않았어요 (KYEOT_WHISPER_MODEL)")
		}
		select {
		case s.transcribing <- struct{}{}:
			defer func() { <-s.transcribing }()
		default:
			return reject(http.StatusTooManyRequests, "음성 인식이 밀려 있어요. 잠시 뒤에 다시 해 주세요")
		}
		dir, err := os.MkdirTemp("", "kyeot-voice-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		in := filepath.Join(dir, "in")
		f, err := os.Create(in)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, http.MaxBytesReader(w, r.Body, maxAudio))
		f.Close()
		if err != nil {
			return reject(http.StatusRequestEntityTooLarge, "recording too large (10MB max)")
		}
		text, err := s.transcribe(r.Context(), in, filepath.Join(dir, "in.wav"), whisperLang(r.URL.Query().Get("lang")))
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]string{"text": text})
		return nil
	}))
}

// whisperLang maps the app language to a whisper language. Unknown values mean auto-detect
func whisperLang(l string) string {
	if l == "ko" || l == "en" {
		return l
	}
	return "auto"
}

func (s *Server) transcribe(ctx context.Context, in, wav, lang string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	run := func(name string, args ...string) ([]byte, error) {
		var out, errb bytes.Buffer
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			tail := errb.String()
			if len(tail) > 300 {
				tail = tail[len(tail)-300:]
			}
			return nil, fmt.Errorf("%s: %w: %s", filepath.Base(name), err, tail)
		}
		return out.Bytes(), nil
	}
	// Convert to 16kHz mono WAV with macOS's built-in afconvert, the format whisper.cpp accepts
	if _, err := run(cmpOr(s.opts.ConvertBin, "afconvert"), "-f", "WAVE", "-d", "LEI16@16000", "-c", "1", in, wav); err != nil {
		log.Printf("transcribe: %v", err)
		return "", reject(http.StatusBadRequest, "녹음을 읽지 못했어요")
	}
	if b, err := os.ReadFile(wav); err == nil {
		if rms, ok := peakRMS(b); ok {
			log.Printf("transcribe: peak RMS %.1f (threshold %d)", rms, quietRMS) // numbers only, to tune the threshold to real-device noise floor
			if rms < quietRMS {
				return "", nil
			}
		}
	}
	out, err := run(cmpOr(s.opts.WhisperBin, "whisper-cli"), "-m", s.opts.WhisperModel, "-l", lang, "-nt", "-np", "-f", wav)
	if err != nil {
		return "", err
	}
	text := strings.Join(strings.Fields(string(out)), " ")
	if isHallucination(text) {
		log.Printf("transcribe: hallucinated phrase filtered")
		return "", nil
	}
	return text, nil
}

func cmpOr(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
