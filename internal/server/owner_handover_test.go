package server_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/server"
)

type leavePlan struct {
	Others []struct {
		ID           string
		Name         string
		Admin        bool
		Rooms, Tasks int
	}
	Mine        struct{ Agents, Rooms, Tasks int }
	Host        string
	CanHandOver bool
}

func handoverHarness(t *testing.T) (*decktest.Harness, string, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath, tokFile := filepath.Join(dir, "kyeot.db"), filepath.Join(dir, "owner-token")
	if err := os.WriteFile(tokFile, []byte(decktest.OwnerToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := decktest.NewWith(t, func(o *server.Options) { o.DBPath, o.OwnerTokenFile = dbPath, tokFile })
	return h, dbPath, tokFile
}

func socketCloseCode(t *testing.T, c *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if _, _, err := c.Read(ctx); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

// The owner hands ownership to another user and deletes only their own data.
func TestHandOverOwner(t *testing.T) {
	h, dbPath, tokFile := handoverHarness(t)
	mine := h.CreateAgent("Agent A")
	room := makeRoom(t, h, "Owner room", mine.Agent.ID)
	heirTok := invite(t, h, "Alice")
	otherTok := invite(t, h, "Bob")
	var heir, other struct{ OwnerID string }
	h.APIAs(heirTok, "GET", "/api/me", nil, &heir)
	h.APIAs(otherTok, "GET", "/api/me", nil, &other)
	h.API("PATCH", "/api/owners/"+other.OwnerID, map[string]bool{"admin": true}, nil)
	shareRoom(t, h, room.ID, heirTok)

	// A post the old owner wrote in the heir's room remains as a departed user's
	var ha struct{ Agent struct{ ID string } }
	h.APIAs(heirTok, "POST", "/api/agents", map[string]string{"name": "Alice bot"}, &ha)
	var hr struct{ ID string }
	h.APIAs(heirTok, "POST", "/api/rooms", map[string]any{"name": "Alice room", "agentId": ha.Agent.ID}, &hr)
	if s := h.APIAs(decktest.OwnerToken, "POST", "/api/rooms/join", map[string]string{"code": roomCode(t, h, heirTok, hr.ID)}, nil); s != http.StatusOK {
		t.Fatalf("owner joins heir room: %d", s)
	}
	if s := h.API("POST", "/api/rooms/"+hr.ID+"/messages", map[string]any{"text": "Post left by owner", "ask": false}, nil); s != http.StatusCreated {
		t.Fatalf("owner message: %d", s)
	}

	var plan leavePlan
	if s := h.API("GET", "/api/owner/leave", nil, &plan); s != http.StatusOK {
		t.Fatalf("plan: %d", s)
	}
	if len(plan.Others) != 2 || plan.Others[0].Name != "Bob" || !plan.Others[0].Admin || plan.Others[1].Rooms != 1 || plan.Mine.Agents != 1 || plan.Mine.Rooms != 1 || !plan.CanHandOver {
		t.Fatalf("plan: %+v", plan)
	}
	// A non-owner can't see, hand over, or close
	if s := h.APIAs(otherTok, "GET", "/api/owner/leave", nil, nil); s != http.StatusForbidden {
		t.Fatalf("admin plan: %d", s)
	}
	if s := h.APIAs(otherTok, "POST", "/api/owner/handover", map[string]string{"heirId": other.OwnerID, "confirm": "지우기"}, nil); s != http.StatusForbidden {
		t.Fatalf("admin handover: %d", s)
	}
	if s := h.APIAs(otherTok, "POST", "/api/server/close", map[string]string{"confirm": "지우기"}, nil); s != http.StatusForbidden {
		t.Fatalf("admin close: %d", s)
	}
	// Without the confirm word or without an heir, nothing changes
	if s := h.API("POST", "/api/owner/handover", map[string]string{"heirId": heir.OwnerID}, nil); s != http.StatusBadRequest {
		t.Fatalf("no confirm: %d", s)
	}
	if s := h.API("POST", "/api/owner/handover", map[string]string{"heirId": "nobody", "confirm": "지우기"}, nil); s != http.StatusNotFound {
		t.Fatalf("no heir: %d", s)
	}
	if b, _ := os.ReadFile(tokFile); strings.TrimSpace(string(b)) != decktest.OwnerToken {
		t.Fatal("token file changed by a failed handover")
	}
	if s := h.API("GET", "/api/me", nil, nil); s != http.StatusOK {
		t.Fatalf("owner after failed handover: %d", s)
	}

	ownerWS := dialWS(t, h, "/client", decktest.OwnerToken)
	if s := h.API("POST", "/api/owner/handover", map[string]string{"heirId": heir.OwnerID, "confirm": "지우기"}, nil); s != http.StatusNoContent {
		t.Fatalf("handover: %d", s)
	}
	if c := socketCloseCode(t, ownerWS); c != 4003 {
		t.Fatalf("old owner socket close: %d", c)
	}

	// The old token is invalid. The new token in the file belongs to no account (the computer can't get in as the heir)
	if s := h.API("GET", "/api/me", nil, nil); s != http.StatusUnauthorized {
		t.Fatalf("old owner token: %d", s)
	}
	b, _ := os.ReadFile(tokFile)
	newTok := strings.TrimSpace(string(b))
	if newTok == decktest.OwnerToken || len(newTok) < 16 {
		t.Fatalf("token file: %q", newTok)
	}
	if s := h.APIAs(newTok, "GET", "/api/me", nil, nil); s != http.StatusUnauthorized {
		t.Fatalf("new file token should be nobody: %d", s)
	}

	// The heir: keeps their own token, role owner, one notice
	var me struct {
		OwnerID, Role, HandoverFrom, Host string
	}
	h.APIAs(heirTok, "GET", "/api/me", nil, &me)
	if me.Role != "owner" || me.HandoverFrom == "" || me.Host == "" {
		t.Fatalf("heir me: %+v", me)
	}
	h.APIAs(heirTok, "POST", "/api/me/handover-seen", nil, nil)
	me = struct{ OwnerID, Role, HandoverFrom, Host string }{}
	h.APIAs(heirTok, "GET", "/api/me", nil, &me)
	if me.HandoverFrom != "" {
		t.Fatalf("notice after seen: %+v", me)
	}
	// New owner's powers: user list (excluding self), appointing admins, removing
	var users []struct{ ID string }
	if s := h.APIAs(heirTok, "GET", "/api/owners", nil, &users); s != http.StatusOK || len(users) != 1 || users[0].ID != other.OwnerID {
		t.Fatalf("heir users: %d %+v", s, users)
	}
	if s := h.APIAs(heirTok, "PATCH", "/api/owners/"+other.OwnerID, map[string]bool{"admin": false}, nil); s != http.StatusNoContent {
		t.Fatalf("heir sets admin: %d", s)
	}
	if s := h.APIAs(otherTok, "DELETE", "/api/owners/"+heir.OwnerID, nil, nil); s != http.StatusForbidden {
		t.Fatalf("other revokes new owner: %d", s)
	}
	// The new owner can't delete the account directly via the app's "delete account" (old-version response as is)
	if s := h.APIAs(heirTok, "DELETE", "/api/me", nil, nil); s != http.StatusForbidden {
		t.Fatalf("new owner DELETE /api/me: %d", s)
	}

	// The old owner's records are gone; only posts in others' rooms remain
	if hits := rowsContaining(t, dbPath, mine.Agent.ID, room.ID); len(hits) != 0 {
		t.Fatalf("old owner rows: %v", hits)
	}
	if hits := rowsContaining(t, dbPath, "Post left by owner"); len(hits) != 1 || !strings.Contains(hits[0], "gone") {
		t.Fatalf("gone message: %v", hits)
	}
}

// A server given the owner token by environment variable (no file) blocks handover.
func TestHandOverNeedsTokenFile(t *testing.T) {
	h := decktest.New(t)
	tok := invite(t, h, "Alice")
	var heir struct{ OwnerID string }
	h.APIAs(tok, "GET", "/api/me", nil, &heir)
	var plan leavePlan
	h.API("GET", "/api/owner/leave", nil, &plan)
	if plan.CanHandOver {
		t.Fatal("canHandOver without token file")
	}
	if s := h.API("POST", "/api/owner/handover", map[string]string{"heirId": heir.OwnerID, "confirm": "지우기"}, nil); s != http.StatusConflict {
		t.Fatalf("handover without file: %d", s)
	}
	if s := h.API("GET", "/api/me", nil, nil); s != http.StatusOK {
		t.Fatalf("owner still owner: %d", s)
	}
}

// Deleting the whole server: erases everyone's records, tells all connections it closed, and stays closed after a restart.
func TestCloseServer(t *testing.T) {
	h, dbPath, _ := handoverHarness(t)
	mine := h.CreateAgent("Agent A")
	makeRoom(t, h, "Owner room", mine.Agent.ID)
	tok := invite(t, h, "Alice")
	otherWS := dialWS(t, h, "/client", tok)
	if s := h.API("POST", "/api/server/close", map[string]string{"confirm": "no"}, nil); s != http.StatusBadRequest {
		t.Fatalf("wrong confirm: %d", s)
	}
	if s := h.API("POST", "/api/server/close", map[string]string{"confirm": "지우기"}, nil); s != http.StatusNoContent {
		t.Fatalf("close: %d", s)
	}
	if c := socketCloseCode(t, otherWS); c != 4010 {
		t.Fatalf("other socket close: %d", c)
	}
	var e struct {
		Error  string
		Closed bool
	}
	if s := h.APIAs(tok, "GET", "/api/me", nil, &e); s != http.StatusGone || !e.Closed {
		t.Fatalf("after close: %d %+v", s, e)
	}
	if hits := rowsContaining(t, dbPath, mine.Agent.ID, "Alice", "Owner room"); len(hits) != 0 {
		t.Fatalf("rows left: %v", hits)
	}
	h.Close()
	h2 := decktest.NewWith(t, func(o *server.Options) { o.DBPath = dbPath })
	if s := h2.API("GET", "/api/me", nil, nil); s != http.StatusGone {
		t.Fatalf("restarted server: %d", s)
	}
}

// Responses that behaved like the old server are unchanged: the owner's DELETE /api/me gets 403 and a terminal notice.
func TestOwnerDeleteMeUnchanged(t *testing.T) {
	h := decktest.New(t)
	var e struct{ Error string }
	if s := h.API("DELETE", "/api/me", nil, &e); s != http.StatusForbidden || !strings.Contains(e.Error, "uninstall --purge") {
		t.Fatalf("owner delete: %d %q", s, e.Error)
	}
	var me map[string]any
	h.API("GET", "/api/me", nil, &me)
	if _, ok := me["handoverFrom"]; ok || me["ownerId"] != "me" || me["role"] != "owner" {
		t.Fatalf("me: %v", me)
	}
}

// After handover, running pair --rotate on the computer (swap the config token and restart) creates a new owner account and the heir becomes an admin.
func TestRotateAfterHandOver(t *testing.T) {
	h, dbPath, tokFile := handoverHarness(t)
	heirTok := invite(t, h, "Alice")
	var heir struct{ OwnerID string }
	h.APIAs(heirTok, "GET", "/api/me", nil, &heir)
	var ha decktest.Created
	h.APIAs(heirTok, "POST", "/api/agents", map[string]string{"name": "Alice bot"}, &ha)
	heirClient := h.ConnectClientAs(heirTok)
	h.StartFakeAgent(ha.Token)
	h.WaitFor(heirClient, "Alice bot idle", decktest.AgentState(ha.Agent.ID, "idle"))
	var task struct{ ID string }
	if s := h.APIAs(heirTok, "POST", "/api/tasks", map[string]any{"agentId": ha.Agent.ID, "text": "Alice's secret chat"}, &task); s != http.StatusCreated {
		t.Fatalf("heir task: %d", s)
	}
	if s := h.API("POST", "/api/owner/handover", map[string]string{"heirId": heir.OwnerID, "confirm": "지우기"}, nil); s != http.StatusNoContent {
		t.Fatalf("handover: %d", s)
	}
	b, _ := os.ReadFile(tokFile)
	fileTok := strings.TrimSpace(string(b))

	// Restarting with the same file token stays disconnected
	h.Close()
	same := decktest.NewWith(t, func(o *server.Options) { o.DBPath, o.OwnerToken, o.OwnerTokenFile = dbPath, fileTok, tokFile })
	if s := same.APIAs(fileTok, "GET", "/api/me", nil, nil); s != http.StatusUnauthorized {
		t.Fatalf("restart with handover token: %d", s)
	}

	// pair --rotate: start with a different token
	const rotated = "rotated-host-token-0123456789" // public-ok
	same.Close()
	h2 := decktest.NewWith(t, func(o *server.Options) { o.DBPath, o.OwnerToken, o.OwnerTokenFile = dbPath, rotated, tokFile })
	var host struct{ OwnerID, Role string }
	if s := h2.APIAs(rotated, "GET", "/api/me", nil, &host); s != http.StatusOK || host.Role != "owner" || host.OwnerID == heir.OwnerID || host.OwnerID == "me" {
		t.Fatalf("rotated token: %d %+v", s, host)
	}
	var tasks []struct{ ID string }
	h2.APIAs(rotated, "GET", "/api/tasks", nil, &tasks)
	for _, x := range tasks {
		if x.ID == task.ID {
			t.Fatal("new host owner sees the heir's chat")
		}
	}
	var agents []struct{ ID string }
	h2.APIAs(rotated, "GET", "/api/agents", nil, &agents)
	if len(agents) != 0 {
		t.Fatalf("new host owner sees agents: %+v", agents)
	}
	var me struct{ OwnerID, Role, HandoverFrom string }
	if s := h2.APIAs(heirTok, "GET", "/api/me", nil, &me); s != http.StatusOK || me.OwnerID != heir.OwnerID || me.Role != "admin" || me.HandoverFrom != "" {
		t.Fatalf("heir after rotate: %d %+v", s, me)
	}
	h2.APIAs(heirTok, "GET", "/api/tasks", nil, &tasks)
	if len(tasks) != 1 || tasks[0].ID != task.ID {
		t.Fatalf("heir keeps chat: %+v", tasks)
	}
	// Another restart doesn't create another account
	h2.Close()
	h3 := decktest.NewWith(t, func(o *server.Options) { o.DBPath, o.OwnerToken, o.OwnerTokenFile = dbPath, rotated, tokFile })
	var again struct{ OwnerID string }
	if h3.APIAs(rotated, "GET", "/api/me", nil, &again); again.OwnerID != host.OwnerID {
		t.Fatalf("second start: %q vs %q", again.OwnerID, host.OwnerID)
	}
}

// If one owner hands over to two people at once, only one succeeds. The loser gets 403 (owner already changed under the lock) or,
// 401 if it reached auth after the earlier handover changed the token. The token file and DB hash match, so Reclaim doesn't run even after a restart.
func TestConcurrentHandOverOnlyOneWins(t *testing.T) {
	forbidden := 0
	for round := 0; round < 8; round++ {
		h, dbPath, tokFile := handoverHarness(t)
		toks := []string{invite(t, h, "Alice"), invite(t, h, "Bob")}
		ids := make([]string, 2)
		for i, tok := range toks {
			var me struct{ OwnerID string }
			h.APIAs(tok, "GET", "/api/me", nil, &me)
			ids[i] = me.OwnerID
		}
		codes := make([]int, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[i] = h.API("POST", "/api/owner/handover", map[string]string{"heirId": ids[i], "confirm": "지우기"}, nil)
			}()
		}
		close(start)
		wg.Wait()
		win := -1
		for i, c := range codes {
			switch c {
			case http.StatusNoContent:
				if win >= 0 {
					t.Fatalf("round %d: both won %v", round, codes)
				}
				win = i
			case http.StatusForbidden:
				forbidden++
			case http.StatusUnauthorized:
			default:
				t.Fatalf("round %d: codes %v", round, codes)
			}
		}
		if win < 0 {
			t.Fatalf("round %d: nobody won %v", round, codes)
		}
		var me struct{ Role string }
		if h.APIAs(toks[win], "GET", "/api/me", nil, &me); me.Role != "owner" {
			t.Fatalf("round %d: winner role %q", round, me.Role)
		}
		if s := h.APIAs(toks[1-win], "GET", "/api/me", nil, nil); s != http.StatusOK {
			t.Fatalf("round %d: loser account should stay: %d", round, s)
		}
		b, _ := os.ReadFile(tokFile)
		fileTok := strings.TrimSpace(string(b))
		h.Close()
		again := decktest.NewWith(t, func(o *server.Options) { o.DBPath, o.OwnerToken, o.OwnerTokenFile = dbPath, fileTok, tokFile })
		if s := again.APIAs(fileTok, "GET", "/api/me", nil, nil); s != http.StatusUnauthorized {
			t.Fatalf("round %d: file token and DB hash disagree (Reclaim ran): %d", round, s)
		}
		if again.APIAs(toks[win], "GET", "/api/me", nil, &me); me.Role != "owner" {
			t.Fatalf("round %d: winner after restart %q", round, me.Role)
		}
	}
	t.Logf("403 rounds: %d/8", forbidden)
}
