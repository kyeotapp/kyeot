# Self-hosted server

**English** · [한국어](self-host.ko.md)

The kyeot server is a single executable that users run on their own computer. The phone app connects to this server, and the server connects the agents on that computer. This document explains why the server lives on the user's computer, which defaults it runs with, and what it does on first run.

## Why this design

Agents handle personal data such as conversations, schedules, costs, and files. When the server runs on the user's computer, those records and the agent keys (language model API keys, messenger bot tokens) never pass through the developer's hands. If the developer's servers are breached, user data is not there, and the developer does not pay for servers.

However, when a language model writes a reply, the conversation goes to the model company the user chose. So we do not say "everything stays only on my computer." We say "records and keys stay on my computer, and go to the model company I chose only when a reply is generated."

"It is safe because it is my server" comes with conditions, because security responsibility moves to the user. Opening a port to put the server on the internet, skipping updates, or having no backups makes it more dangerous, not less. The challenge of this design is for the installer and the app to take care of those conditions for the user.

## Updates and a kyeot that differs per person

Each person's kyeot grows differently. Those differences accumulate in data and agents, not in server code. That way, when the developer ships a new server, it does not collide with the kyeot each person has built up.

| What differs per person | Where it lives | When the server is updated |
|---|---|---|
| Tabs, screen designs, hiding, locking, links | Screen designs (JSON) in the server DB | Kept as is |
| Numbers shown on screens | Data bundles in the server DB | Kept as is |
| Conversation history | Server DB | Kept as is |
| What the agent can do, routines | Agent side (Hermes skills and settings) | Unrelated to the kyeot server |
| Available component types | Catalog in the app and server (code) | The developer adds more |

To keep this, the developer follows four rules.

| Rule | Description | Status |
|---|---|---|
| Data migration | When the DB structure changes, the server moves old data to the new structure on startup | Done (`migrate` in `internal/store`) |
| Reading old screen designs | A new version draws screen designs made by an old version as they were. Options can be added, but existing meanings do not change, and without an option the old look stays | Ongoing |
| Protocol version | The app and server tell each other their versions, and a new app on an old server (or the reverse) does not break. The side that is behind shows "Update the server" | Done (11-16). `GET /version` returns `protocol`, `minClientProtocol`, `features` (`internal/server/compat.go`). The app decides with `app/src/lib/compat.ts`, shows the banner, and turns on new feature buttons with `useDeck().features.has(name)`. If the server version is unknown (dev build), everything is on |
| Skipping unknown components | When an old app meets a new component, it leaves only that cell as "unknown component" and draws the rest | Done (`app/src/ui/Screen.tsx`) |

Requests for components that are not in the catalog do not lead to server code changes. They go through the "Custom parts" section of `docs/agent-ui.md` (small components made by an agent, stored as data and drawn in an isolated cell). Requests to connect a new agent or a new data source go through the adapter slot. Forks that change server code directly are outside official support, and merging updates is up to whoever made the fork.

When an updated kyeot starts for the first time and the DB structure must change, it first copies the DB as it was before the change into the data folder as `kyeot.db.bak-<old schema number>-<time>`. The server log gets one line with that path, and only the three most recent copies are kept. The attachments folder is not copied, because updates do not touch it. Conversely, when an older kyeot meets a DB made by a newer version, it logs that the data was made by a newer kyeot and exits without touching the DB.

To update, run `kyeot update`. It reads the latest release, checks the signature of `checksums.txt` against the keys built into kyeot and the file's sha256 (the same checks as the install script), keeps the running version as `kyeot.prev` next to it, replaces the file, and restarts the registered server and connect services. `kyeot update --check` only reports whether a newer version exists. Running the install command again also updates. `kyeot update` exists from 0.1.6; on 0.1.5 or earlier, run the install command again.

Servers newer than 0.1.6 tell you when a new version is out. Once right after start and then every 24 hours, the server reads `https://github.com/kyeotapp/kyeot/releases/latest/download/VERSION` and adds it to `GET /version` as `latest`. The request carries only standard HTTP headers and `User-Agent: kyeot/<version>`; no account, token, server id, or data. It honors `HTTPS_PROXY`. GitHub sees your server's IP address, as with any download. A failed check only writes one line to the server log. The app then shows the server's version in Settings › Server, and the owner sees "New version available" with the `kyeot update` command to copy (plus a small dot on the Settings button on the home screen). In the app, other people on the server do not see it (`GET /version` itself is public, like `version`). To turn the check off, set `KYEOT_UPDATE_CHECK=off` (a registered service keeps it like the other settings); the app then shows nothing.

Either way, a registered service keeps the settings saved in its service file (`KYEOT_ADDR`, `KYEOT_NETS`, `KYEOT_HERMES` and the rest); a value set in the current shell replaces the saved one. To drop a saved setting, run `kyeot service uninstall`, then `kyeot service install` from a shell that has the settings you want. The install script shows the owner token QR only on first setup, not on updates. If kyeot was installed before without a service, an update does not register one: a server started in a terminal keeps running the old version until you stop it (Ctrl-C) and start it again.

Both keep the previous version as `kyeot.prev`. If something goes wrong after an update, roll back like this.

1. Stop the server without removing its service file, so the saved settings stay. On macOS `launchctl bootout gui/$(id -u)/app.kyeot.server` (if that fails, `launchctl bootout user/$(id -u)/app.kyeot.server`), on Linux `systemctl --user stop kyeot-server`. In a terminal, Ctrl-C.
2. If this update changed the DB structure (the server log has a line `DB schema … Backup before the change` with a path), delete `kyeot.db-wal` and `kyeot.db-shm` in the data folder and put that bak file in place as `kyeot.db`. If that line is not there, leave the DB alone. If you restore the bak from an earlier update, you lose the records made after it.
3. Put `~/.local/bin/kyeot.prev` in place as `~/.local/bin/kyeot` and start it again. On macOS `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/app.kyeot.server.plist` (if that fails, `user/$(id -u)` instead of `gui/$(id -u)`), on Linux `systemctl --user start kyeot-server`. These work with any version, including one without `kyeot update`.

If you start the new version instead of going back, it goes through the same update again and makes one more backup. `kyeot uninstall` also deletes `kyeot.prev`.

## Backups and moving to another computer

While running, the server copies the DB once a day to `backups/kyeot-<time>.db` in the data folder (600). On startup and every hour it checks whether the last backup is more than 24 hours old, and it keeps only the 7 most recent. Attachments (`uploads/`) are not included, because they are never changed after being written. On failure it only logs, and the server keeps running. When you delete the whole server from the app, or delete only your own data with `kyeot uninstall --purge=mine`, the daily backups, `kyeot.db.bak-…`, and pre-restore bundles are deleted too (when deleting only your own data, it makes one new daily backup for the remaining people). Files the user made separately with `kyeot backup` are not deleted. The records of someone who left in the app or handed over ownership stay in daily backups for up to 7 days.

Daily backups stay on the same disk. When the disk fails or you change computers, make a single file with `kyeot backup` and keep it somewhere else.

| Command | What it does |
|---|---|
| `kyeot backup <file>` | Packs the DB, attachments, sensor records, app update bundles, the agent id of Hermes on the same computer, the owner token, and the attachment signing key into one tar.gz (600). The server may be running. It does not overwrite an existing file |
| `kyeot restore [--yes] <file>` | Runs only while the server is stopped. It first backs up the current data to `backups/before-restore-<time>.tar.gz` (keeps the 3 most recent), then replaces it with the contents of the backup. If the backup DB has a newer schema or the bundle is broken, it changes nothing and stops |

While running, the server holds `kyeot.db.lock` next to the DB. restore makes its changes while holding that lock, so it changes nothing if a server is running on another port, and a server cannot start while it is replacing data (the server waits 15 seconds and then exits).

The backup file contains the owner token. Anyone with this file can get into the server without connecting the app, so do not give it to others. The link preview cache, `connect.json` (this computer's connection to another server), the app update signing private key, and daily backups are not included. If you provide the owner token through `KYEOT_OWNER_TOKEN`, the token is left out, and you keep providing that environment variable after restoring.

Steps to move to another computer.

1. On the old computer, run `kyeot backup ~/kyeot.backup`.
2. Move the file to the new computer (AirDrop, `scp`, and so on).
3. Install kyeot on the new computer (`curl -fsSL --proto '=https' https://kyeot.app/install | sh`). If the install started the server, stop it with `kyeot service uninstall`.
4. Run `kyeot restore ~/kyeot.backup` and answer `y`.
5. Start it again with `kyeot service install`. If the backup has an older schema, it is converted to the new structure at this point and the previous DB is kept as `kyeot.db.bak-…`.
6. Stop the server on the old computer (`kyeot service uninstall`). If two computers run with the same data, conversations split.

The server id and owner token move with the data. If the address stays the same, the phone app has nothing to do. If the address changed, change that server's address to the new one in Settings › Server in the phone app. The new address reports the same server id, so the app treats it as the same server and keeps read marks and names. Tab order, pins, and groups are in the server DB, so they move too. If you scan a QR with the new address, the app says it is the same server as one you already have and guides you to fix the address. Agents that connected from another computer with `kyeot connect` run `kyeot connect` again on that computer with the new address.

## Current defaults

If you run `kyeot` without any environment variables, it runs as below. An environment variable, when given, takes precedence.

| Item | Default | How to change |
|---|---|---|
| Listen address | `0.0.0.0:7700`. It listens everywhere because the phone must reach it | `KYEOT_ADDR` |
| Accepted networks | `loopback,tailscale,lan`. Accepts only this device, Tailscale, and private networks (same Wi-Fi); public addresses get 403 | `KYEOT_NETS`. A list of names (`loopback`, `tailscale`, `lan`) or CIDRs. `all` opens everything. If the Hermes adapter on the same computer is on (`KYEOT_HERMES` is not off), loopback is added with a warning even if missing. If you open ranges outside private, Tailscale, and loopback (`all`, public CIDRs) without https, startup is refused (exit code 1) |
| https | Off. Accepts plain http (Tailscale handles encryption) | `KYEOT_TLS_CERT`, `KYEOT_TLS_KEY` (both). When on, a separate plain entrance for the Hermes adapter on the same computer opens on a free port at `127.0.0.1` (the certificate is for a domain, so loopback https fails verification). The QR address from `kyeot pair` is `https://<first domain in the certificate>`. Get certificates from `tailscale cert`, Let's Encrypt, and so on. Running behind an https proxy on the same computer, or behind Tailscale serve or Funnel, is not supported. Every request looks like it comes from this device, so `KYEOT_NETS` cannot filter, and the pairing lock applies to everyone. When the server sees such requests, it logs a warning and also shows it in `kyeot service status` (it does not block them). For a proxy on another computer, narrow `KYEOT_NETS` to that address |
| Allow plain text | Off. A server open to the internet starts only with https, and `kyeot pair` does not put `http` in a public address | `KYEOT_ALLOW_PLAIN=1`. Prints a warning and starts in plain text |
| Data folder | `kyeot/` in the config folder (`os.UserConfigDir()`). On macOS `~/Library/Application Support/kyeot`, on Linux `~/.config/kyeot`. Folder permission 700. A `data/kyeot.db` in the working folder is not used (the server only logs that it saw it). The DB is `kyeot.db` inside it; attachments (`uploads/`), app updates (`updates/`), and the Hermes agent id (`hermes-agent`) sit next to the DB | `KYEOT_DB` |
| Attachment space per user | 20GB, counting files already attached to messages. Over the limit, upload returns 507 | `KYEOT_UPLOAD_TOTAL_MB` (MB, 0 means the default) |
| Owner token | Generated randomly on first run and stored in `owner-token` in the data folder (permission 600). If the old location `~/.config/kyeot/owner-token` exists, it reads that | `KYEOT_OWNER_TOKEN` (16 characters or more). To change it, `kyeot pair --rotate`: it writes a new token, restarts the server service, and draws a new QR. Phones connected with the old token must scan the new QR. A token given through the environment variable changes that value. When the owner hands over ownership in the app, the server replaces this file with a new value, and that token does not log in to any account. After the handover, `pair --rotate` asks once and creates a new owner account on this computer, and the person who took over becomes an admin. If the token is given through the environment variable, handing over in the app is blocked |
| Attachment signing key | `uploads-key` in the data folder (permission 600). On first start it is created from the key that used to be derived from the current owner token, so old attachment URLs keep working; after that it is independent of the token | Changing it breaks every attachment URL already shared: `kyeot pair --rotate-uploads` |
| App connection | On the run that first creates the token, it shows a connection QR if running in a terminal. When output goes to a log file, it does not write the QR. To see it again, `kyeot pair`. The server address in the QR is picked in order: Tailscale, then private network | `kyeot pair -host <address>`. A name (`x.example.com`) gets the scheme and listen port added; a full address (`https://x.example.com`, `https://x:8443`) is used as is |
| Public address | None | `KYEOT_PUBLIC_URL=https://kyeot.example.com`. When running behind a proxy or domain, the first-run QR and `kyeot pair` use this address (`-host` takes precedence). When set, the "the phone cannot reach it" warning for `KYEOT_ADDR=127.0.0.1:…` is not shown. Giving `http` for a public address refuses startup |
| Access log | Off | `KYEOT_ACCESS_LOG=1`. One line per request to the server log: time, first 8 characters of the account id, method, path (no query or body), status, duration, client IP, and a short User-Agent. Websocket open and close get a line each. Tokens are never written. Meant for a demo server you run for others; turning it on keeps visitors' IP addresses in your logs |
| Update check | On. Reads the newest version number from GitHub once a day | `KYEOT_UPDATE_CHECK=off` (`0`, `false`, `no`). See "Updates" above |
| Token in the URL | `?token=` is accepted only for WebSocket connections (`/client`, `/agent`). REST accepts only the `Authorization: Bearer` header. The phone apps (iOS, Android) send the header for WebSockets too; the URL token is used only by browsers (web version) and old apps | None |
| Conversation language | The app language is passed briefly to each Hermes run as `instructions` in `/v1/runs`. The Hermes config file is not changed | None |
| Server version | `GET /version` returns `{"version":"x.y.z"}` without authentication, plus `latest` once the update check has read it. Install scripts on other computers fetch this version. If the version is `dev`, it returns 503 | At build time, `-ldflags "-X main.version=<version>"` (`VERSION` file) |
| Auto start | `kyeot service install` registers with launchd (macOS, label `app.kyeot.server`) or systemd (Linux, `kyeot-server.service`). It uses the same code as the agent connection service (`kyeot connect`) (`cmd/kyeot/service.go`), and the owner token is not put in the service file. | `kyeot service uninstall`, `status` |
| Uninstall | `kyeot uninstall` removes only the service and executable and keeps records. `--purge` also deletes keys and records. If there are other users, it shows the number of rooms and conversations per person and lets you choose between "Delete everything" and "Delete only mine" (keeps other people's records and removes only the program). In a run that cannot ask (a script), it deletes nothing and stops. | `--purge=all`, `--purge=mine` |

## Binary releases

The executables and install script that other computers download live in GitHub Releases of this repository (`kyeotapp/kyeot`). `scripts/release-build.sh <version>` creates `kyeot_darwin_arm64`, `kyeot_darwin_amd64`, `kyeot_linux_arm64`, `kyeot_linux_amd64`, `install.sh` (a copy of `scripts/install.sh`), and `checksums.txt` (`shasum -a 256` of these five files) in `dist/<version>/`. It then signs `checksums.txt` with `ssh-keygen -Y sign -n kyeot-release` using `~/.config/kyeot/release-signing/id_ed25519` (if missing, create it with `scripts/release-key.sh`) to produce `checksums.txt.sig`. Without the key, it does not build. `install.sh` trusts the checksums only after verifying this signature with the public key embedded in the script, and does not install if the signature is missing or wrong. If you change the key, also change `RELEASE_PUBKEY` in `install.sh` and publish the new release as latest. The version must be numeric `x.y.z`. The build is `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=<version>"`, and because the DB is the pure Go `modernc.org/sqlite`, macOS cross-builds the Linux versions too. Upload every file in that folder to the release tagged `v<version>` (`gh release create v<version> dist/<version>/kyeot_* dist/<version>/install.sh dist/<version>/checksums.txt dist/<version>/VERSION dist/<version>/checksums.txt.sig -R kyeotapp/kyeot`). Users download the script of the most recent release from `releases/latest/download/install.sh`, so a release that changes the script must be published as latest. The script downloads the version reported by the server's `/version`, so build the server with the same version too. It does not download versions lower than `MIN_VERSION` at the top of the script, so raise that value if an old version has a security flaw. Check the version with `kyeot version`.

Before a release, and whenever Hermes gets a new version, run `sh scripts/check-hermes-live.sh` on a computer with Hermes. It drives approve, deny, clarify and stop through the bridge against the real Hermes, with an in-memory server.

## First server run and agent connection

Agents are not bundled; the server finds installed agents and connects them. On startup the server only looks for Hermes on this computer; it connects only after the user taps connect in the app. When the app connects to the server, it asks "Found Hermes on this computer. Connect?"

```
$ kyeot
  Created the data folder: ~/Library/Application Support/kyeot
  Found Hermes. It connects when you connect it in the app
  ? Start kyeot automatically when the Mac turns on? (Y/n)
  When an agent first opens a protected folder such as Photos or Documents, the Mac asks for permission.
  Only the Mac can answer that window. Decide whether to trust the task, and it won't ask again
  Scan this QR with the kyeot app on your phone
  (QR)
```

| Case | What the server does |
|---|---|
| Hermes is installed | On startup it only looks (it counts as found if the `HERMES_HOME` folder or `API_SERVER_KEY` exists). The app checks the status with `GET /api/local-hermes` and asks; when the user taps, `POST /api/local-hermes/connect` checks that the Hermes API is reachable and registers it as an internal agent. The agent id file (`hermes-agent` in the data folder) is the record of consent. The agent key moves only inside the server, and the adapter runs inside the server |
| Already connected before | If the agent id file exists, it connects right away on startup without asking. If the connection was removed in the app, it does not reconnect on startup; tapping connect in the app again connects it as a new agent |
| Hermes has no key | `problem` in the status is `no_key`. Tapping connect does not connect it |
| The Hermes API server is off | `problem` in the connect response is `unreachable` (`bad_key` if the key is wrong). The app says the Hermes API server must be turned on and shows the command to turn it on. It does not quietly change Hermes settings |
| No agent | Shows "You need an agent" and an install link for a recommended agent. The QR still shows, so the app can connect first |
| Hermes on another computer | Adding an agent in the app shows a 6-digit pairing code and a one-line command `curl -fsSL --proto '=https' https://kyeot.app/install \| sh -s -- '<server address>' <code>`. If that computer has no `kyeot`, it downloads the same version as the server from the releases, installs it into `~/.local/bin`, and runs `kyeot connect`. macOS and Linux only. The token is never shown to a person |
| Your own agent | Uses the same pairing code: `POST /pair/agent {code}` → gets `{token}` and connects. How to connect is in `docs/connect-your-agent.md` |
| Adding agents later | "Add agent" in the app (available now) or the command above |
| Reconnecting the phone | `kyeot pair` shows the QR again |

Config files on the agent side are not changed. The conversation language is passed as an instruction (`instructions`) given to Hermes on each run. This instruction applies only to that run and is not stored.

The macOS protected folder permission window (TCC) can only be answered in front of the Mac; kyeot cannot click it for you. So on first run, while a person is at the Mac, kyeot warns about it in advance. The user decides whether to allow it. kyeot does not narrow the search scope to avoid the window, because handling things outside the working folder is also part of the agent's job. Instead, if a tool stalls on that window, the tool row in the app shows the elapsed time and the running command after 20 seconds (`SLOW_TOOL_SECS` in `Chat.tsx`).
