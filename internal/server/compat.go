package server

// App/server version matching. Old apps are always accepted; new features are announced by name (Features).
//
// Raise Protocol only for breaking changes (ones old apps can no longer connect through). Within one Protocol,
// only add fields and never change meaning. A new feature adds one name to Features,
// and the app draws that feature's button only when the name is present.
const (
	Protocol          = 1
	MinClientProtocol = 1 // older apps get "please update the app"
)

// Features is what this server can do. Names are never removed (old apps look for them).
var Features = []string{
	"rooms.share",     // room invite code and participants
	"users",           // account invites and user management
	"search.block",    // search screen part
	"service",         // kyeot service install
	"refresh.block",   // refresh screen part
	"routines.run",    // POST /api/agents/{id}/routines/{routine}/run (the agent must also announce it in hello)
	"routines.manage", // create, toggle, delete routines; 409 if the agent declared features in hello without routines.manage
	"sensor.network",  // network sensor template, refresh with data only, POST /api/agents/{id}/data/{name}/refresh (the agent must also announce sensor.network and data.refresh in hello)
	"sensor.disk",     // disk sensor template (disk bundle). Refreshes the same way as sensor.network
	"sensor.system",   // system sensor (system bundle: CPU, memory, swap, battery). Refreshes the same way as sensor.network
	// "sensor.server" is added to /version only while sensors run (sensors.go). The server measures directly and fills and refreshes without an agent
	// "sensor.server.off" is added only when sensors are off (KYEOT_SENSORS=off). The app shows "off on this kyeot server"
	"data.warnings",        // bundle warnings: paths and reasons that disagree with the design's $data slots (data.<name>.warnings in the ui response)
	"rooms.question",       // task.question in the room post list (original first user message). Routine description tied to the room question background
	"hermes.profiles",      // profiles list of /api/local-hermes, POST /api/local-hermes/connect {profile}
	"avatars.motion",       // accepts animated characters as the picture (preset:<iconDrafts G kind>)
	"routines.history",     // GET /api/agents/{id}/routines/{routine}/history. History of routine.run quiet runs (runs with nothing to send)
	"rooms.reply",          // quoteId of a room human post with ask:false (same-room posts only), RoomMessage.quote
	"rooms.edit",           // PATCH /api/rooms/{id}/messages/{mid}. Only my last human post, RoomMessage.edited
	"rooms.react",          // POST /api/rooms/{id}/messages/{mid}/reactions, RoomMessage.reactions
	"rooms.files",          // attachments of a room human post with ask:false, RoomMessage.attachments
	"costs.routines",       // report period.byRoutine (amount per routine, gone), routine.monthCost, monthRuns
	"lists.pin",            // pin conversations and rooms: PATCH /api/lists/{tasks|rooms}/items/{id} {pinned}, PUT .../pins {ids}, pinned in the list
	"lists.groups",         // conversation and room groups: /api/lists/{tasks|rooms}/groups, items {groupId}, groupId in the list
	"lists.groupOrder",     // PUT /api/lists/{tasks|rooms}/groups/{gid}/items {ids}: put items in that group in that order, groupPos in items
	"owner.handover",       // GET /api/owner/leave, POST /api/owner/handover {heirId, confirm}, handoverFrom and host in /api/me, POST /api/me/handover-seen
	"ui.parts",             // composed parts: ui.part.put and remove, use in a screen (the server expands it, the app gets only base parts), /api/agents/{id}/ui/parts
	"ui.order",             // PUT /api/agents/{id}/ui/order {order}, order in the ui response (screen order outside tabs, deleted screens drop out)
	"server.id",            // serverId in /version, server in notification bodies. Made once on first start and never changed
	"server.close",         // POST /api/server/close {confirm}. After closing, 410 {closed:true} instead of /version, websocket server.closed and close code 4010
	"links.preview",        // GET /api/link-preview?url= {ok, reason | kind, title, description, site, author, image, favicon, at}, image via the signed /api/link-preview/image/{sha256} address
	"links.preview.cached", // GET /api/link-preview?url=&cached=1 — stored results only (expired included), {ok:false, reason:"not_cached"} if none. No outside access
	"rooms.delete",         // DELETE /api/rooms/{id}/messages/{mid}: the author deletes the row, the room owner leaves a "deleted post" slot (RoomMessage.removed owner). On websocket removed self the app drops the post
	"rooms.report",         // POST /api/rooms/{id}/reports {messageId|targetId, reason, note}, GET /api/reports, POST /api/reports/{id} {actions}, websocket room.report, notification room. RoomMessage.reported
	"rooms.block",          // GET /api/blocks, PUT and DELETE /api/blocks/{ownerId}. Per server, RoomMessage.blocked (the original is kept)
	"rooms.filter",         // hide banned words in rooms (on by default): PATCH /api/rooms/{id} {filter}, Room.filterOff, RoomMessage.filtered (not turned off for the author)
	"terms",                // POST /api/me/terms {version}, termsVersion and termsAgreed in /api/me. Without agreeing, no posting in shared rooms (403 terms_required)
	"people.avatar",        // person picture: PATCH /api/me {avatar: ""|color:<name>|photo}, avatar of Me, Member, Invitee, GET /api/rooms/{id}/people {owner, members}
}
