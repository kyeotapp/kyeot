# Tabs built through conversation

**English** · [한국어](agent-ui.ko.md)

In the kyeot app, users create new tabs just by talking to an agent. They fill each tab with the parts they want and connect data to it. For example, a user who earns money from apps and ads can say "Make a tab that shows App Store sales and ad revenue at a glance." The agent then designs a Revenue tab and fills in the numbers every day. What the tab shows is not fixed. It can show this month's total in large type, bars per source, or both. The user decides the combination through conversation.

The app does not know what the agent does. The app only knows the list of parts and how to draw them. What revenue means and where it comes from is the agent's job. This is kyeot's principle "the app only provides generic parts for agents to use", applied to screens.

## Who does what

| Component | Responsibility | Does not know |
|---|---|---|
| App | The part list (catalog) and the code that draws each part. It receives a screen spec and draws the screen. It also has the screen where the user picks which tabs to show | What the data means or where it comes from |
| Deck server | Stores screen specs and data per user, keeps versions, and validates the format. Exposes the catalog to agents | The kind of agent |
| Agent | Finds out through conversation what screen the user wants, reads the catalog, writes the screen spec, and fetches data from external services | The app's internal code |
| Per-agent adapter | A thin layer that makes it easy for that agent to read the catalog and upload screen specs. For Hermes, it is one skill | Nothing. It is optional |

Users register their own server and agents, so the core of the product is the app's modular parts and the catalog. An adapter may or may not exist for a given agent. With the catalog alone, any agent can read the manual and write a screen spec.

## Parts

Each part is registered in the app code. Registration includes a name, the shape of the values it accepts, and a short description, and these three become the catalog as is. The drawing code and the catalog come from one place, so they do not drift apart.

The parts in stage 1 are listed below. They cover most screens made of numbers and lists, such as revenue, to-dos, habits, and workout logs.

| Part | Use | Accepts |
|---|---|---|
| `heading` | Section title | Text |
| `text` | Description text, a subset of Markdown | Text |
| `stat` | One large number with a change against a comparison | Label, value, unit, comparison value, better direction |
| `stats` | Two or three small numbers in one row. With `style: "tiles"`, each cell is a tile with a large number and a small unit | List of `stat` (each cell can set `decimals`) |
| `bars` | Vertical bars (trend by date) | List of labels and values |
| `hbars` | Horizontal bars (comparison by source) | List of names and values |
| `line` | Line chart | List of labels and values, multiple lines |
| `area` | Area (line chart with the area below filled) | Same as `line` |
| `stackbars` | Stacked bars (total and breakdown per column) | Labels, series (name and list of values) |
| `groupbars` | Grouped bars (series side by side per column) | Same as `stackbars` |
| `spark` | A number with a small trend line | Label, value, unit, list of trend values |
| `donut` | Each item's share of the whole | List of names and values |
| `gauge` | Current value within a range (semicircle gauge) | Label, value, minimum, maximum |
| `heatmap` | Magnitude of cells on two axes, such as weekday × hour | Column names, rows (name and list of values) |
| `calendar` | Values per date as calendar cell colors (contribution grid). Tapping or sliding over a cell shows that day's log | List of dates (`YYYY-MM-DD`), values, and optional `entries` `[{title, time, note}]`. `days` sets the period |
| `scatter` | Relation between two values | List of x, y, and names |
| `funnel` | Counts that shrink step by step, with conversion rates | List of step names and values |
| `treemap` | Shares of a whole as areas | List of names and values |
| `rank` | Top N items, largest first | List of names, values, and tags |
| `progress` | Progress toward a goal | Label, current value, goal value |
| `compare` | Two values side by side, with the difference | Two labels and two values |
| `countdown` | Time left until a set moment | Label, time (ISO 8601) |
| `list` | List of rows | Per row: title, secondary text, right-side value, tag |
| `table` | Table | Column names and rows |
| `keyvalue` | Name and value pairs | List of names and values |
| `checklist` | Done and remaining items. If an item has `say`, the user can tap to check it | Title, whether done, message sent on check (`say`), message sent on uncheck (`undo`) |
| `timeline` | Events in time order | Time, title, secondary text |
| `status` | OK, warning, or outage light per item | Name, status, note |
| `connections` | External services connected to the agent, their state, and one button per row | Name, status (connected, expired, error, off), account, button (message to send or https URL) |
| `badges` | Several short tags | Text and color |
| `callout` | Info, warning, error, or success box | Kind, title, text |
| `image` | One image | https URL, description, aspect ratio |
| `link` | A card that opens in the browser when tapped | Title, URL |
| `search` | Search box. Filters list parts on the same screen (`list`, `table`, `rank`, `badges`, `checklist`, `timeline`, `keyvalue`, `status`, `connections`) inside the app by the text the user types | Placeholder text, the `title` of the list to filter, field names to search |
| `button` | Sends a fixed message to the agent when tapped | Label, message to send |
| `divider` | Divider line | None |

Charts are drawn with the app's basic views only, without SVG. No native dependency was added, so the phone's development build does not need to be rebuilt.

Anything visible inside a part can be changed by the screen spec. Color is set with `color`. Theme-aware names (`accent`, `ok`, `warn`, `danger`, `neutral`, `blue`, `purple`, `teal`, `coral`, `green`, `yellow`) stay readable in both light and dark mode, and `#RRGGBB` is also accepted when needed. Elements that can be turned on and off, such as legends, axis ticks, percentages, and comparison text, are booleans starting with `show…`. Number precision is `decimals`, and ordering is `sort`. Inside list fields, each item can use `color`, `tags` next to the name, and `accent` in rank lists. With no options, a part is drawn as it looks today. The catalog is the source of truth for which options each part has.

When a screen spec is uploaded, the server matches names exactly, including case, and rejects unknown names and `null` values. Numbers must be in a range that fits their meaning. `decimals` is an integer 0–10, `limit` 1–100, `highlight` 0–100, and `ratio` is 0.25–4. Other numbers are limited to an absolute value of 10¹⁵. The `url` of `image` and `link` accepts only `https://` addresses. Elements of list fields must be objects with only the names in the catalog's `fields`. Fields without `fields` (`labels`, `columns`, the table's `rows`) are text, numbers, or arrays of them.

Adding `link` to a part opens another deck screen when tapped. It points to a built-in screen (`costs`, `routines`, `rooms`, `approvals`) or a screen id of the same agent, as in `{"to": "costs", "period": "week"}`, and a small chevron appears at the end of the title. When each row must go somewhere different, such as list rows or rank rows, put `link` inside the row. The opening rules live in one place in the app (`app/src/ui/link.tsx`). If the target is in the tab bar, the app switches to that tab. If it is outside the tab bar or hidden, the app opens it as a screen. A built-in screen that does not exist yet opens briefly as a template, and "Add to tab bar" adds it. A missing agent screen shows "Screen not found" with a button that asks the agent to make it again.

A screen is placed in one of three spots: the tab bar, outside the tab bar, or hidden. The agent sets the spot with `place` when it uploads a screen. A detail screen reached only through a link from a summary screen is set to `hidden` to save tab slots. When the user tries to delete a screen that other screens link to, the confirmation dialog says which screens link to it.

The app's existing Rooms, Routines, and Costs screens are also in the catalog as large parts (`rooms`, `routines`, `costs`). These three use data the deck already has and have their own behavior, such as moving between periods. So each is one whole part instead of being split into small parts. This way, built-in tabs and tabs made by agents are drawn the same way.

When the same layout repeats across several tabs, the agent can extract it into a composite part. A composite part is a named template that groups several basic parts. Upload it with `ui.part.put` as `{version, id, title, params, blocks}`, and put `{"$param": name}` in value slots. A screen calls it with one cell: `{"type": "use", "part": id, "with": {...}}`. When the server sends a screen to the app, it expands `use` into the part's blocks. The app never sees `use`, and older apps draw it as before. The agent's `ui.list` returns the original as is.

A parameter's `kind` must match the kind of the property at its slot. Declared but unused parameters and `use` inside a part are rejected. In `with`, `required` values must be given. A missing optional value takes its `default`, and if there is none, the property is dropped. Values can also be `{"$data": ...}`. Uploading a new version of a part changes every screen that uses it, and the upload is rejected if any of those screens fails to expand with the new version. A part that screens use cannot be deleted. Limits are in the catalog's `parts` section: 20 per agent, 1–12 parameters, 1–20 blocks, and the expanded screen must also stay within the basic limits (60 blocks, 64 KB).

Agents get three guidelines. Extract a part only when the same layout repeats in two or more tabs. Put only values that differ per tab in parameters. Tell the user before changing a part, because every screen that uses it changes. In "Composite parts" at the bottom of Edit tabs, the user sees which tabs use each part. The detail screen shows a preview expanded with `sample` (`GET /api/agents/{id}/ui/parts/{pid}/preview`) and the change history. Only parts that no tab uses can be deleted there.

Parts follow the app's theme. Agents do not set colors or font sizes. This way, every tab looks like part of the app no matter who made it, and light and dark mode and Korean and English fit automatically.

## Screen spec

A screen spec is JSON that describes one tab. It is the only thing the agent makes.

```json
{
  "version": 1,
  "id": "revenue",
  "title": "Revenue",
  "icon": "chart.line.uptrend.xyaxis",
  "blocks": [
    { "type": "stat", "label": "This month", "value": { "$data": "revenue.month.total" }, "unit": "KRW",
      "compare": { "$data": "revenue.month.prev" }, "better": "up" },
    { "type": "bars", "title": "Daily", "items": { "$data": "revenue.daily" } },
    { "type": "hbars", "title": "Where it came from", "items": { "$data": "revenue.bySource" } },
    { "type": "button", "label": "Recalculate now", "say": "Recalculate the Revenue tab numbers now" }
  ]
}
```

A value slot can hold a number directly, or point to one field of a data bundle with `{ "$data": "…" }`. Keeping layout and numbers apart means the agent does not resend the screen spec when only the numbers change each morning.

## Data

Data comes from three places.

The first is data bundles that the agent puts in. A bundle is one named JSON value (`revenue`), and the agent replaces it whole with results it fetched from external services and computed. All data that needs external credentials, such as App Store sales, ad revenue, or bank balances, comes this way. So the app and the deck server hold no credentials for external services. The agent's routines handle refreshes. When the user says "Refresh it every morning at 8," the agent creates a routine.

The second is data the deck already knows. Values on the deck server, such as costs, routines, and conversation counts, are referenced directly by names like `deck.costs.daily`. They are always up to date without the agent putting anything in.

The third is values written directly in the screen spec. Use them for values that rarely change, such as a target amount.

Each data bundle carries its update time. The app shows "Updated 3 hours ago" at the bottom of the tab and dims it when the data is too old.

## Tab layout

Each agent has its own tabs. When the user switches agents in the sidebar, the tab bar switches to that agent's tabs. An agent for work and an agent for personal tasks get different screens. Tabs are stored on the deck server, so picking the same agent on an iPhone or any other device shows the same tabs. So tabs do not show "who made it". An agent's tabs are made and edited by that agent.

The tab bar at the bottom shows up to five tabs. Home is always first and cannot be hidden. This prevents a badly made tab from making the app unusable. The user picks the other four from that agent's tabs. A newly registered agent starts with Rooms, Routines, and Costs templates.

The user picks them in "Edit tabs" in settings: turn tabs on and off and drag to reorder. Tabs not in the tab bar open from the chip row above Home or from "Not in tab bar" in Edit tabs. When an agent makes a new tab, it joins the tab bar only if a slot is free. If all slots are full, it goes outside the tab bar, and `ui.ack` reports this in `reason`. The agent passes the reason on to the user in conversation.

Tab icons are given as iOS system icon (SF Symbols) names. For now the app is the reference. If a web client is added later, a mapping table will turn system icon names into web icons, and a default icon will be used when there is no mapping.

The iOS system tab bar needs its tab slots declared in code ahead of time. So four slots for agent tabs are created ahead of time, and only the used ones are shown. Hiding or showing a slot redraws the tab bar and resets the state of the current screen (behavior documented in the expo-router type descriptions). This is accepted because tab layout does not change often.

## Templates

So users can add tabs without asking an agent, the app ships a few template tabs. A template is one screen spec. When the user picks a template in "Add tab" in Edit tabs, its screen spec is copied into the current agent's tabs. After copying, it is a normal tab, and the user can ask the agent to change it, for example "Add a progress bar for this month's budget to the Costs tab."

<!-- gen-template-docs:start (generated by scripts/gen-template-docs.mjs; do not edit by hand) -->
| Template | Contents | Data (bundle shape) |
|---|---|---|
| Rooms | Your rooms | Deck data. No bundle |
| Routines | This agent's routines | Deck data. No bundle |
| Costs | Agent costs | Deck data. No bundle |
| Today at a glance | Costs, approvals, chats, and routines, filled in right away | Deck data. No bundle |
| My computer | Network, disk, CPU, memory and battery on one screen. Kyeot measures it itself | The kyeot sensors upload `netwatch`, `disk`, `system`. The agent is not involved |
| Revenue | Empty frame. Ask your agent to fill it | `revenue`: `month{total,prev}`, `daily[{label,value}]`, `bySource[{label,value}]` |
| Habits | Streak, progress per goal, today's check-ins, last 14 days | `habits`: `streak`, `goals[{value,goal}]`, `today[{title,value}]`, `days[{date,value,entries[{title,time,note}]}]` |
| To-dos | Open to-dos and what's due today | `todos`: `open`, `dueToday`, `items[{title,subtitle,value}]` |
| Workout | This week's sessions and time, day by day | `workout`: `week{count,minutes}`, `daily[{label,value}]`, `recent[{title,subtitle,value}]` |
| Assets | Total, by asset, and by month. Numbers only | `assets`: `total`, `prev`, `byKind[{label,value}]`, `months[]`, `series[{name,values[]}]` |
| Reading | A morning summary with links | `reading`: `links[{title,subtitle}]` |
| Project | Milestone progress and this week's tasks | `project`: `milestones[{value,goal}]`, `week[[…]]` |
<!-- gen-template-docs:end -->

Rooms, Routines, Costs, and Today at a glance use only deck data, so numbers appear as soon as they are picked. My computer needs no setup either, because kyeot's sensors upload its three bundles. Network is measured every 20 seconds and `netwatch` is uploaded every 5 minutes (immediately when the status changes). `disk` is uploaded every 10 minutes. CPU, memory, swap, and battery are uploaded every 30 seconds as `system`. The `status` of `system` is warn for CPU at 85% or more and memory at 90% or more, warn for battery at 20% or less, danger at 10% or less, and off when there is no battery. The server measures each bundle separately, and only the app template combines them into one screen. Turn sensors off with `KYEOT_SENSORS=off`. The rest are contract templates. They are empty frames with only a layout, and the "Ask the agent to fill this in" button in the tab sends a request that includes the bundle name and example shape from the table. When the agent uploads a bundle, the server checks it against the `$data` paths of the screen specs that use it. If a path is missing, a number slot holds a non-number, or a list field has a key not in the catalog's `fields`, the server still saves the bundle and reports it in `reason` of `ui.ack`. The table is generated from the app's `app/src/ui/templates.ts` by `node scripts/gen-template-docs.mjs`.

## How agents learn and upload

The deck server exposes the catalog in two ways. It puts the catalog version in the first message sent when an agent connects, and it serves the whole catalog at `GET /ui/catalog`. The catalog contains the part list, screen spec examples, and the rules to follow (size limits, tab count limits).

The agent uploads screen specs and data bundles with the protocol messages `ui.put` and `data.put`. The deck server validates each screen spec against the catalog. It rejects unknown parts and oversized screen specs with a reason, and the agent reads the reason, fixes the spec, and uploads it again.

For agents that do not speak the deck protocol directly, such as Hermes, the bridge does it for them. The bridge opens a small address reachable only inside the Mac. The agent sends screen specs there, and the bridge forwards them to the deck server. Hermes gets one skill (`deck-ui`) that describes this address and how to read the catalog. The skill is embedded in the executable. When the bridge opens the screen address and connects, it installs the skill into the Hermes folder. If the installed version is older, it keeps the old file as `SKILL.md.bak-<time>` and replaces it. Even without the skill, an agent that knows the catalog address can do the same.

## Safety

Agents send only screen specs. The app draws only parts in the catalog, so an agent has no way to run code inside the app. The deck server stores screen specs and data per owner, and an agent can change only its own owner's tabs. Screen spec size and data bundle size are limited.

Every change to a screen spec keeps a version. When the user says "Change it back to how it looked yesterday," the agent uploads the previous version again. The user can also pick a previous version in Edit tabs in settings.

## Custom parts

When the basic parts are not enough, there are two ways to extend them.

First, composite parts. A composite part is a named template that groups several basic parts. For example, a "revenue card" groups a large number, a bar chart, and a button. Once the agent makes a template, several tabs can reuse the same layout. It is still a combination of basic parts, so no code runs and it is safe.

Second, sandboxed parts. A part written by the agent in HTML and JavaScript is drawn in an isolated web view. The web view cannot touch the app's tokens or other screens, and it receives only data bundles through a fixed channel. It is an escape hatch for screens that basic parts cannot express (maps, calendar grids, unusual charts). It has the most freedom, but the part must match the theme and language itself, so the catalog states those rules.

## Stages

| Stage | Done when | Touches |
|---|---|---|
| 1 | Rooms, Routines, and Costs are drawn from template screen specs, tabs change per agent, and Edit tabs lets the user pick visible tabs and templates. When the user says "Make a Revenue tab," the agent builds it with basic parts and numbers written directly | App (part registration, drawing, tab slots, Edit tabs, templates), server (storage, validation, versions, catalog), bridge (address inside the Mac), skill |
| 2 | "Refresh it every morning" works, specs reference deck data directly, and tapping a button sends a message to the agent | Server (data bundles), app (`$data` resolution, update time) |
| 3 | Composite parts, then sandboxed parts | App, server, catalog |

The first test screen for stage 1 is a real Revenue tab. Where the revenue data comes from (App Store Connect, an ad dashboard, manual entry) is decided in conversation with the agent. In stage 1, the user can also give the numbers directly in conversation.

## Later additions

Add a tab store: a place where users publish tab screens they made (screen spec and sample data) to share with others, and search tabs others published to add them to their own agent's tabs. If today's template picker is a list built into the app, the store is that list filled by people. A screen spec is already JSON with only part names and values, and the server validates it against the catalog, so drawing a screen spec made by someone else is not dangerous. Data bundles are not shared. A received tab arrives as an empty frame, and the user's own agent fills in the numbers. Still to decide: how to show the publisher, how to handle reports, and how to notify recipients when a screen spec changes version. (Recorded 2026-11-12)

## Open questions

Should sandboxed parts (stage 3) be allowed to access the internet themselves? A sandboxed part is a small web page the agent makes in HTML. Blocking access means it can draw only the data bundles the agent provides, so data does not leak out. But then parts that must keep fetching images from external servers, such as maps, cannot be built. The current plan is to block by default, have each part declare the addresses it will access, and open only addresses the user has approved once.
