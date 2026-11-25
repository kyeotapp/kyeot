---
name: deck-ui
description: "Build and edit tabs and screens (dashboards) in the Kyeot (곁) app. Upload a screen spec and data bundles with curl to the local address 127.0.0.1:7701, and leave daily refreshes to a cronjob routine. Also read it when creating or editing any routine (cronjob, script routine), for the description and cost rules."
version: 1.2.4
author: kyeot
license: MIT
platforms: [macos, linux]
metadata:
  hermes:
    tags: [kyeot, 곁, UI, tab, 탭, dashboard, 대시보드, screen, 화면]
---

# Building Kyeot screens (deck-ui)

**Language.** Reply in the user's language, and write tab names, titles, and labels in the user's language too. The examples below are in English; translate their visible text for the user.

The user sees the screens you make in the bottom tab bar of the Kyeot app. A screen is one spec (JSON), and the app draws only the fixed set of components in the catalog. You write the spec and upload it to the bridge's local address. The bridge passes it to the deck server, which validates it and either accepts it or rejects it with a reason.

Numbers do not go in the spec. Upload them separately as a data bundle. The spec only holds a placeholder such as `{ "$data": "revenue.month.total" }`, and the number lives in the `revenue` bundle. When the numbers change, you re-upload only the bundle.

## When to use

Use it when the user asks you to make or change a tab, screen, or dashboard. Bugs or interaction issues in the Kyeot app itself (gestures, keyboard, input box, image viewer, and so on) are not for this skill. Typical requests:

| English | 한국어 |
|---|---|
| "Make a revenue tab" | "수익 탭 만들어 줘" |
| "Add a budget bar to the costs tab" | "비용 탭에 예산 막대 넣어 줘" |
| "Reorder my tabs" | "탭 순서 바꿔 줘" |
| "I want a workout log screen" | "운동 기록 화면 보고 싶어" |
| "Make a to-do tab for today" | "오늘 할 일 탭 만들어 줘" |

Also use it for requests that only change the numbers, such as "refresh the revenue tab every morning" (「수익 탭 매일 아침 갱신해 줘」) or "recalculate the numbers" (「숫자 다시 계산해 줘」). When creating or editing any routine, even one unrelated to screens, follow the "Routine descriptions" and "Routine cost" sections.

## Address

The bridge opens this address only on the local machine. There is no authentication.

```bash
DECK=http://127.0.0.1:7701
```

| Path | What it does |
|---|---|
| `GET $DECK/ui/catalog` | Components, properties, limits |
| `GET $DECK/ui` | Current screens, tab layout, bundles `{ "screens": [{ "id", "version", "spec" }], "tabs": [...], "data": { "name": { "value", "updatedAt" } } }` |
| `POST $DECK/ui/put` | Body is the spec. If the same id exists, it becomes a new version |
| `POST $DECK/ui/remove` | Body `{ "id": "..." }` |
| `POST $DECK/ui/layout` | Body `{ "tabs": ["id", ...] }`, the tab bar order |
| `GET $DECK/ui/screens/<id>/versions` | Saved versions of one screen, newest first: `{ "versions": [{ "version", "createdAt", "current", "summary" }] }` |
| `POST $DECK/ui/screens/<id>/revert` | Body `{ "version": 3 }`. Uploads that version again as a new version |
| `POST $DECK/ui/part/put` | Body is a composite part (see "Composite parts"). If the same id exists, it becomes a new version |
| `POST $DECK/ui/part/remove` | Body `{ "id": "..." }`. Rejected while a screen uses it |
| `POST $DECK/data/put` | Body `{ "name": "revenue", "data": { ... } }`. Replaces the whole bundle |
| `POST $DECK/data/remove` | Body `{ "name": "revenue" }` |

The reply is `{ "ok": true, "id": "revenue" }`. For bundles, `id` is the bundle name. If `ok` comes with a `reason`, the deck accepted it but handled it differently from what you asked (for example, the tab bar was full so the screen went outside it). Tell the user. A rejection is HTTP 400 with `{ "ok": false, "reason": "..." }`. 503 means the bridge is not connected to the deck; 504 means the deck did not answer within 5 seconds. Tell the user either one as is.

## Steps

1. Read the catalog. Do not rely on memory for component names and properties; check here every time.
   ```bash
   curl -s $DECK/ui/catalog
   ```
2. Read the current screens. Check whether the same id exists and how many tab slots are taken.
   ```bash
   curl -s $DECK/ui
   ```
3. Ask the user what they want to see: one big number, bars by date, or a comparison by source. Also decide where the numbers come from. Skip asking if they already said.
4. Write the spec. Put changing numbers as `{ "$data": "bundle.path" }` placeholders. For values the deck already knows (see "Deck values"), point to a `deck.` path. Writing the spec to a file avoids quoting mistakes.
5. Compute the numbers, upload the bundle first, then the spec. In the other order, the screen briefly shows "—".
   If the command that computes the numbers may take long (scanning logs, many lookups), do not rely on the default 60-second `terminal` limit: raise `timeout` from the start (up to 600) or run it with `background=true`. Run Python and shell commands with `terminal`, not `browser_exec`.
   ```bash
   curl -s -X POST $DECK/data/put -H 'Content-Type: application/json' --data @/tmp/revenue-data.json
   curl -s -X POST $DECK/ui/put -H 'Content-Type: application/json' --data @/tmp/revenue.json
   ```
6. If rejected, read `reason`, fix, and upload again. It tells you which block is wrong, as in `blocks[2]: ...`. If the same reason rejects it three times, stop and show the user the reason.
7. Decide the tab layout. A new screen joins the end of the tab bar if a slot is free. If the slots are full, it goes outside the tab bar (the chip row above Home, "Outside tabs" in tab editing) and the reply's `reason` says so. Tell the user as is and reorder if they want.
   ```bash
   curl -s -X POST $DECK/ui/layout -H 'Content-Type: application/json' -d '{"tabs":["revenue","rooms","routines","costs"]}'
   ```
8. When done, tell the user in one or two sentences what you made and which tab it is in. Also ask when to recompute the numbers (just now, or daily). If daily, follow "Daily refresh" below.

## Example: revenue tab

This month 1,200,000 KRW, last month 950,000 KRW, daily bars, bars by source, and a recalculate button. The spec only holds placeholders.

```json
{
  "version": 1,
  "id": "revenue",
  "title": "Revenue",
  "icon": "chart.line.uptrend.xyaxis",
  "blocks": [
    { "type": "stat", "label": "This month", "value": { "$data": "revenue.month.total" }, "unit": "KRW",
      "compare": { "$data": "revenue.month.prev" }, "compareLabel": "Last month", "better": "up" },
    { "type": "bars", "title": "Daily", "unit": "KRW", "items": { "$data": "revenue.daily" } },
    { "type": "hbars", "title": "By source", "unit": "KRW", "items": { "$data": "revenue.bySource" } },
    { "type": "button", "label": "Recalculate now", "say": "Recalculate the revenue tab numbers now" }
  ]
}
```

For a Korean-speaking user the same spec would use `"title": "수익"`, `"label": "이번 달"`, `"say": "수익 탭 숫자 지금 다시 계산해 줘"`, and so on.

The numbers live in the `revenue` bundle (`/tmp/revenue-data.json`).

```json
{
  "name": "revenue",
  "data": {
    "month": { "total": 1200000, "prev": 950000 },
    "daily": [ { "label": "11/09", "value": 42000 }, { "label": "11/10", "value": 51000 }, { "label": "11/11", "value": 38000 } ],
    "bySource": [ { "label": "App Store", "value": 800000 }, { "label": "Ads", "value": 400000 } ]
  }
}
```

When the user taps a `button`, the app opens a new conversation with you and sends `say`. When you receive that message, recompute the numbers and re-upload only the `revenue` bundle. Leave the spec alone.

## Bundle shapes for template tabs

When the user adds a template tab in the app and asks you to "fill it in", the tab already exists. Do not touch the spec; `/data/put` with exactly the names and shapes below. The request text carries the same shape. If key names differ, that slot stays empty and the deck's `reason` tells you which path is off. Then fix it and upload again.

<!-- gen-template-docs:start (generated by scripts/gen-template-docs.mjs; do not edit by hand) -->
| Tab id | Bundle | Shape |
|---|---|---|
| `revenue` | `revenue` | `month{total,prev}`, `daily[{label,value}]`, `bySource[{label,value}]` |
| `habits` | `habits` | `streak`, `goals[{value,goal}]`, `today[{title,value}]`, `days[{date,value,entries[{title,time,note}]}]` |
| `todos` | `todos` | `open`, `dueToday`, `items[{title,subtitle,value}]` |
| `workout` | `workout` | `week{count,minutes}`, `daily[{label,value}]`, `recent[{title,subtitle,value}]` |
| `assets` | `assets` | `total`, `prev`, `byKind[{label,value}]`, `months[]`, `series[{name,values[]}]` |
| `reading` | `reading` | `links[{title,subtitle}]` |
| `project` | `project` | `milestones[{value,goal}]`, `week[[…]]` |
<!-- gen-template-docs:end -->

(Columns: tab id, bundle, shape.)

## Example: My Computer tab

Kyeot measures the My Computer tab (network, disk, CPU, memory, battery) itself (sensor template). Agents do not build it. Kyeot's sensors upload the `netwatch`, `disk`, and `system` bundles, so do not `data.put` under those names, and do not create monitoring scripts or routines for them.

## Component options

Always check component names and properties in the catalog (`GET $DECK/ui/catalog`). Below are the rules for using options. Without options you get the default look. When the user mentions color, emphasis, or hiding, use these options.

| Kind | Value | Example |
|---|---|---|
| Color (`color`, `aColor`, `bColor`) | A name from the catalog `colors`, or `#RRGGBB`. Prefer names (they adapt to light and dark) | `"color": "purple"` |
| Toggles (`show…`) | `true` / `false`. On by default | `"showPercent": false` |
| Decimal places (`decimals`) | 0–6 | `"decimals": 1` |
| Sort (`sort`) | `input` (as given) / `value` (largest first) | `"sort": "value"` |
| Per-item color | `color` on each list item. Overrides the block `color` | `{ "label": "Ads", "value": 3, "color": "danger" }` |
| Tags (`tags`) | Small chips to the right of the name. An array of strings or `{ "label", "color" }` | `"tags": ["Kitchen", { "label": "New", "color": "ok" }]` |

Options per component:

| Component | Block options | Item options |
|---|---|---|
| `heading` | `align` (left, center), `color` | |
| `text` | `align`, `size` (body, sub). Do not use Markdown in centered or small text | |
| `stat` | `color`, `decimals`, `showCompare`, `showChange` | |
| `stats` | `color`, `decimals`, `style` (`row` default; `tiles` puts a big number and small unit in a tile per item) | `color`, `decimals` (that item only) |
| `bars` | `color`, `decimals`, `showHead`, `showTicks` | `color` |
| `hbars` | `color`, `decimals`, `showValue`, `showPercent`, `sort` | `color` |
| `line`, `area` | `unit`, `decimals`, `showLegend`, `showDots`, `showTicks` | series `color` |
| `stackbars`, `groupbars` | `decimals`, `showLegend`, `showHead`, `showTicks` | series `color` |
| `progress` | `color`, `decimals`, `showPercent`, `showGoal` | |
| `spark` | `color`, `decimals`, `showChange` | |
| `donut` | `decimals`, `showLegend`, `showPercent`, `showTotal`, `centerLabel`, `sort` | `color` |
| `gauge` | `color`, `decimals`, `showRange` | |
| `heatmap` | `color`, `decimals`, `showHead`, `showTicks` | |
| `calendar` | `color`, `showHead`, `days` (period to draw, 1–371, ending at the last date) | `date` must be `YYYY-MM-DD`, otherwise that item is dropped. `entries` `[{title, time, note}]` (up to 50) is what happened that day; tapping the cell lists it. Without `value`, the count of `entries` is used |
| `scatter` | `color`, `decimals` | `color` |
| `funnel` | `color`, `decimals`, `showPercent`, `showStep` | `color` |
| `treemap` | `decimals`, `showValue`, `showPercent` | `color` |
| `rank` | `limit`, `highlight` (highlight the top N, default 3, 0 for none), `color`, `sort` (default value), `decimals`, `showRank`, `showBar`, `showValue` | `tags`, `accent` (force highlight), `color` |
| `list` | | `tags`, `color` (color of the value on the right) |
| `table` | `decimals` | |
| `keyvalue` | `decimals` | `tags`, `color` (value color) |
| `checklist` | `color` (check color), `showProgress` | `tags`, `say` (sent to you when the user checks it; on receipt, fix `done` and re-upload the bundle), `undo` (sent when unchecked; without it the item cannot be unchecked) |
| `timeline` | | `color` (dot color, overrides `tone`), `tags` |
| `status` | `showNote` | `color` (instead of the `state` color), `tags` |
| `connections` | `showAccount` | `icon` (Feather name), `action` (`label`, `say` or `url` (https), `kind`) |
| `badges` | | `color` (overrides `tone`) |
| `callout` | `color` (instead of the `tone` color), `showIcon` | |
| `image` | `fit` (cover, contain) | |
| `link` | `color` | |
| `countdown` | `color`, `showDate`. `to` must be an ISO time like `2026-12-24T18:00:00+09:00`, otherwise it shows "—" | |
| `compare` | `aColor`, `bColor`, `decimals`, `showBar`, `showDiff` | |
| `search` | `placeholder`, `targets` (`title` of the lists to filter), `fields` (field names to search; column names for tables) | |
| `button` | `kind` (accentSoft, primary, tonal, danger) | |
| `refresh` | `label` (default "Refresh" in the app's language), `say`, `routine`, `data`, `bundles` (array of sensor bundle names, to refresh several with one button), `kind`, `style` (button: big button, default; icon: round icon next to the refresh time; text: one line of text) | |
| `divider` | `color` | |

In `labels` of `line` and `area`, put each point's time as is (for example `15:02:20`). The app shows only a few ticks where the label changes, fitted to the chart width, and when the user taps or drags across the chart it shows that point's label and per-series values on the top line. Do not blank labels or round them to minutes to reduce ticks.

Inside items, the deck only checks item names, `color`, and `link`. The fixed-value fields below are not checked, so wrong values are still saved, and the app draws unknown values with the default look. If you want text to show, put it in a text field such as `note` or `subtitle`, not in these fields.

| Component | Field | Allowed values | If unknown |
|---|---|---|---|
| `status` | `state` | `ok`, `warn`, `danger`, `off` (only sets the traffic-light color) | Grey light with that text. Still put text like "Normal" in `note` |
| `connections` | `state` | `connected`, `expired`, `error`, `off` | Shown as "Not connected" |
| `connections` | `action.kind` | `accentSoft`, `primary`, `tonal`, `danger` | `tonal` |
| `badges` | `tone` | `ok`, `warn`, `danger`, `info`, `neutral` | `neutral` |
| `timeline` | `tone` | `ok`, `warn`, `danger`, `info` | Grey dot |

Example: "Rank the best sellers, highlight only #1, and show the category next to each" (「많이 팔린 것 순위, 1등만 강조하고 분류를 옆에 달아 줘」)

```json
{ "type": "rank", "title": "Best sellers", "unit": "pcs", "highlight": 1,
  "items": [ { "label": "Stickers", "value": 342, "tags": ["Stationery"] },
             { "label": "Mug", "value": 128, "tags": ["Kitchen", "New"] } ] }
```

Example: "Show the apps connected to me" (「나한테 연결된 앱 보여 줘」). The list goes in a bundle and the spec only holds the placeholder. Give the button either `say` or `url`, whichever the user wants. With `say`, the message comes to you, you handle it on this computer, and you answer in the conversation. A `url` (https) opens directly in the phone's browser. A login URL that sends its result back to this computer's localhost will not finish when opened on the phone, so tell the user once when you use such a URL as `url`.

```json
{ "type": "connections", "title": "Connected apps", "items": { "$data": "apps.list" } }
```

```json
{ "name": "apps", "data": { "list": [
  { "name": "Google Calendar", "state": "connected", "account": "me@example.com", "icon": "calendar" },
  { "name": "Gmail", "state": "off", "icon": "mail", "action": { "label": "Connect", "say": "Connect Gmail", "kind": "primary" } } ] } }
```

Example: "Let me search the order list" (「주문 목록에서 찾을 수 있게 해 줘」). The search box filters in the app without asking the server. Without `targets`, it filters list components below it up to the next search box; with `targets`, it filters only lists with that `title`. A `title` that is not on this screen makes the deck reject the spec. Without `fields`, it searches all visible text (title, subtitle, value, tags).

```json
{ "type": "search", "placeholder": "Search by name or category", "targets": ["Orders"], "fields": ["title", "tags"] },
{ "type": "list", "title": "Orders", "items": { "$data": "shop.orders" } }
```

### Calendar with a daily log

Set `days` to the period in the title ("Last 14 days" → `14`). 6 weeks or less draws big cells, 7 per row; longer periods draw a small weekday × week grid. Put what was done that day in `entries`, so tapping a cell shows it.

```json
{ "type": "calendar", "title": "Last 14 days", "days": 14, "items": { "$data": "habits.days" } }
```

```json
{ "days": [
  { "date": "2026-11-24", "value": 2, "entries": [{ "title": "Water 2L", "time": "21:10" }, { "title": "Stretch 10 min", "time": "07:30", "note": "morning" }] },
  { "date": "2026-11-25", "value": 1, "entries": [{ "title": "Read 20 pages", "time": "23:05" }] }
] }
```

## Refresh button

A button that reloads numbers inside a screen is `refresh`, not `button`. A `button` jumps to the conversation, but `refresh` stays on the screen, switches to a "refreshing…" state, and finishes when the `data` bundle is uploaded again.

| Who computes the screen's numbers | Fields to use |
|---|---|
| A script routine (`no_agent`) | `routine` with the routine id from `jobs.json`, `data` with the bundle name that routine uploads. No LLM call, so no cost |
| You compute them | `say` with a refresh request sentence, `data` with the bundle name. Each tap creates a conversation |

You may set both `routine` and `say`. The app uses `routine` if it can run the routine directly, otherwise `say`. With neither, it is rejected. A refresh button with only `data` is reserved for Kyeot sensor bundles; you do not use it.

```json
{ "type": "refresh", "routine": "a1b2c3d4e5f6", "data": "prices", "say": "Recompute the prices tab numbers" }
```

Placing `refresh` right below a component that shows empty data or errors (`callout`, a list that may be empty) lets the user retry on the spot.

## Links and screen placement

Adding `link` to a component opens another deck screen when tapped. Most components with a title or name accept it (`text`, `button`, `divider`, `link` do not), and list items (`list`, `rank`, `stats`, `hbars`, `funnel`, `keyvalue`, `status`, `timeline`, `checklist`, `badges`, `connections`) accept a `link` per item.

| `link.to` | Opens |
|---|---|
| `costs` | Costs. `period` sets `day`, `week`, or `month` |
| `routines`, `rooms` | Routines, rooms |
| `approvals` | Approval requests |
| A screen id | Another screen of this agent |

The built-in screens (`costs`, `routines`, `rooms`, `approvals`) open from links even when they are not in the tab bar. Do not build and upload them yourself.

A screen sits in one of three places: the tab bar, outside the tabs (the chip row above Home), or hidden (shown nowhere, opened only by links). Choose with `?place=` when uploading.

```bash
curl -s -X POST "$DECK/ui/put?place=hidden" -H 'Content-Type: application/json' --data @/tmp/cost-detail.json
```

Make a detail screen reached from a summary screen with `place=hidden`, and add `"link": {"to": "cost-detail"}` to a component on the summary screen. If the user wants the detail screen in the tab bar too, upload it again with `place=tab` (same content is fine). If the tabs are full, it goes outside the tabs and `reason` says so; pass that to the user. Re-uploading without `place` keeps the current place.

```json
{ "type": "bars", "title": "Costs, last 7 days", "items": { "$data": "deck.costs.daily" },
  "link": { "to": "costs", "period": "week" } }
```

If you delete a screen a link points to, that link stops opening anything. Before deleting a screen, fix the `link`s on other screens.

## Paths

A `$data` value is a dot-separated path. The first segment is the bundle name; the rest are keys inside that object. Pick array elements with numeric segments.

| Path | Value in the bundle above |
|---|---|
| `revenue.month.total` | `1200000` |
| `revenue.daily` | Bar list array (an `items` slot) |
| `revenue.bySource.0.value` | `800000` |

Point number slots (`number`) at numbers and list slots (`items`) at arrays. Each item's shape follows the catalog `fields`. A wrong path is not rejected; the app draws "—" or a "no data yet" message. If a spec already points to that bundle, the deck lists mismatched paths in the `reason` of the `data/put` reply. After uploading, compare the `data` in `GET $DECK/ui` with the spec's paths once.

## Deck values

Paths starting with `deck.` are values the deck already knows. The app computes them, so you do not upload them. The list is in the catalog's `deckData`; check it every time.

| Path | Slot | Meaning |
|---|---|---|
| `deck.costs.today` | number | Your cost today (USD) |
| `deck.costs.week` | number | Cost over the last 7 days |
| `deck.costs.month` | number | Cost this month |
| `deck.costs.daily` | items | Cost per day over the last 7 days |
| `deck.approvals.count` | number | Requests waiting for the user's approval |
| `deck.tasks.today` | number | Conversations started today |
| `deck.routines.upcoming` | items | Up to 5 routines due next |

When the user asks to see what the deck already knows, such as costs or routines, use `deck.` paths instead of making a bundle.

## Daily refresh

When the user says "refresh it every morning" (「매일 아침 갱신해 줘」), create a routine with the `cronjob` tool. The routine only computes the numbers and calls `/data/put`. The spec uploaded the first time stays as is.

```
cronjob(action="create",
        name="revenue-tab-refresh",
        schedule="0 8 * * *",
        deliver="local",
        prompt="Read the deck-ui skill. Compute this month's revenue total and last month's total for the same period, daily revenue for the last 7 days, and revenue by source. Write the result to /tmp/revenue-data.json in the shape {\"name\": \"revenue\", \"data\": {\"month\": {\"total\", \"prev\"}, \"daily\": [{\"label\": \"11/09\", \"value\"}], \"bySource\": [{\"label\", \"value\"}]}} and upload it with curl -s -X POST http://127.0.0.1:7701/data/put -H 'Content-Type: application/json' --data @/tmp/revenue-data.json. If the reply is not ok, leave the reason in one line. Do not touch the spec.")
```

Set the time the user asked for. If no notification is needed, use `deliver="local"`. Before creating it, confirm the schedule and what it computes with the user in one sentence. After creating it, run it once right away and check that the bundle gets uploaded.

For a script routine (`script`, `no_agent`), set the Python interpreter explicitly. The `cronjob` tool has no `interpreter`; if empty, it runs with the Hermes default (a venv path that does not exist) and fails with `dependency environment interpreter is missing`. Set it in the terminal right after creating it. Do not edit `jobs.json` by hand.

```bash
hermes cron edit <job_id> --interpreter /opt/homebrew/bin/python3
```

## Routine descriptions

The Kyeot app's routines screen describes each routine with a one-sentence "what it does" and "details" (Markdown) shown when expanded. For the user to trust a routine, its policy must be visible there. Write it this way when creating or editing a routine, in the user's language.

| Routine kind | Where | Content |
|---|---|---|
| Script routine (`script`, `no_agent`) | The docstring at the top of the script | First line: what it does in one sentence. After a blank line, headings and lists such as `## Targets`, `## Conditions` (priority or criteria), `## Notifications` (when it sends and when it stays quiet), `## Does not`, `## Stops` |
| Prompt routine | The first sentence of the prompt | What it does in one sentence. The app shows only the first sentence and never sends the full prompt |

Do not keep policy values such as numbers, dates, and targets only in code constants. When changing them, update the constants and the docstring together. Never put passwords, tokens, or account numbers in the docstring; the app shows it as is.

## Routine output

Whatever a script routine prints to standard output becomes a conversation in the Kyeot app and also sends a phone notification. A routine that only refreshes screen numbers prints nothing on success (`curl -s … > /dev/null`). It prints a one-line reason only on failure. The bridge quietly skips runs that leave only an `{"ok":true,…}` reply or `[SILENT]`, but printing nothing in the first place is right.

## Routine cost

The Kyeot app's costs tab counts only `state.db` sessions titled `<routine name> · <time>` toward that routine. Prompt routines get this from hermes automatically. If a script routine calls an LLM inside, the cost leaks into "direct runs" and does not show on the routine's line. So when a script routine calls an LLM:

| Rule | Details |
|---|---|
| How to call | Only through `hermes chat -Q --oneshot`. Calling an API such as Anthropic directly from the script leaves no record in `state.db`, so the cost never shows in the costs tab |
| Title | Right after the call, find `session_id:` in the output (both stdout and stderr) and rename it with `hermes sessions rename <id> "<routine name> · <time>"`. `<routine name>` must match `name` in `jobs.json` character for character (including `-`) |
| Not allowed | `--source cron` is rejected. `-c <title> --create-if-missing` sets the title but changes the source to `local`. Use neither |

```python
JOB_NAME = "event-conference-alerts"  # same as name in jobs.json

def tag_session(output):
    sid = re.findall(r"^(?:session_id:|Session:)\s*(\S+)", output, re.M)
    if sid:
        subprocess.run([HERMES, "sessions", "rename", sid[-1], f"{JOB_NAME} · {datetime.now():%b %d %H:%M}"],
                       capture_output=True, timeout=60)

out = subprocess.run([HERMES, "chat", "-Q", "--oneshot", "--query-file", path], capture_output=True, text=True)
tag_session(out.stdout + "\n" + out.stderr)
```

When renaming the routine, change `JOB_NAME` too. After running it once, check that the title stuck with `sqlite3 "file:$HOME/.hermes/state.db?mode=ro" "select source, title from sessions order by started_at desc limit 3"`.

## Composite parts

A named template that bundles several basic components. The deck expands it before sending a screen to the app, so the app receives only basic components. The catalog's `parts` section and the `use` component are the source of truth for the rules.

| Guidance | Details |
|---|---|
| When to extract | Only when the same shape repeats on two or more tabs. If only one tab uses it, keep basic components |
| Parameters | Extract only values that change per tab into `params`. Keep fixed text and colors inside the part |
| When edited | Every screen that uses the part changes. Tell the user which tabs change first |

```json
{"version": 1, "id": "money-card", "title": "Money card",
 "params": {
   "label": {"kind": "text", "required": true, "sample": "App revenue"},
   "value": {"kind": "number", "required": true, "sample": 1200000},
   "daily": {"kind": "items"},
   "ask":   {"kind": "text", "default": "Tell me more about this month's revenue"}},
 "blocks": [
   {"type": "stat", "label": {"$param": "label"}, "value": {"$param": "value"}, "unit": "KRW"},
   {"type": "bars", "items": {"$param": "daily"}},
   {"type": "button", "label": "Details", "say": {"$param": "ask"}}]}
```

A screen calls it with one block: `{"type": "use", "part": "money-card", "with": {"label": "App revenue", "value": {"$data": "revenue.month.total"}, "daily": {"$data": "revenue.daily"}}}`. `kind` must match the kind of the property it fills. Declared but unused parameters, `use` inside a part, unknown names in `with`, and missing `required` ones are rejected. `sample` is the preview value the user sees in the app. Upload the part first, then the screen. Current parts are in `parts` of `GET $DECK/ui`.

## Going back to an earlier version

When the user asks to undo a change or go back ("previous version", "two versions back", "the one from this morning"), use the version paths. Never rebuild the old spec from memory or from earlier messages — a new conversation does not have them, and a rebuilt spec drifts from what was saved.

1. List the versions. `current: true` is what the user sees now. "Previous" is the next entry, "two back" the one after that. Match a time with `createdAt` (milliseconds) or a description with `summary`.
   ```bash
   curl -s $DECK/ui/screens/habits/versions
   ```
2. Revert to that version number.
   ```bash
   curl -s -X POST $DECK/ui/screens/habits/revert -H 'Content-Type: application/json' -d '{"version":3}'
   ```
3. The reply `reason` says which new version it became. Reverting is itself a version, so it can be undone the same way. Only the latest 20 versions can be restored. If the one asked for is older, say so. A removed screen keeps its versions, so the same steps bring it back.

Bundles have no versions. Reverting a screen does not change its data.

## Rules

| Rule | Details |
|---|---|
| `id` | Lowercase letters, digits, `-` only. Up to 32 characters, starting with a digit or lowercase letter. To edit, upload with the same id |
| `version` | Always `1` |
| `title` | 1–20 characters, in the user's language |
| `icon` | Pick an icon that fits the content from the catalog `iconHints` (icon → meaning). Optional — if empty or not in `icons`, the server picks one from the title and components, saves it, and says so in `reason` |
| `blocks` | 1–60. Components or properties not in the catalog are rejected |
| Tabs | Home is always the first slot and separate. You control up to 4 slots |
| Screen count | Up to 20 per agent. One spec up to 64KB |
| Composite parts | Up to 20 per agent, 1–12 parameters, 1–20 blocks. The expanded screen still cannot exceed 60 blocks or 64KB |
| Numbers | Put changing numbers in bundles and point to them with `$data`. Write a number directly in the spec only if it is shown once |
| Bundle name | Lowercase letters, digits, `-`, up to 32 characters. `deck` is not allowed. Usually the same as the screen id |
| Bundle size | The value is one JSON object, up to 256KB. Up to 20 per agent |
| Bundle updates | Always send the whole bundle. There is no partial update |
| Deleting | Do not delete (`remove`) or overwrite existing screens or bundles before the user confirms. The same goes for templates (`rooms`, `routines`, `costs`). A routine refreshing its own bundle is not overwriting |
| Credentials | Even when pulling data from external services, never put API keys, tokens, account numbers, or similar values in specs or bundles. Both go to the app screen as is and are stored by the deck. Do not write them in routine prompts either |
| Colors and fonts | Do not set them. The app theme handles them |
