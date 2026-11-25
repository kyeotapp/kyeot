package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/store"
	"github.com/kyeotapp/kyeot/internal/uicatalog"
)

type uiState struct {
	Screens []store.ScreenRow        `json:"screens"`
	Tabs    []string                 `json:"tabs"`
	Hidden  []string                 `json:"hidden"`
	Locked  []string                 `json:"locked"`
	Data    map[string]store.DataRow `json:"data"`
	Parts   []store.ScreenRow        `json:"parts"` // raw assembly parts
	Order   []string                 `json:"order"` // off-tab order
}

// The lowercase methods below expect the caller to hold h.mu.

// ui holds screens and tab layout. For an agent with neither a spec nor a layout, the first-tab (FirstTabs) template is filled in first.
func (h *hub) ui(agentID string) (uiState, error) {
	_, set, err := h.store.Tabs(agentID)
	if err != nil {
		return uiState{}, err
	}
	screens, err := h.store.Screens(agentID)
	if err != nil {
		return uiState{}, err
	}
	if !set && len(screens) == 0 {
		ids := []string{}
		for _, s := range uicatalog.Starters() {
			if !slices.Contains(uicatalog.FirstTabs(), s.ID) {
				continue
			}
			if _, err := h.store.PutScreen(agentID, s.ID, s.Raw); err != nil {
				return uiState{}, err
			}
			ids = append(ids, s.ID)
		}
		if err := h.store.SetTabs(agentID, ids); err != nil {
			return uiState{}, err
		}
	}
	tabs, _, err := h.store.Tabs(agentID)
	if err != nil {
		return uiState{}, err
	}
	if screens, err = h.store.Screens(agentID); err != nil {
		return uiState{}, err
	}
	data, err := h.store.Data(agentID)
	if err != nil {
		return uiState{}, err
	}
	h.withSensors(agentID, data)
	parts, err := h.store.Parts(agentID)
	if err != nil {
		return uiState{}, err
	}
	order := slices.DeleteFunc(h.store.OffOrder(agentID), func(id string) bool { return !hasScreen(screens, id) })
	return uiState{screens, tabs, h.store.Hidden(agentID), h.store.Locked(agentID), data, parts, order}, nil
}

// parts maps an agent's assembly parts by id. Stored parts have already been validated.
func (h *hub) parts(agentID string) (map[string]uicatalog.Part, error) {
	rows, err := h.store.Parts(agentID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]uicatalog.Part, len(rows))
	for _, r := range rows {
		if p, err := uicatalog.ValidatePart(r.Spec); err == nil {
			out[r.ID] = p
		}
	}
	return out, nil
}

// expanded is the screen to give the app, with use expanded into assembly parts.
func (h *hub) expanded(agentID string, screens []store.ScreenRow) ([]store.ScreenRow, error) {
	parts, err := h.parts(agentID)
	if err != nil || len(parts) == 0 {
		return screens, err
	}
	out := slices.Clone(screens)
	for i := range out {
		out[i].Spec = uicatalog.ExpandStored(out[i].Spec, parts)
	}
	return out, nil
}

// partUsers maps each assembly part id to the ids of screens that use it.
func partUsers(screens []store.ScreenRow) map[string][]string {
	out := map[string][]string{}
	for _, s := range screens {
		for _, id := range uicatalog.Uses(s.Spec) {
			out[id] = append(out[id], s.ID)
		}
	}
	return out
}

func hasScreen(rows []store.ScreenRow, id string) bool {
	return slices.ContainsFunc(rows, func(r store.ScreenRow) bool { return r.ID == id })
}

// putScreen validates and stacks a new revision. A newly created screen goes at the end of the layout if a tab slot is free.
// putScreen stores the spec and positions it by place. note is not a rejection but something to report (tabs full, placed off-tab).
func (h *hub) putScreen(ownerID, agentID string, raw []byte, place string) (id, note string, err error) {
	if place != "" && !slices.Contains(protocol.Places, any(place)) {
		return "", "", reject(http.StatusBadRequest, "place 는 tab, off, hidden 중 하나")
	}
	parts, err := h.parts(agentID)
	if err != nil {
		return "", "", err
	}
	exp, s, err := uicatalog.Expand(raw, parts)
	if err != nil {
		return s.ID, "", reject(http.StatusBadRequest, err.Error())
	}
	raw, iconNote := uicatalog.FitIcon(raw)
	var spec bytes.Buffer
	if err := json.Compact(&spec, raw); err != nil {
		return s.ID, "", reject(http.StatusBadRequest, "invalid json")
	}
	screens, err := h.store.Screens(agentID)
	if err != nil {
		return s.ID, "", err
	}
	existed := hasScreen(screens, s.ID)
	if !existed && len(screens) >= uicatalog.MaxScreens() {
		return s.ID, "", reject(http.StatusBadRequest, "화면은 "+strconv.Itoa(uicatalog.MaxScreens())+"개까지")
	}
	if _, err := h.store.PutScreen(agentID, s.ID, spec.Bytes()); err != nil {
		return s.ID, "", err
	}
	if note, err = h.placeScreen(agentID, s.ID, place, existed); err != nil {
		return s.ID, "", err
	}
	// Saved. Report changed icons and values that will look different in the app along with ok
	notes := append([]string{note, iconNote}, uicatalog.Warnings(exp)...)
	note = strings.Join(slices.DeleteFunc(notes, func(s string) bool { return s == "" }), " / ")
	h.toClients(ownerID, protocol.ClientMessage{Type: "ui", AgentID: agentID})
	return s.ID, note, nil
}

const tabsFull = "탭이 다 차서 탭 밖에 두었다"

// placeScreen puts a screen in one of: tab bar, off-tab, hidden.
// If place is empty, only a new screen goes at the end of the tabs when a slot is free. An existing screen keeps its place (so a rename alone doesn't make it jump).
func (h *hub) placeScreen(agentID, id, place string, existed bool) (string, error) {
	tabs, _, err := h.store.Tabs(agentID)
	if err != nil {
		return "", err
	}
	inTabs := slices.Contains(tabs, id)
	room := len(tabs) < uicatalog.MaxVisibleTabs()
	without := slices.DeleteFunc(slices.Clone(tabs), func(x string) bool { return x == id })
	switch place {
	case "":
		if existed || inTabs {
			return "", nil
		}
		if !room {
			return tabsFull, nil
		}
		return "", h.store.SetTabs(agentID, append(tabs, id))
	case "tab":
		if err := h.unhide(agentID, id); err != nil || inTabs {
			return "", err
		}
		if !room {
			return tabsFull, nil
		}
		return "", h.store.SetTabs(agentID, append(tabs, id))
	case "off":
		if inTabs {
			if err := h.store.SetTabs(agentID, without); err != nil {
				return "", err
			}
		}
		return "", h.unhide(agentID, id)
	case "hidden":
		if inTabs {
			if err := h.store.SetTabs(agentID, without); err != nil {
				return "", err
			}
		}
		if hidden := h.store.Hidden(agentID); !slices.Contains(hidden, id) {
			return "", h.store.SetHidden(agentID, append(hidden, id))
		}
	}
	return "", nil
}

func (h *hub) setTabs(ownerID, agentID string, tabs []string) error {
	if len(tabs) > uicatalog.MaxVisibleTabs() {
		return reject(http.StatusBadRequest, "탭은 "+strconv.Itoa(uicatalog.MaxVisibleTabs())+"개까지")
	}
	have, err := h.store.Screens(agentID)
	if err != nil {
		return err
	}
	for i, id := range tabs {
		if !hasScreen(have, id) {
			return reject(http.StatusBadRequest, "없는 화면 "+id)
		}
		if slices.Contains(tabs[:i], id) {
			return reject(http.StatusBadRequest, "같은 화면이 두 번 "+id)
		}
	}
	if err := h.store.SetTabs(agentID, tabs); err != nil {
		return err
	}
	if err := h.unhide(agentID, tabs...); err != nil {
		return err
	}
	h.toClients(ownerID, protocol.ClientMessage{Type: "ui", AgentID: agentID})
	return nil
}

// setHidden replaces the whole list of hidden screens. A screen in the tab bar can't be hidden.
func (h *hub) setHidden(ownerID, agentID string, ids []string) error {
	have, err := h.store.Screens(agentID)
	if err != nil {
		return err
	}
	tabs, _, err := h.store.Tabs(agentID)
	if err != nil {
		return err
	}
	for i, id := range ids {
		if !hasScreen(have, id) {
			return reject(http.StatusBadRequest, "없는 화면 "+id)
		}
		if slices.Contains(ids[:i], id) {
			return reject(http.StatusBadRequest, "같은 화면이 두 번 "+id)
		}
		if slices.Contains(tabs, id) {
			return reject(http.StatusBadRequest, "탭 막대에 있는 화면 "+id)
		}
	}
	if err := h.store.SetHidden(agentID, ids); err != nil {
		return err
	}
	h.toClients(ownerID, protocol.ClientMessage{Type: "ui", AgentID: agentID})
	return nil
}

// setOffOrder replaces the whole off-tab screen order. An empty list returns to server order.
func (h *hub) setOffOrder(ownerID, agentID string, ids []string) error {
	have, err := h.store.Screens(agentID)
	if err != nil {
		return err
	}
	for i, id := range ids {
		if !hasScreen(have, id) {
			return reject(http.StatusBadRequest, "없는 화면 "+id)
		}
		if slices.Contains(ids[:i], id) {
			return reject(http.StatusBadRequest, "같은 화면이 두 번 "+id)
		}
	}
	if err := h.store.SetOffOrder(agentID, ids); err != nil {
		return err
	}
	h.toClients(ownerID, protocol.ClientMessage{Type: "ui", AgentID: agentID})
	return nil
}

// setLocked replaces the whole list of locked screens. A locked screen can't be deleted by the user or the agent.
func (h *hub) setLocked(ownerID, agentID string, ids []string) error {
	have, err := h.store.Screens(agentID)
	if err != nil {
		return err
	}
	for i, id := range ids {
		if !hasScreen(have, id) {
			return reject(http.StatusBadRequest, "없는 화면 "+id)
		}
		if slices.Contains(ids[:i], id) {
			return reject(http.StatusBadRequest, "같은 화면이 두 번 "+id)
		}
	}
	if err := h.store.SetLocked(agentID, ids); err != nil {
		return err
	}
	h.toClients(ownerID, protocol.ClientMessage{Type: "ui", AgentID: agentID})
	return nil
}

// unhide removes ids from the hidden list. A screen placed in a tab or deleted does not stay hidden.
func (h *hub) unhide(agentID string, ids ...string) error {
	hidden := h.store.Hidden(agentID)
	kept := slices.DeleteFunc(slices.Clone(hidden), func(id string) bool { return slices.Contains(ids, id) })
	if len(kept) == len(hidden) {
		return nil
	}
	return h.store.SetHidden(agentID, kept)
}

func (h *hub) removeScreen(ownerID, agentID, screenID string) error {
	have, err := h.store.Screens(agentID)
	if err != nil {
		return err
	}
	if !hasScreen(have, screenID) {
		return reject(http.StatusNotFound, "없는 화면 "+screenID)
	}
	if slices.Contains(h.store.Locked(agentID), screenID) {
		return reject(http.StatusConflict, "잠긴 화면이라 지울 수 없어요 "+screenID)
	}
	if err := h.store.RemoveScreen(agentID, screenID); err != nil {
		return err
	}
	tabs, _, err := h.store.Tabs(agentID)
	if err != nil {
		return err
	}
	if err := h.store.SetTabs(agentID, slices.DeleteFunc(tabs, func(id string) bool { return id == screenID })); err != nil {
		return err
	}
	if err := h.unhide(agentID, screenID); err != nil {
		return err
	}
	if order := h.store.OffOrder(agentID); slices.Contains(order, screenID) {
		if err := h.store.SetOffOrder(agentID, slices.DeleteFunc(order, func(id string) bool { return id == screenID })); err != nil {
			return err
		}
	}
	h.toClients(ownerID, protocol.ClientMessage{Type: "ui", AgentID: agentID})
	return nil
}

// putData validates a bundle and replaces it whole. deck is blocked because it is where the app computes deck values.
// note marks a place that will be empty in the app: saved, but its shape differs from the spec's slot.
func (h *hub) putData(ownerID, agentID, name string, raw json.RawMessage) (note string, err error) {
	switch {
	case name == "deck":
		return "", reject(http.StatusBadRequest, "deck 은 덱 값 이름이라 묶음 이름으로 쓸 수 없다")
	case !uicatalog.ValidBundleName(name):
		return "", reject(http.StatusBadRequest, "묶음 이름은 소문자, 숫자, - 로 32자 이하")
	}
	var value bytes.Buffer
	if err := json.Compact(&value, raw); err != nil {
		return "", reject(http.StatusBadRequest, "invalid json")
	}
	if value.Len() > uicatalog.MaxDataBytes() {
		return "", reject(http.StatusBadRequest, "묶음은 "+strconv.Itoa(uicatalog.MaxDataBytes())+"바이트까지")
	}
	have, err := h.store.Data(agentID)
	if err != nil {
		return "", err
	}
	if _, ok := have[name]; !ok && len(have) >= uicatalog.MaxDataBundles() {
		return "", reject(http.StatusBadRequest, "묶음은 "+strconv.Itoa(uicatalog.MaxDataBundles())+"개까지")
	}
	var notes []string
	var warnings []protocol.DataWarning
	if screens, err := h.store.Screens(agentID); err == nil { // if the spec can't be read, save without a warning
		if screens, err = h.expanded(agentID, screens); err != nil {
			return "", err
		}
		specs := make([]json.RawMessage, len(screens))
		for i, s := range screens {
			specs[i] = s.Spec
		}
		for _, w := range uicatalog.DataWarnings(specs, name, value.Bytes()) {
			notes = append(notes, w.String())
			if d := (protocol.DataWarning{Path: w.Path, Reason: w.Reason}); !slices.Contains(warnings, d) {
				warnings = append(warnings, d)
			}
		}
	}
	if err := h.store.PutData(agentID, name, value.Bytes(), warnings); err != nil {
		return "", err
	}
	h.toClients(ownerID, protocol.ClientMessage{Type: "ui", AgentID: agentID})
	return strings.Join(notes, " / "), nil
}

func (h *hub) removeData(ownerID, agentID, name string) error {
	have, err := h.store.Data(agentID)
	if err != nil {
		return err
	}
	if _, ok := have[name]; !ok {
		return reject(http.StatusNotFound, "없는 묶음 "+name)
	}
	if err := h.store.RemoveData(agentID, name); err != nil {
		return err
	}
	h.toClients(ownerID, protocol.ClientMessage{Type: "ui", AgentID: agentID})
	return nil
}

// putPart validates an assembly part and stacks a new revision. Accepted only if the screens that use it still expand with the new revision.
func (h *hub) putPart(ownerID, agentID string, raw []byte) (string, error) {
	p, err := uicatalog.ValidatePart(raw)
	if err != nil {
		return p.ID, reject(http.StatusBadRequest, err.Error())
	}
	var spec bytes.Buffer
	if err := json.Compact(&spec, raw); err != nil {
		return p.ID, reject(http.StatusBadRequest, "invalid json")
	}
	parts, err := h.parts(agentID)
	if err != nil {
		return p.ID, err
	}
	if _, ok := parts[p.ID]; !ok && len(parts) >= uicatalog.MaxParts() {
		return p.ID, reject(http.StatusBadRequest, "조립 부품은 "+strconv.Itoa(uicatalog.MaxParts())+"개까지")
	}
	parts[p.ID] = p
	screens, err := h.store.Screens(agentID)
	if err != nil {
		return p.ID, err
	}
	for _, sid := range partUsers(screens)[p.ID] {
		i := slices.IndexFunc(screens, func(r store.ScreenRow) bool { return r.ID == sid })
		if _, _, err := uicatalog.Expand(screens[i].Spec, parts); err != nil {
			return p.ID, reject(http.StatusBadRequest, "이 판으로는 화면 "+sid+" 가 안 펼쳐진다: "+err.Error())
		}
	}
	if _, err := h.store.PutPart(agentID, p.ID, spec.Bytes()); err != nil {
		return p.ID, err
	}
	h.toClients(ownerID, protocol.ClientMessage{Type: "ui", AgentID: agentID})
	return p.ID, nil
}

// removePart deletes only when no screen uses it.
func (h *hub) removePart(ownerID, agentID, partID string) error {
	parts, err := h.store.Parts(agentID)
	if err != nil {
		return err
	}
	if !hasScreen(parts, partID) {
		return reject(http.StatusNotFound, "없는 조립 부품 "+partID)
	}
	screens, err := h.store.Screens(agentID)
	if err != nil {
		return err
	}
	if users := partUsers(screens)[partID]; len(users) > 0 {
		return reject(http.StatusConflict, "쓰는 화면이 있어 지울 수 없다: "+strings.Join(users, ", ")+". 화면을 먼저 지우거나 use 를 뺀다")
	}
	if err := h.store.RemovePart(agentID, partID); err != nil {
		return err
	}
	h.toClients(ownerID, protocol.ClientMessage{Type: "ui", AgentID: agentID})
	return nil
}

// agentUI handles ui.* and data.*. ui.list is answered with ui.state, the rest with ui.ack, and a rejection carries its reason.
func (h *hub) agentUI(a store.AgentAuth, msg protocol.AgentMessage) error {
	var state uiState
	var err error
	if !strings.HasPrefix(msg.Type, "data.") { // bundles don't depend on templates
		if state, err = h.ui(a.ID); err != nil {
			return err
		}
	}
	if msg.Type == "ui.list" {
		raw, err := json.Marshal(state)
		if err != nil {
			return err
		}
		h.toAgent(a.ID, protocol.ServerMessage{Type: "ui.state", UI: raw})
		return nil
	}
	var id, note string
	var body json.RawMessage
	switch msg.Type {
	case "ui.versions":
		id = msg.ID
		body, err = h.versionSummaries(a.ID, msg.ID)
	case "ui.revert":
		id = msg.ID
		note, err = h.revertScreen(a.OwnerID, a.ID, msg.ID, msg.To)
	case "ui.put":
		id, note, err = h.putScreen(a.OwnerID, a.ID, msg.Screen, msg.Place)
	case "ui.remove":
		id, err = msg.ID, h.removeScreen(a.OwnerID, a.ID, msg.ID)
	case "ui.layout":
		err = h.setTabs(a.OwnerID, a.ID, msg.Tabs)
	case "ui.part.put":
		id, err = h.putPart(a.OwnerID, a.ID, msg.Part)
	case "ui.part.remove":
		id, err = msg.ID, h.removePart(a.OwnerID, a.ID, msg.ID)
	case "data.put":
		id = msg.Name
		note, err = h.putData(a.OwnerID, a.ID, msg.Name, msg.Data)
	case "data.remove":
		id, err = msg.Name, h.removeData(a.OwnerID, a.ID, msg.Name)
	}
	ok := err == nil
	ack := protocol.ServerMessage{Type: "ui.ack", ID: id, OK: &ok, Reason: note, Req: msg.Req, UI: body}
	if err != nil {
		ack.Reason = err.Error()
	}
	h.toAgent(a.ID, ack)
	return nil
}

// ownedUI takes the lock, checks ownership, and runs f with the template already filled in.
func (h *hub) ownedUI(ownerID, agentID string, f func() error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.store.AgentOwnedBy(agentID, ownerID) {
		return reject(http.StatusNotFound, "agent not found")
	}
	if _, err := h.ui(agentID); err != nil {
		return err
	}
	return f()
}

func (h *hub) UI(ownerID, agentID string) (uiState, error) {
	h.freshenSensors()
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.store.AgentOwnedBy(agentID, ownerID) {
		return uiState{}, reject(http.StatusNotFound, "agent not found")
	}
	state, err := h.ui(agentID)
	if err != nil {
		return state, err
	}
	state.Screens, err = h.expanded(agentID, state.Screens)
	return state, err
}

// PartRow is an assembly part as the owner sees it. UsedBy is the ids of screens using it.
type PartRow struct {
	store.ScreenRow
	UsedBy []string `json:"usedBy"`
}

func (h *hub) Parts(ownerID, agentID string) (rows []PartRow, err error) {
	err = h.ownedUI(ownerID, agentID, func() error {
		parts, err := h.store.Parts(agentID)
		if err != nil {
			return err
		}
		screens, err := h.store.Screens(agentID)
		if err != nil {
			return err
		}
		users := partUsers(screens)
		rows = make([]PartRow, len(parts))
		for i, p := range parts {
			rows[i] = PartRow{p, append([]string{}, users[p.ID]...)}
		}
		return nil
	})
	return rows, err
}

func (h *hub) RemovePart(ownerID, agentID, partID string) error {
	return h.ownedUI(ownerID, agentID, func() error { return h.removePart(ownerID, agentID, partID) })
}

func (h *hub) PartVersions(ownerID, agentID, partID string) (rows []store.ScreenRow, err error) {
	err = h.ownedUI(ownerID, agentID, func() error {
		rows, err = h.store.PartVersions(agentID, partID)
		return err
	})
	return rows, err
}

// RevertPart re-uploads that revision's content as a new revision.
func (h *hub) RevertPart(ownerID, agentID, partID string, version int) error {
	return h.ownedUI(ownerID, agentID, func() error {
		rows, err := h.store.PartVersions(agentID, partID)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Version == version {
				_, err := h.putPart(ownerID, agentID, r.Spec)
				return err
			}
		}
		return reject(http.StatusNotFound, "없는 판")
	})
}

// PartPreview is the blocks expanded with example values.
func (h *hub) PartPreview(ownerID, agentID, partID string) (blocks []json.RawMessage, err error) {
	err = h.ownedUI(ownerID, agentID, func() error {
		parts, err := h.parts(agentID)
		if err != nil {
			return err
		}
		p, ok := parts[partID]
		if !ok {
			return reject(http.StatusNotFound, "없는 조립 부품 "+partID)
		}
		blocks = p.Preview()
		return nil
	})
	return blocks, err
}

func (h *hub) PutScreen(ownerID, agentID string, raw []byte, place string) (note string, err error) {
	err = h.ownedUI(ownerID, agentID, func() error {
		_, note, err = h.putScreen(ownerID, agentID, raw, place)
		return err
	})
	return note, err
}

func (h *hub) RemoveScreen(ownerID, agentID, screenID string) error {
	return h.ownedUI(ownerID, agentID, func() error { return h.removeScreen(ownerID, agentID, screenID) })
}

func (h *hub) SetTabs(ownerID, agentID string, tabs []string) error {
	return h.ownedUI(ownerID, agentID, func() error { return h.setTabs(ownerID, agentID, tabs) })
}

func (h *hub) SetHidden(ownerID, agentID string, ids []string) error {
	return h.ownedUI(ownerID, agentID, func() error { return h.setHidden(ownerID, agentID, ids) })
}

func (h *hub) SetOffOrder(ownerID, agentID string, ids []string) error {
	return h.ownedUI(ownerID, agentID, func() error { return h.setOffOrder(ownerID, agentID, ids) })
}

func (h *hub) SetLocked(ownerID, agentID string, ids []string) error {
	return h.ownedUI(ownerID, agentID, func() error { return h.setLocked(ownerID, agentID, ids) })
}

func (h *hub) RemoveData(ownerID, agentID, name string) error {
	return h.ownedUI(ownerID, agentID, func() error { return h.removeData(ownerID, agentID, name) })
}

func (h *hub) ScreenVersions(ownerID, agentID, screenID string) (rows []store.ScreenRow, err error) {
	err = h.ownedUI(ownerID, agentID, func() error {
		rows, err = h.store.ScreenVersions(agentID, screenID)
		return err
	})
	return rows, err
}

// RevertScreen re-uploads that revision's content as a new revision.
func (h *hub) RevertScreen(ownerID, agentID, screenID string, version int) error {
	return h.ownedUI(ownerID, agentID, func() error {
		_, err := h.revertScreen(ownerID, agentID, screenID, version)
		return err
	})
}

// revertScreen runs under the hub lock. Lookups key on agentID, so another agent's screen has no versions here.
func (h *hub) revertScreen(ownerID, agentID, screenID string, version int) (string, error) {
	rows, err := h.store.ScreenVersions(agentID, screenID)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", reject(http.StatusNotFound, "no such screen "+screenID)
	}
	for _, r := range rows {
		if r.Version == version {
			_, note, err := h.putScreen(ownerID, agentID, r.Spec, "")
			if err != nil {
				return "", err
			}
			done := fmt.Sprintf("restored version %d as version %d", version, rows[0].Version+1)
			if note != "" {
				done += "; " + note
			}
			return done, nil
		}
	}
	return "", reject(http.StatusNotFound, fmt.Sprintf("no version %d of %s; only the latest %d versions can be restored", version, screenID, versionsKept))
}

const versionsKept = 20 // store.ScreenVersions LIMIT

type versionSummary struct {
	Version   int    `json:"version"`
	CreatedAt int64  `json:"createdAt"`
	Current   bool   `json:"current,omitempty"`
	Summary   string `json:"summary"`
}

// versionSummaries answers ui.versions with one-line summaries instead of full specs.
func (h *hub) versionSummaries(agentID, screenID string) (json.RawMessage, error) {
	rows, err := h.store.ScreenVersions(agentID, screenID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, reject(http.StatusNotFound, "no such screen "+screenID)
	}
	out := make([]versionSummary, len(rows))
	for i, r := range rows {
		out[i] = versionSummary{r.Version, r.UpdatedAt, i == 0, specSummary(r.Spec)}
	}
	return json.Marshal(map[string]any{"versions": out})
}

// specSummary reads like `"Habits" · 3 blocks: stat, heatmap, list`.
func specSummary(raw json.RawMessage) string {
	var spec struct {
		Title  string `json:"title"`
		Blocks []struct {
			Type string `json:"type"`
			Part string `json:"part"`
		} `json:"blocks"`
	}
	json.Unmarshal(raw, &spec)
	kinds := make([]string, 0, 6)
	for i, b := range spec.Blocks {
		if i == 6 {
			kinds = append(kinds, fmt.Sprintf("+%d", len(spec.Blocks)-6))
			break
		}
		k := b.Type
		if b.Type == "use" && b.Part != "" {
			k = "use:" + b.Part
		}
		kinds = append(kinds, k)
	}
	return fmt.Sprintf("%q · %d blocks: %s", spec.Title, len(spec.Blocks), strings.Join(kinds, ", "))
}

func (s *Server) uiRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /ui/catalog", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(uicatalog.Raw)
	})

	m.HandleFunc("GET /api/agents/{id}/ui", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		state, err := s.hub.UI(owner, r.PathValue("id"))
		if err == nil {
			writeJSON(w, http.StatusOK, state)
		}
		return err
	}))

	m.HandleFunc("POST /api/agents/{id}/ui/screens", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		raw, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 70<<10))
		if err != nil {
			return reject(http.StatusBadRequest, "body too large")
		}
		note, err := s.hub.PutScreen(owner, r.PathValue("id"), raw, r.URL.Query().Get("place"))
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": note})
		return nil
	}))

	m.HandleFunc("DELETE /api/agents/{id}/ui/screens/{sid}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if err := s.hub.RemoveScreen(owner, r.PathValue("id"), r.PathValue("sid")); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	m.HandleFunc("DELETE /api/agents/{id}/ui/data/{name}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if err := s.hub.RemoveData(owner, r.PathValue("id"), r.PathValue("name")); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	m.HandleFunc("PUT /api/agents/{id}/ui/hidden", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		var b struct {
			Hidden []string `json:"hidden"`
		}
		if json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10)).Decode(&b) != nil || b.Hidden == nil {
			return reject(http.StatusBadRequest, "hidden is required")
		}
		if err := s.hub.SetHidden(owner, r.PathValue("id"), b.Hidden); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	m.HandleFunc("PUT /api/agents/{id}/ui/order", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		var b struct {
			Order []string `json:"order"`
		}
		if json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10)).Decode(&b) != nil || b.Order == nil {
			return reject(http.StatusBadRequest, "order is required")
		}
		if err := s.hub.SetOffOrder(owner, r.PathValue("id"), b.Order); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	m.HandleFunc("PUT /api/agents/{id}/ui/locked", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		var b struct {
			Locked []string `json:"locked"`
		}
		if json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10)).Decode(&b) != nil || b.Locked == nil {
			return reject(http.StatusBadRequest, "locked is required")
		}
		if err := s.hub.SetLocked(owner, r.PathValue("id"), b.Locked); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	m.HandleFunc("PUT /api/agents/{id}/ui/tabs", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		var b struct {
			Tabs []string `json:"tabs"`
		}
		if json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10)).Decode(&b) != nil || b.Tabs == nil {
			return reject(http.StatusBadRequest, "tabs is required")
		}
		if err := s.hub.SetTabs(owner, r.PathValue("id"), b.Tabs); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	m.HandleFunc("GET /api/agents/{id}/ui/parts", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		rows, err := s.hub.Parts(owner, r.PathValue("id"))
		if err == nil {
			writeJSON(w, http.StatusOK, rows)
		}
		return err
	}))

	m.HandleFunc("DELETE /api/agents/{id}/ui/parts/{pid}", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		if err := s.hub.RemovePart(owner, r.PathValue("id"), r.PathValue("pid")); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	m.HandleFunc("GET /api/agents/{id}/ui/parts/{pid}/versions", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		rows, err := s.hub.PartVersions(owner, r.PathValue("id"), r.PathValue("pid"))
		if err == nil {
			writeJSON(w, http.StatusOK, versionList(rows))
		}
		return err
	}))

	m.HandleFunc("POST /api/agents/{id}/ui/parts/{pid}/revert", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		var b struct {
			Version int `json:"version"`
		}
		if json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<10)).Decode(&b) != nil || b.Version < 1 {
			return reject(http.StatusBadRequest, "version is required")
		}
		if err := s.hub.RevertPart(owner, r.PathValue("id"), r.PathValue("pid"), b.Version); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return nil
	}))

	m.HandleFunc("GET /api/agents/{id}/ui/parts/{pid}/preview", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		blocks, err := s.hub.PartPreview(owner, r.PathValue("id"), r.PathValue("pid"))
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"blocks": blocks})
		}
		return err
	}))

	m.HandleFunc("GET /api/agents/{id}/ui/screens/{sid}/versions", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		rows, err := s.hub.ScreenVersions(owner, r.PathValue("id"), r.PathValue("sid"))
		if err == nil {
			writeJSON(w, http.StatusOK, versionList(rows))
		}
		return err
	}))

	m.HandleFunc("POST /api/agents/{id}/ui/screens/{sid}/revert", s.api(func(w http.ResponseWriter, r *http.Request, owner string) error {
		var b struct {
			Version int `json:"version"`
		}
		if json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<10)).Decode(&b) != nil || b.Version < 1 {
			return reject(http.StatusBadRequest, "version is required")
		}
		if err := s.hub.RevertScreen(owner, r.PathValue("id"), r.PathValue("sid"), b.Version); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return nil
	}))
}

type version struct {
	Version   int             `json:"version"`
	CreatedAt int64           `json:"createdAt"`
	Spec      json.RawMessage `json:"spec"`
}

func versionList(rows []store.ScreenRow) []version {
	out := make([]version, len(rows))
	for i, row := range rows {
		out[i] = version{row.Version, row.UpdatedAt, row.Spec}
	}
	return out
}
