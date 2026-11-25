# kyeot server

Server for a messenger built for agents. Agents connect over WebSocket; the phone app handles tasks, events, and approvals over REST + WebSocket. One Go binary (`cmd/kyeot`) includes the Hermes adapter.

## Where to change

| Task | File |
|---|---|
| Add a message type or required field | `required`, `enums` in `internal/protocol/protocol.go`. List a new type in the `docs/connect-your-agent.md` table too (`guide_test.go` checks) |
| Agent message handling, task state transitions | `handleAgentMessage` in `internal/server/hub.go` |
| User actions (start, message, approve, cancel) | Exported methods in `internal/server/hub.go` |
| Conversation title (`task.title`, `PATCH /api/tasks/{id}`) | `retitle` in `internal/server/hub.go`. The only guard that stops agents from overwriting a user-set title (`title_source = 'user'`) is `SetTaskTitle` in `internal/store` |
| Agent picture names (`preset:<name>`) | The old 10 in `store.Presets` (default assignment depends on its length; do not grow it), animated characters in `store.MotionPresets`. Names must match the app's character list |
| Person avatar (`owners.avatar`: empty, `color:<name>`, photo) | Value check `store.PersonAvatarOK`, color names `store.PersonColors` (keep equal to the app's `personColors`). The room owner's avatar is not sent in the WebSocket `room`; only via `GET /api/rooms/{id}/people` |
| HTTP routes | `routes` in `internal/server/server.go` |
| Access log (`KYEOT_ACCESS_LOG`, off by default) | `internal/server/accesslog.go`. Who a request belongs to is set by `noteOwner` in `ownerOf` and `serveAgent`. Paths with a secret segment are masked in `logPath` |
| Tables | `schema` in `internal/store/store.go` |
| Phone notifications (APNs), `/api/devices` | `internal/push/push.go` sends; `notify` in `internal/server/push.go` decides when. Setup in `docs/push.md` |
| Test agent behavior | `internal/fakeagent/fakeagent.go` |
| Sensor templates (network, disk, system) | `internal/sensor/*`. The server runs them itself via `Options.SensorDir` (`internal/server/sensors.go`). Turn off with `KYEOT_SENSORS=off`. The source of truth for aggregation rules is `integrations/hermes/examples/netwatch/netwatch_deck.py`; `TestSameAsPythonOriginal` checks they match (excluding the `$text` key table). Display text: Korean fields stay for old apps, and per-path keys ride in `$text` at the top of the bundle (`internal/sensor/text`, Korean baseline `text/testdata/ko.json`) |
| Hermes event translation | `stream` in `internal/hermesbridge/bridge.go` |
| Hermes HTTP calls | `internal/hermesbridge/hermes.go` |
| Multiple Hermes instances (profiles) on one computer | `Profiles` in `internal/hermesbridge/profiles.go` (reads folders only). Server-side connection in `internal/server/localhermes.go` |
| Bridge local address on the Mac (`127.0.0.1:7701`) | `internal/hermesbridge/uiapi.go`. Requests are serialized by `uiMu`. Address via `BRIDGE_UI_ADDR` (empty disables). Same-computer profiles share it through `uirouter.go`: default `/ui/…`, others `/h/<key>/ui/…`; the skill is installed per profile with its key (`deckui.InstallKey`), `kyeot uninstall` removes only our copies (`deckui.Remove`) |
| Screen skill for Hermes | `integrations/hermes/deck-ui/SKILL.md`. Embedded in the binary (`deckui.go`); when the bridge opens the screen address and connects, it installs into `<HERMES_HOME>/skills/productivity/deck-ui/`, and replaces an older version after keeping `.bak-<time>` (`installSkill` in `cmd/kyeot/hermes.go`). Manual install: `sh scripts/install-hermes-skill.sh` |
| Test helpers (start a server, wait on the screen side) | `internal/decktest/decktest.go` |
| Component catalog (components, props, limits) | `internal/uicatalog/catalog.json` is the source. The app holds a copy |
| Layout validation, starter screens for new agents | `Validate`, `Starters` in `internal/uicatalog/uicatalog.go` |
| Screen storage, tab layout, `ui.*` handling, `/ui/catalog` and `/api/agents/{id}/ui*` | `internal/server/ui.go`. Ownership check lives only in `ownedUI` |
| Data bundle checks (name, blocking `deck`, size, count) | `putData` in `internal/server/ui.go`. Limits are catalog `limits.maxDataBytes`, `maxDataBundles` |
| Bundle shape warnings | `DataWarnings` in `internal/uicatalog/data.go` |
| Template bundle shape tables (`docs/agent-ui.md`, `SKILL.md`) and `internal/uicatalog/testdata/template_contracts.json` | Generated. Do not edit by hand |
| App JS update routes (`/updates/manifest`, `/updates/files/*`) | `internal/server/updates.go` |
| Routine run history (runs with nothing to post) | `quiet` on `routine.run` → `routineRun` in `internal/server/hub_routines.go`. Hermes side: `internal/hermesbridge/routines.go` |
| Quotes, edits, reactions, attachments on people's room messages | `internal/server/room_talk.go`, storage in `internal/store/room_messages.go` |
| Room safety (delete messages, reports and handling, blocking, word filter, terms) | `internal/server/room_safety.go`, storage in `internal/store/safety.go` (tables `room_reports`, `blocks`, `owners.terms_*`, `rooms.filter_off`, `room_messages.removed_at`). Word list in `internal/wordfilter` (small hand-written list). Terms version is `TermsVersion`; bump it when the text changes. Viewer-specific marking (blocked, reported, filtered) lives only in `markForViewer` |
| Pinning and groups in conversation and room lists | `internal/store/lists.go`, routes in `internal/server/lists.go`. Feature names `lists.pin`, `lists.groups`, `lists.groupOrder` (order within a group: `list_prefs.group_pos`, `PUT .../groups/{gid}/items`) |
| Rooms and routines | Table `room_routines`, routes in `internal/server/rooms.go`. Keep `rooms.routine_id` equal to the first routine, for old app compatibility |
| Owner transfer, deleting the whole server (`/api/owner/*`, `/api/server/close`) | `internal/server/owner.go`, `internal/store/owner.go`. The owner is `store.OwnerID()`, not a constant (initial value `store.Owner`). A closed server returns 410 from `Handler` for everything except `/version` |
| Service registration (`kyeot service install`) | `cmd/kyeot/service.go`. Env vars passed to the service: `serverSpec.env` |
| DB backups (`.bak-*` before schema changes, daily `backups/`, removal on server close) | `internal/store/backup.go`, daily schedule in `internal/server/backup.go`. Changing a data-fix function run on upgrade (`migrate` etc.) breaks the fingerprint in `schemaversion_internal_test.go` — bump `SchemaVersion` |
| `kyeot backup`, `kyeot restore` (folders and keys in the bundle) | `bundleDirs`, `bundleKeys` in `cmd/kyeot/backup.go`. Add new user data kept in the data folder here |
| Release build and install script | `scripts/release-build.sh`, `scripts/install.sh` (`docs/self-host.md` "Binary releases") |
| Check for values that must not go public | `scripts/check-public.sh` |

## Don't

| Don't | Why |
|---|---|
| Change or remove a field's meaning within the same `protocol` | Users run different server versions. Only add; register new features by name in `Features` in `internal/server/compat.go`. Never remove a name once added |
| Make changes that put new requirements on agents | The contract is only `message`, `task.end` (or `status idle`), and `approval`. The server handles normalization and display text. Server and docs do not assume Hermes |
| Tables or queries without `owner_id` | Present from the start so multi-user support needs no schema change |
| Store raw agent tokens | DB keeps only sha256. The raw token goes out once, in the registration response |
| Commit personal values (home paths, account names, private addresses, keys) | Public repo. `scripts/check-public.sh` blocks it. Mark test examples with `public-ok` at line end |
| Open or test against the DB in a real data folder | Tests use a fresh DB in `t.TempDir()` |
| Store then deliver outside the hub lock | Event order would differ per screen |
| Make `peer.close` wait for a handshake | It is called while holding the hub lock. If it blocks, everything stops |
| Close the DB in `Server.Close` before cleaning up connections | Late disconnect handling writes to a closed DB |
| Change a contract file (`catalog.json`, message fields) and leave the app as is | The app holds a copy. The app repo picks up the commit and syncs its copy |

## Verify

```sh
go vet ./... && go test ./... && sh scripts/check-public.sh   # ok, public check passes
go test -race ./internal/store/... ./internal/server/...
```

When you change a test, confirm it fails on a copy with one guard broken.
Not covered by tests: the steer success path for an in-progress run (the fake Hermes always returns 409).

## Pitfalls

| Symptom | Cause |
|---|---|
| Browser `/client` gets 403, API gets CORS errors | The app origin is missing from `KYEOT_ALLOWED_ORIGINS` |
| Hermes events vanish while the bridge is disconnected | `send` drops messages when there is no connection. Nothing is resent after reconnect |
| Creating a test profile in `~/.hermes` changed your Hermes config | With two profiles, Hermes 0.22 switches the default gateway to multiplex and writes to `config.yaml`. Test in an isolated `HOME=<temp>` root |
| Testing with a real Hermes changes its long-term memory | Hermes writes `~/.hermes/memories/USER.md` with its `memory` tool. Keep memorable facts out of test prompts |
| `deckui` test `TestVersionMovesWithContent` fails | `SKILL.md` content changed (including via `gen-template-docs.mjs`). Bump `version` and add the new version to `bodyHashes` in `deckui_test.go`. Same version means user copies are not updated |
| Occasional `timeout waiting for B idle` only in CI | `decktest.WaitFor` drops non-matching messages. The server sends idle for several agents in no fixed order, so wait together with `WaitAll` |
| Table-not-found error with a `:memory:` DB | Each connection gets a different in-memory DB. `SetMaxOpenConns(1)` was removed |
| A message missing an object or array field passes | `required` in `ParseAgentMessage` checks only string fields. Add per-type checks |
