# Connect your agent to kyeot

**English** · [한국어](connect-your-agent.ko.md)

This document is for developers who want to attach their own bot or another agent framework to kyeot. kyeot has three parts: the app that runs on phones and the web, the deck server that stores and relays conversations and screens, and the agent that does the actual work. Every user runs the same app and deck server code. Only the agent differs per user. The agent connects to the deck server from outside over a WebSocket — a channel where, once connected, both sides can send messages at any time. Because the agent opens the connection, you do not need to open a port on your home router even if the agent runs on a Mac at home. The deck server only needs to be somewhere the agent can reach.

Every message is a single JSON object, and its `type` field tells the kind. `internal/protocol/protocol.go` is the source of truth for field names and meanings. A test (`internal/protocol/guide_test.go`) checks that the message tables in this document match that source.

## Minimum contract

An agent must do only three things. Reply with `message` to incoming conversations (`task.start`, `user_message`), send `task.end` when done, and send `approval` when it needs a human to confirm. If the agent never sends the other messages (`status`, `hello`, `report`, `routine.run`, `room.*`, `ui.*`, `data.*`), the deck server does not reject anything and the app hides the screens for those features. The agent can ignore deck messages it does not understand (`routine.*`, `ui.ack`, `error`) without breaking server state.

If the agent forgets `task.end`, two fallbacks cover it. If the agent sends `status` as `idle` after replying, the deck server closes that conversation as `done`. If the agent stays disconnected for 1 minute (the `KYEOT` server setting `AgentGrace`), the server closes its in-progress conversations as `failed`. The same applies when the deck server restarts. If the agent does not reconnect within 1 minute and either send something to that conversation or report `status` as `working`/`blocked`, the conversation closes as `failed`. If the agent reports that it is working, the server waits until it becomes `idle`. Still, sending `task.end` at the end of every reply is more accurate.

kyeot does not assume any particular agent product. The Hermes bridge (`internal/hermesbridge`) is just one implementation of this protocol. Anything that needs interpretation, such as report state values, schedule text, or routine name formatting, is handled by the deck server (see "Reports" below).

## Agent registration and tokens

In the app's sidebar, tap "Add agent" and choose a name. The deck server creates an agent token and shows it. The token is a secret that proves who this agent is. The deck server does not store the raw token. It keeps only a hash — a fingerprint scrambled so the original cannot be recovered — so the token is shown **only once**, at registration. If you lose it, register the agent again. Do not paste it into a code repository or chat room. Pass it through an environment variable or a secret file.

You can also register over HTTP instead of the app. The owner token is the `KYEOT_OWNER_TOKEN` value set when the deck server starts. The `token` in the response is the agent token.

```sh
curl -s -H "Authorization: Bearer $KYEOT_OWNER_TOKEN" -d '{"name":"My bot"}' http://127.0.0.1:7700/api/agents
```

## Connecting

The address is `ws://<deck address>/agent`. If the deck server sits behind TLS (the scheme that encrypts traffic over the internet), use `wss://`. When opening the WebSocket, put `Authorization: Bearer <agent token>` in the HTTP headers. If the token is missing or wrong, the deck server rejects with 401. If the deck server limits allowed networks with `KYEOT_NETS`, connections from outside those networks get 403.

After a successful connection, the deck server marks the agent as `idle`. The agent should send its own state once with `status` right after connecting. If it was in the middle of work, it should report `working` so the app draws it correctly.

```json
{"type": "status", "state": "idle"}
```

If a second connection arrives with the same token, the deck server closes the earlier one (close code 4000, reason `replaced by a new connection`). So reconnect code must fully drop the old connection before opening a new one, or the two will keep pushing each other out. The deck server accepts messages up to 1MB each.

If a message is malformed, the deck server stores nothing and returns `error`. A `reason` of `invalid message` means the `type` is unknown, a required field is empty, or a value is out of range. Using another agent's conversation or a nonexistent conversation ID returns `unknown task <id>`.

## Messages the agent sends

Fields with no type noted are strings. Fields in parentheses are optional. `task` is the conversation ID. In kyeot, one conversation is one task.

| type | Fields | When |
|---|---|---|
| `status` | `state` (`idle`, `working`, `blocked`) | When the agent's state changes. `blocked` means waiting for a human's answer |
| `hello` | All optional. `kind`, `version`, `models` (`[{role, name}]`, up to 10), `features` string array | Once, right after connecting. The app's agent info shows the kind and the model per role. Send `role` as an English key such as `chat`, `delegate`, `title`. `features` lists what you use among `routines`, `costs`, `ui`, `routines.run` (run a routine now, optional), `routines.manage` (handles `routine.create`·`routine.toggle`·`routine.remove`; if declared and later missing, the app blocks turning routines on and off). If you do not send it, the deck server infers features from the messages it receives. If you send it, missing features are treated as absent, so list everything you use |
| `task.open` | `task`, `title` | When the agent opens a new conversation on its own. The agent picks `task`. An existing ID returns `error` |
| `task.title` | `task`, `title` | When announcing a title derived from the conversation. Leading and trailing spaces are trimmed, 1–80 characters, line breaks become spaces. A title the user changed in the app is not overwritten; the update is silently ignored |
| `message` | `task`, `text` | When sending a reply. Markdown headings, lists, code, quotes, bold, and links are rendered |
| `draft` | `task`, `text` | When previewing a reply in progress. It is only streamed to the screen, not stored. Send the finished version with `message` when done |
| `tool` | `task`, `call`, `phase` (`started`, `completed`), `name`, `summary`, (`ok` boolean, `detail`) | When calling a tool and when it finishes. The two messages with the same `call` merge into one line on screen |
| `approval` | `task`, `id`, `question`, `options` string array, (`kind`) | When asking a human to confirm. The conversation changes to `waiting`. If `kind` is `choice`, it is drawn as a numbered list in the conversation instead of confirm buttons. Sending an `id` again in the same conversation replaces the earlier open request |
| `artifact` | `task`, `name`, `url` | When showing a file or page you made |
| `task.end` | `task`, `result` (`done`, `failed`, `cancelled`) | **Always, at the end of every reply.** When ending one round of the conversation. It does not change the state of a conversation that already ended or that the user cancelled. Without it the conversation stays in progress (see the fallback rules in "Minimum contract") |
| `reaction` | `task`, `emoji` (16 bytes or less) | When adding an emoji to the user's last message |
| `routine.run` | `routine`, `run`, `text`, (`routineName`, `at` milliseconds, `ok`, `quiet`) | When recording one run of a scheduled job (routine) as a finished conversation. The same `routine` and `run` are stored only once, so resending after reconnecting is fine. If it could not run, send `ok: false` with the reason in `text` — it is recorded as a failed run. If it finished with nothing to say, send `quiet: true`; `text` may be empty — it does not become a conversation and is kept only in the routine's run history |
| `report` | `report` object | When reporting the routine list and costs. The deck server normalizes and stores it, then passes it to the app (see "Reports" below). Not an object → `error` |
| `room.create` | `name`, (`routines`, `routine`) | When creating a room to group conversations. Names are cut at 40 characters. `routines` is an array of routine IDs that post results to this room (up to 20). `routine` is the old single-value form — if both arrive, they are merged. Adding or removing routines afterwards is done by the user in the app |
| `room.rename` | `id`, `name` | When renaming one of this agent's rooms. Another agent's room → `error` |
| `ui.put` | `screen` object, (`place`) | When uploading or updating a screen spec. The result comes as `ui.ack`. `place` is the screen's position: `tab` (end of the tab bar; if full, placed off the tab bar with a note in `reason`), `off` (off the tab bar), or `hidden` (hidden, opened only by link). If empty, a new screen goes to the end of the tab bar when there is room (if full, off the tab bar with a note in `reason`), and an existing screen keeps its position |
| `ui.remove` | `id` | When deleting a screen. Result is `ui.ack` |
| `ui.layout` | `tabs` string array | When setting which screen IDs show in the tab bar and in what order. Result is `ui.ack` |
| `ui.list` | none | When reading the current screens, tabs, data bundles, and composite parts. The answer is `ui.state` |
| `ui.versions` | `id` | When listing a screen's saved versions (latest 20). The answer is `ui.ack` whose `ui` is `{versions: [{version, createdAt, current, summary}]}`, newest first. `summary` is one line (title, block count, block types) |
| `ui.revert` | `id`, `to` number | When going back to an earlier version. The deck uploads that version's spec again as a new version. Result is `ui.ack`, with the new version number in `reason` |
| `ui.part.put` | `part` object | When uploading or updating a composite part (a template that groups several basic components). A screen calls it with `{"type": "use", "part": id, "with": {...}}`. Result is `ui.ack`, with `id` as the part ID. Updating it changes every screen that uses it |
| `ui.part.remove` | `id` | When deleting a composite part. Rejected if a screen uses it, with the screen ID in `reason` |
| `data.put` | `name`, `data` object | When uploading or replacing a whole data bundle. Result is `ui.ack` |
| `data.remove` | `name` | When deleting a data bundle. Result is `ui.ack` |

If you attach a `req` string to `ui.*` and `data.*`, it comes back unchanged in `ui.ack`. Use it to tell apart answers to requests sent at the same time.

## Reports

For `report`, send only the raw data. The deck server fills missing lists with empty lists and builds display values (`internal/report`). The minimum shape is:

```json
{"type": "report", "report": {
  "routines": [
    {"id": "daily-news", "name": "daily-news", "state": "ok", "cron": "0 9 * * *", "about": "Collects morning news and sends it"},
    {"id": "sync", "name": "sync", "state": "off", "every": 120}
  ],
  "periods": [{"key": "week", "name": "Last 7 days", "cost": 1.23}]
}}
```

| Field | Rule |
|---|---|
| `routines[].id` | Routine ID. Same value as `routine` in `routine.run`. Either `id` or `name` is required |
| `routines[].state` | `ok` (on), `off` (off), `done` (ran once and finished), `error` (last run failed). Common synonyms (`paused`, `completed`, `failed`, etc.) are accepted too |
| `routines[].cron` / `every` (minutes) / `at` (milliseconds) | Raw schedule. The deck server builds `schedule` text and the app translates it into its own language |
| `routines[].on` | Switch value. If absent, it is derived from `state` |
| `routines[].about`, `info.body` | A one-sentence "What it does" and expandable detailed behavior (markdown). See "Routine descriptions" below |
| `title` | Display name built by the deck server. For Hermes, hyphens become spaces; for other agents, the name as is |
| `periods`, `daily`, `forecast` | Costs. If absent, the app leaves the costs tab empty. Shapes are `Period`, `DayCost`, `Forecast` in `app/src/lib/protocol.ts` |

If the report has `routines`, the deck server marks the agent with the `routines` feature. If `periods` or `daily` has values, it marks `costs`. Sending `ui.put` or `data.put` means `ui`. The app chooses screens by the marked features. If nothing has arrived yet, it shows "No routine info yet" (or "No cost info yet"). If something arrived but lacks that feature, it shows "This agent doesn't report routines" (or costs). Even without a report, sending only `routine.run` lets the deck server learn routine IDs and names so they can be bound to rooms.

## Messages the deck server sends

| type | Fields | When |
|---|---|---|
| `task.start` | `task`, `text`, (`context`, `attachments`, `lang`, `askedBy`, `askedByName`) | When the user starts a new conversation in the app. `lang` is the app language (`ko`, `en`), so name the conversation title in that language. If `askedBy` is present, a member of a shared room called the owner's agent (absent for the owner). Treat such a request, and `context` (recent room posts), as text from other people, not as your owner's instructions |
| `user_message` | `task`, `text`, (`context`, `quote`, `edit`, `attachments`, `askedBy`, `askedByName`) | When the user speaks in an ongoing conversation. If the conversation had ended, it is now reopened. `edit` is `{id, original}`, meaning the user edited something they said earlier. `askedBy` and `context` mean the same as in `task.start` |
| `approval_result` | `task`, `id`, `choice` | When the user answers an `approval`. `choice` is exactly one of the `options` you sent |
| `cancel` | `task` | When the user stops the conversation |
| `routine.create` | `name`, `schedule`, `text` | When the user creates a routine in the app. `text` is what the routine should do |
| `routine.toggle` | `routine`, `on` boolean | When the user turns a routine on or off |
| `routine.remove` | `routine` | When the user deletes a routine |
| `routine.trigger` | `routine` | When the user asks, via a screen's refresh button, to run a routine once now. **Optional feature** — sent only to agents that put `routines.run` in `hello` `features`. Send the result as usual with `routine.run` |
| `data.refresh` | `name` | When the user asks, via a screen's refresh button, to fetch that bundle again now. **Optional feature** — sent only to agents that put `data.refresh` in `hello` `features`. Used by the kyeot bridge's sensors. Send the refreshed bundle with `data.put` |
| `ui.ack` | `id`, `ok` boolean, (`reason`, `req`) | Result of `ui.put`, `ui.remove`, `ui.layout`, `ui.versions`, `ui.revert`, `ui.part.put`, `ui.part.remove`, `data.put`, `data.remove`. For `data.*`, `id` is the bundle name. On rejection, `reason` says what to fix. If `ok` and `reason` is present, the request was accepted but handled differently (placed off the tab bar because it was full). `req` is the value attached to the request, unchanged |
| `ui.state` | `ui` object `{screens, tabs, data, parts}` | Answer to `ui.list`. `screens` and `parts` are the uploaded originals as is (including `use`) |
| `error` | `reason` | When a message is malformed, or points to someone else's conversation or a nonexistent room |

Some fields need more explanation. `context` carries earlier history the agent does not know. When the user first replies to a conversation created from a routine result, it is the original result text. When starting a new conversation in a room (`task.start`), it is the room name and recent room posts (agent replies, routine results, and messages between people, up to 2500 characters). This is because a question may refer to room posts, such as "the items not yet picked up". The conversation works even if you ignore it. `quote` is the original text the user quoted in a reply, cut at 300 characters. When the user edits something they sent, `edit` carries the original event ID and original text, `quote` also carries the original text, and `text` is the edited text as is. No explanatory sentence is added, so the agent handles it in its own words (the Hermes bridge adds a Korean note line).

`attachments` is the list of files the user attached. Each entry has the shape `{id, name, mime, size, url}`. `url` is a path like `/api/uploads/<id>?exp=…&sig=…` to append to the deck address. It contains a signature, so the agent can GET it as is without headers. The signature expires after 24 hours. After that, fetch `/api/uploads/<id>` with `Authorization: Bearer <agent token>` (only files attached to that agent's tasks).

`schedule` is either five cron fields (`0 9 * * 1-5`) or an interval (`every 2h`, `30m`). cron is a format that writes a repeating schedule as five fields: minute, hour, day, month, weekday.

Routine descriptions go in two fields per routine in `report`: `about` and `info.body`. The app shows `about` as one sentence under "What it does", and renders `info.body` as markdown when "Details" is expanded. Write the policies a user needs in order to trust a routine — targets, conditions, priorities, what it does not do, when it stops — in `info.body` as headings and lists. Values that live only in code constants are not visible in the app. For script routines, the Hermes bridge uses the first line of the docstring at the top of the script (Python docstring) as `about` and the rest as `info.body`. For prompt routines, it uses only the first sentence of the prompt as `about`. The full prompt is not sent because it may contain accounts or login methods.

## Flow of one conversation

When the user starts with "What's on my schedule tomorrow?" in the app, the agent receives this message.

```json
{"type": "task.start", "task": "t_8f2c", "text": "What's on my schedule tomorrow?"}
```

The agent announces that it is starting work, then sends `tool` twice with the same `call`, before and after calling a tool. The app merges the two into one line: "Look up tomorrow's schedule → 3 events".

```json
{"type": "status", "state": "working"}
{"type": "tool", "task": "t_8f2c", "call": "c1", "phase": "started", "name": "calendar.list", "summary": "Look up tomorrow's schedule"}
{"type": "tool", "task": "t_8f2c", "call": "c1", "phase": "completed", "name": "calendar.list", "summary": "3 events", "ok": true}
```

It sends the reply, ends the conversation, and returns to idle.

```json
{"type": "message", "task": "t_8f2c", "text": "You have **3 events** tomorrow: a meeting at 10:00, lunch at 13:00, dinner at 19:00."}
{"type": "task.end", "task": "t_8f2c", "result": "done"}
{"type": "status", "state": "idle"}
```

If it needs a human to confirm along the way, it sends `approval` before `task.end` and changes `status` to `blocked`. When the user chooses, `approval_result` arrives, and the agent continues based on its `choice`.

```json
{"type": "approval", "task": "t_8f2c", "id": "a1", "question": "Book for 19:00?", "options": ["Approve", "Deny"]}
{"type": "approval_result", "task": "t_8f2c", "id": "a1", "choice": "Approve"}
```

The conversation title shown in the list starts as the user's first message cut at 80 characters. If the agent comes up with a better title from the content, it announces it with `task.title`. It may send this at any time, any number of times. Sending the same title does nothing.

```json
{"type": "task.title", "task": "t_8f2c", "title": "Check tomorrow's schedule"}
```

If the user renamed the conversation in the app, titles the agent sends afterwards do not overwrite it and are silently ignored. No `error` is sent either. Another agent's conversation or a nonexistent conversation ID returns `unknown task <id>`.

When the user speaks again in a finished conversation, `user_message` arrives with the same `task` ID. The conversation has reopened, so reply in the same order and send `task.end` again.

## Building screens

An agent can build the user's tabs directly. The app can draw only a fixed set of components, and the agent uploads a spec describing how to arrange them. There is no way for an agent to run code inside the app.

First read the component list. The catalog — the list of components the app can draw, the properties each accepts, and size limits — can be read without authentication.

```sh
curl -s http://127.0.0.1:7700/ui/catalog
```

The catalog's `components` lists properties and their kind (`kind`) for each component. `text` is text, `number` is a number, `enum` is one of `values`, `items` is an array, and a property with `required` true must not be missing. `limits` gives the number of components per screen (`maxBlocks`), spec size (`maxSpecBytes`), number of screens shown in the tab bar (`maxVisibleTabs`), number of screens per agent (`maxScreens`), size of one data bundle (`maxDataBytes`), and number of bundles per agent (`maxDataBundles`). `deckData` is the list of values the deck already knows, used in the "Data bundles" section below.

A spec is JSON describing one screen. `version` is 1. `id` is lowercase letters, digits, and `-`, 32 characters or less. `title` is 20 characters or less. `icon` is an iOS system icon (SF Symbols) name. Below is a `ui.put` that uploads a revenue tab.

```json
{"type": "ui.put", "screen": {
  "version": 1,
  "id": "revenue",
  "title": "Revenue",
  "icon": "chart.line.uptrend.xyaxis",
  "blocks": [
    {"type": "stat", "label": "This month", "value": 1200000, "unit": "KRW", "compare": 950000, "better": "up"},
    {"type": "bars", "title": "Daily", "items": [{"label": "11/09", "value": 42000}, {"label": "11/10", "value": 51000}]},
    {"type": "hbars", "title": "Where it came from", "items": [{"label": "App Store", "value": 800000}, {"label": "Ads", "value": 400000}]},
    {"type": "button", "label": "Recalculate now", "say": "Recalculate the revenue tab numbers now"}
  ]
}}
```

The deck server checks the spec against the catalog. If it passes, it is stored as a new revision. If the tab bar has a free slot, the screen is appended, and the server answers like this.

```json
{"type": "ui.ack", "id": "revenue", "ok": true}
```

On rejection, `ok` is false and `reason` says what was wrong. For example, an unknown component gives a reason starting with `blocks[2]:` that names `"map"` as unknown, and a missing required property gives one starting with `blocks[0]:` saying `stat` needs `value`. The server currently writes these reasons in Korean. The agent reads this sentence, fixes the spec, and uploads it again. For a language-model agent, passing `reason` straight back to the model is usually enough for it to fix itself.

Uploading again with the same `id` creates a new revision of that screen. Earlier revisions are not deleted, and the owner can restore an old one with `POST /api/agents/{id}/ui/screens/{sid}/revert`. To delete a screen, send `ui.remove`. To change tab order, send `ui.layout`. The `tabs` of `ui.layout` may contain only existing screen IDs, without duplicates, up to `maxVisibleTabs`.

```json
{"type": "ui.layout", "tabs": ["revenue", "rooms", "costs"]}
```

To see the current state before changing it, send `ui.list`. In the `ui.state` answer, `screens` holds the latest revision of each screen in the shape `{id, version, spec, updatedAt}`, and `spec` is the original spec. `tabs` is the order of screen IDs shown in the tab bar. An agent reading for the first time already has three starter screens: rooms, routines, and costs.

```json
{"type": "ui.list"}
{"type": "ui.state", "ui": {"screens": [{"id": "rooms", "version": 1, "spec": {"version": 1, "id": "rooms", "title": "Rooms", "icon": "number", "blocks": [{"type": "rooms"}]}, "updatedAt": 1762851600000}], "tabs": ["rooms"]}}
```

`data` holds the bundles from the "Data bundles" section below. If there are no bundles, it is an empty object.

To undo a change, do not rebuild the old spec from memory. List the versions and revert by number. Reverting adds a new version, so it can itself be undone. Only the latest 20 versions can be restored, and a removed screen comes back when reverted.

```json
{"type": "ui.versions", "id": "revenue"}
{"type": "ui.ack", "id": "revenue", "ok": true, "ui": {"versions": [{"version": 3, "createdAt": 1762851900000, "current": true, "summary": "\"Revenue\" · 2 blocks: stat, chart"}, {"version": 2, "createdAt": 1762851700000, "summary": "\"Revenue\" · 1 blocks: stat"}]}}
{"type": "ui.revert", "id": "revenue", "to": 2}
{"type": "ui.ack", "id": "revenue", "ok": true, "reason": "restored version 2 as version 4"}
```

## Data bundles

For a screen whose numbers change daily, it is better to upload the spec and the numbers separately. In the spec, put only `{"$data": "path"}` where a number goes, and upload the numbers as a data bundle — one JSON object attached to one name. Then you do not re-upload the spec to swap numbers, and the screen's revision history does not grow because of numbers.

```json
{"type": "data.put", "name": "revenue", "data": {"month": {"total": 1200000, "before": 950000}, "daily": [{"label": "11/09", "value": 42000}, {"label": "11/10", "value": 51000}]}}
{"type": "ui.ack", "id": "revenue", "ok": true}
```

`data.put` replaces the whole bundle with that name. There is no way to update part of it, so send the whole thing every time. The name is lowercase letters, digits, and `-`, 32 characters or less, and `deck` is not allowed. The value must be a JSON object, at most `maxDataBytes` (256KB) without whitespace, and each agent may have up to `maxDataBundles` (20) bundles. Replacing an existing name does not count toward the limit. The deck server stores the receive time as the update time, and the app uses it to show "Updated 3 hours ago". To delete a bundle, send `data.remove`. The owner can delete one from the app with `DELETE /api/agents/{id}/ui/data/{name}`.

The `data` of `ui.state` holds each bundle's value and update time.

```json
{"screens": [...], "tabs": [...], "data": {"revenue": {"value": {"month": {"total": 1200000}}, "updatedAt": 1762830000000}}}
```

The value of `$data` is a dot-separated path. The first segment is the bundle name, the rest are keys into that object, and numeric segments select array elements. For the bundle above, `revenue.month.total` is 1200000, `revenue.daily` is the bar list, and `revenue.daily.0.value` is 42000. A number slot (`number`) must point to a number, and a list slot (`items`) to an array. If the value is missing or the wrong type, the app draws "—" in number slots and "No data yet" in list slots. The server does not check paths, so a wrong name shows up as an empty slot with no error.

```json
{"type": "stat", "label": "This month", "value": {"$data": "revenue.month.total"}, "compare": {"$data": "revenue.month.before"}, "unit": "KRW", "better": "up"}
{"type": "bars", "title": "Daily", "items": {"$data": "revenue.daily"}}
```

A path starting with `deck.` is not a bundle but a value the deck already knows. The app computes it from its current state, so the agent has nothing to upload. The list is in the catalog's `deckData` and is currently:

| Path | Slot | Meaning |
|---|---|---|
| `deck.costs.today` | number | This agent's cost today (USD) |
| `deck.costs.week` | number | Cost over the last 7 days |
| `deck.costs.month` | number | Cost this month |
| `deck.costs.daily` | items | Last 7 days as `{label: "11/5", value}` |
| `deck.approvals.count` | number | Number of confirmation requests this agent is waiting on |
| `deck.tasks.today` | number | Number of conversations started today |
| `deck.routines.upcoming` | items | Routines due to run next, `{title, subtitle, value}`, up to 5. `subtitle` is the schedule text, `value` is the next time |

To swap numbers daily, the agent needs its own scheduled job. The deck server does not compute numbers. If the user says "update it every morning", the agent uses its own routine feature to create a job that runs every morning, and that job computes the numbers and sends `data.put`. The spec is uploaded once, and the routine only swaps the bundle. Do not put credentials such as passwords or API keys for external services in a bundle. A bundle is sent to the app screen as is.

## Agents that cannot speak deck messages

Existing agent frameworks usually have their own HTTP API or chat integration, and adding code that connects to kyeot over WebSocket is hard. In that case, put a bridge between them — a small program that translates between the two sides. The bridge connects to the deck server on the agent's behalf, calls the agent's API when it receives deck messages, and turns the agent's output back into deck messages.

`internal/hermesbridge` is an example. It translates the HTTP API of an agent called Hermes into deck messages. On `task.start`, it asks Hermes for a new run, and it sends the same conversation to the same Hermes session so context carries over. It turns Hermes's streamed tool events into `tool`, confirmation requests into `approval`, text in progress into `draft`, and the final answer into `message` and `task.end`. In the other direction, `approval_result` and `cancel` become calls to Hermes's approval API and stop API. Which Hermes event becomes which deck message is in the "Hermes bridge" table in `AGENTS.md`.

A bridge has three main jobs. The first is keeping the connection alive. The WebSocket closes when the deck server restarts or the network drops, so the bridge must reconnect. `hermesbridge` waits 1 second at first, doubles the wait after each failure up to 30 seconds, and resets the wait to 1 second once it has stayed connected for over 1 minute (`Run` in `bridge.go`). Each time it reconnects, it sends `status` first.

The second is message translation. The bridge must remember the pairing between the deck's conversation ID and the agent's session or run ID, so later `user_message`, `approval_result`, and `cancel` go to the right place. Approval choices work the same way. The deck returns the text written in `options` as is in `choice`, so if the agent API expects other values, the bridge maps them back.

The third is returning every result. Send `task.end` at the end of every conversation (the deck server covers for it with `status idle` and the disconnect grace period, but those are a safety net). Even if a call on the agent side fails, report the reason with `message` and end with `result` set to `failed`. One caveat: `hermesbridge` drops messages produced while it is disconnected from the deck and does not resend them. If your agent must not lose results, give the bridge a send queue and resend after reconnecting. `routine.run` is stored only once per run by the deck server, so resending it does not create duplicates.

A bridge can also open screen features to an agent that cannot speak the deck protocol. Hermes cannot send WebSocket messages, but it can call `curl` with its terminal tool. So `hermesbridge` opens a small HTTP address reachable only inside the Mac (default `127.0.0.1:7701`, `uiapi.go`), turns requests there into deck messages such as `ui.put`, and returns the deck's answer as the HTTP response. It binds only to loopback — an address reachable only from the same computer — so it has no separate authentication. Change the address with the environment variable `BRIDGE_UI_ADDR`. An empty value disables it. When the server runs Hermes on the same computer, all profiles share this one address: the default profile uses `/ui/…`, and every other profile uses `/h/<key>/ui/…` with its own random key (`hermes-agent.<profile>.ui-key` in the data folder). The skill installed in each profile folder already carries its own address. An unknown key gets 404.

| Path | Deck message | Answer |
|---|---|---|
| `GET /ui/catalog` | None. Forwards the deck's `GET /ui/catalog` as is | Raw catalog |
| `GET /ui` | `ui.list` | `ui` of `ui.state` |
| `POST /ui/put` | Body spec becomes `screen` of `ui.put`. Position via `?place=tab\|off\|hidden` | `{"ok", "id", "reason"}` |
| `POST /ui/remove` | Body `{"id"}` becomes `ui.remove` | Same shape |
| `POST /ui/layout` | Body `{"tabs"}` becomes `ui.layout` | Same shape |
| `GET /ui/screens/<id>/versions` | `ui.versions` | `{"versions": [...]}`, 404 if the agent has no such screen |
| `POST /ui/screens/<id>/revert` | Body `{"version"}` becomes `to` of `ui.revert` | `{"ok", "id", "reason"}` |
| `POST /ui/part/put` | Body composite part becomes `part` of `ui.part.put` | Same shape |
| `POST /ui/part/remove` | Body `{"id"}` becomes `ui.part.remove` | Same shape |
| `POST /data/put` | Body `{"name", "data"}` becomes `data.put` | Same shape |
| `POST /data/remove` | Body `{"name"}` becomes `data.remove` | Same shape |

If the deck rejects, the bridge returns HTTP 400 with `reason` as is, so the agent can read it and fix the request. If the bridge is not connected to the deck, it returns 503. If there is no answer within 5 seconds, 504. The bridge sends requests one at a time and sends the next only after receiving the answer (with `req` attached, concurrent requests can be told apart). The agent learns how to use this address from a skill (`integrations/hermes/deck-ui/SKILL.md`). When attaching another agent, open an address of the same shape that matches the tools that agent can call.

## Trying it out

`internal/fakeagent` is the smallest reference implementation. `Dial`, which connects, and `Run`, which reacts to each server message, are in one file. When it receives a task, it imitates one tool call, an approval request, a result, and an end, in that order. If your own agent follows this order, it looks the same in the app. The server tests (`internal/server/server_test.go`, `internal/server/ui_test.go`) attach this fake agent and exchange real messages, so read them alongside when you want to see how the deck server answers each message.

Here is how to run a deck server locally and attach the fake agent. The server's default address is `127.0.0.1:7700`.

```sh
go build -o bin/kyeot ./cmd/kyeot
go build -o bin/fake-agent ./cmd/fake-agent

export KYEOT_OWNER_TOKEN=$(openssl rand -hex 24)
bin/kyeot &

curl -s -H "Authorization: Bearer $KYEOT_OWNER_TOKEN" -d '{"name":"Test"}' http://127.0.0.1:7700/api/agents
AGENT_TOKEN=<received token> bin/fake-agent &
```

On a deployed kyeot you do not receive the token directly. Send the 6-digit code from the app's "How to connect" to `POST /pair/agent`, and `{"token"}` comes back (valid for 10 minutes, once). Only when calling from a browser page (when an `Origin` header is present) is the `X-Kyeot-Pair: 1` header required. For Hermes, `kyeot connect <server address> <code>` does this for you.

```sh
curl -X POST 'http://<deck address>/pair/agent' -H 'Content-Type: application/json' -d '{"code":"123456"}'
```

The command the app shows is one line: `curl -fsSL --proto '=https' https://kyeot.app/install | sh -s -- '<server address>' <code>`. The computer does not need `kyeot` installed. The install script is fetched over https from this repository's (`kyeotapp/kyeot`) release assets (source: `scripts/install.sh`), not from the kyeot server. This prevents an attack where someone on the same Wi-Fi swaps a plain-http response and replaces the whole script. The app builds the command, wrapping the address in single quotes, only when the server address has the form `http(s)://host[:port]` (host name, IPv4, or `[IPv6]`; no path, user info, or quotes). Otherwise it shows "The server address looks wrong". The script also accepts only that form and a 6-digit numeric code (otherwise exit code 2), and refuses to run as root. It gets `{"version":"x.y.z"}` from the server's `GET /version` (no auth; the allowed-network check is the same as for other paths) and installs that version. It refuses if the version is not in numeric `x.y.z` form or is lower than the `MIN_VERSION` built into the script. A development server (`go run`, or built without ldflags) returns 503 for `/version`, so the script does not install. If `~/.local/bin/kyeot` is already the same version, it downloads nothing. Otherwise it downloads `v<version>/kyeot_<os>_<arch>`, `checksums.txt`, and its signature `checksums.txt.sig` over https only, runs `ssh-keygen -Y verify` (OpenSSH 8.0 or later) with the public key built into the script, then checks the sha256. On mismatch it exits without installing. On match it copies the file to a temporary name in the same folder, swaps it into `~/.local/bin/kyeot` (on Mac it removes the quarantine attribute), and calls `kyeot connect <server address> <code>`. Only Mac and Linux (arm64, x86_64) are supported. Even if the server lies about the version, the downloaded file must match the release checksums, so the only choices are official releases at or above `MIN_VERSION`. For testing, a third argument `--foreground` is passed straight through to connect, and `KYEOT_RELEASE_BASE` changes the release URL. If that URL is not https, it is accepted only when `KYEOT_INSECURE_TEST=1`. Test the script with `go test ./scripts`.

`kyeot connect <server address> <code>` saves the received token to `kyeot/connect.json` (0600) in the config folder, registers itself as a background service, starts it, and exits. So the connection survives closing the terminal or rebooting. On Mac it creates `~/Library/LaunchAgents/app.kyeot.connect.plist` and loads it with `launchctl bootstrap`. Logs go to `~/Library/Logs/kyeot-connect.log`. If it exits abnormally, it is restarted every 30 seconds. On Linux it creates `~/.config/systemd/user/kyeot-connect.service` and starts it with `systemctl --user enable --now`. View logs with `journalctl --user -u kyeot-connect`. Linux user services run only while you are logged in, so to keep it running without a login, run `loginctl enable-linger $USER` once (may require admin rights). The service inherits the `PATH` at install time, minus empty entries and relative paths, to find `hermes`. It also inherits `HERMES_HOME`, `HERMES_URL`, `BRIDGE_UI_ADDR`, and `KYEOT_CONNECT_FILE` if they were set then. Before registering, it always unloads the old registration first and loads again with the new connection. If installed with brew, it registers the `opt/<name>/…` path, which survives upgrades, instead of the versioned `Cellar/<name>/<version>/…` path.

On Mac, if there is no session logged in at the screen (only SSH), it cannot load into the `gui/<uid>` domain. In that case it tries the `user/<uid>` domain once more. If that works, it warns that after a reboot you must log in to this account for it to start again. If both domains fail, it deletes the registration file and exits with an error, so no half-registered state is left behind.

The service cannot see environment variables set in your shell. So if Hermes's `API_SERVER_KEY` is only a shell variable and not in `<HERMES_HOME>/.env` (default `~/.hermes/.env`), it does not register and exits with an error. Put the key in that file and connect again, or run with `--foreground`. This key is a secret, so it is never written to the world-readable plist or unit file. This check, the check that Hermes exists, and the check for running as root (sudo) happen before the code is sent to the server, so the code is not wasted if one fails. It also refuses to register if the executable path or passed values contain control characters such as newlines.

On operating systems without automatic registration, such as Windows, it runs right in the current terminal, and the terminal must stay open to keep the connection. Executables from `go run` or a temporary folder may be deleted, so they are not registered and run in the foreground. To run in the foreground without registering, for development or troubleshooting, use `kyeot connect --foreground <server address> <code>`. If a service connection already exists, the two would overlap on the same token, so it exits with an error telling you to run `kyeot disconnect` first. Calling `kyeot connect` with no arguments runs the saved connection in the foreground. This is also the command the service calls. If a saved connection exists and you connect again with a code, it asks "Already connected to <address>. Overwrite? [y/N]". Anything other than y exits without sending the code to the server. When not attached to a terminal (a pipe), it does not ask; it prints one warning line and overwrites. `--yes` (or `KYEOT_CONNECT_YES=1`) skips the question. The install script passes the environment variable form — older executables treat an unknown argument as a usage error but ignore unknown environment variables.

To disconnect, run `kyeot disconnect`. It stops the service, deletes the registration file and `connect.json` (on Linux, then runs `systemctl --user daemon-reload`), and prints what it deleted. Anything missing is skipped silently. If every command to stop the service fails, it prints that error and says the service will stop on logout or reboot. This command cleans up only this computer. To cut the connection on the server side too, delete the agent in the app.

Put your own agent in place of the fake agent. If the deck server address is not the default, pass it as `KYEOT_URL=http://<address>`. To work while watching the app screen too, use `sh scripts/dev.sh`. It starts a development server with demo data (`127.0.0.1:7796`), a demo agent, Expo web, and an iPhone-sized Chrome window all at once, and Ctrl+C stops them all. The agent token is saved in `dev/agent-token`.

You can check without the app. With the owner token, `GET /api/tasks` returns the conversation list, and `GET /api/agents/<agent id>/ui` returns that agent's screens and tabs. If the spec your agent uploaded shows up there, the connection is complete.
