# Labels

Labels this repository uses. GitHub's defaults (`bug`, `documentation`, `duplicate`, `enhancement`, `good first issue`, `help wanted`, `invalid`, `question`, `wontfix`) stay as they are; the rows below are added on top.

| Label | Color | Meaning |
|---|---|---|
| `needs-triage` | `ededed` | New issue nobody has looked at yet. Issue forms add it |
| `security` | `b60205` | Security hardening that is safe to discuss in public. Vulnerabilities go through SECURITY.md |
| `agent:hermes` | `5319e7` | Hermes bridge, profiles, the screen skill |
| `agent:custom` | `8a63d2` | A user's own agent over WebSocket, the agent protocol |
| `area:screens` | `1d76db` | Agent screens, the component catalog, data bundles |
| `area:rooms` | `0e8a16` | Rooms, people's messages, room safety |
| `area:install` | `fbca04` | Install script, service registration, backup and restore, updates |
| `breaking` | `d93f0b` | Changes what an older app or agent sees. Needs a `Features` name or a `protocol` bump |

Create them with:

```sh
grep '^| `' .github/labels.md | while IFS='|' read -r _ name color desc _; do
  name=$(echo "$name" | tr -d ' `'); color=$(echo "$color" | tr -d ' `'); desc=$(echo "$desc" | sed 's/^ *//; s/ *$//')
  gh label create "$name" --color "$color" --description "$desc" --force
done
```
