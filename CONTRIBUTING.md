# Contributing

**English** · [한국어](CONTRIBUTING.ko.md)

Thanks for helping. This guide covers how to report a problem, how to set up a development copy, and what a pull request needs before it can be merged. Issues and pull requests are welcome in English or Korean.

## Report a bug or ask for a feature

Search existing issues first, then open a new one with the **Bug report** or **Feature request** form. The bug form asks for `kyeot version`, the app version, your OS, the connected agent, steps, expected and actual results.

Issues are public. Remove tokens, pairing codes, server addresses and personal data from logs before pasting them.

| What you have | Where it goes |
|---|---|
| A security problem | Privately, as described in [SECURITY.md](SECURITY.md). Never in a public issue |
| A question about setup or use | [Discussions](https://github.com/kyeotapp/kyeot/discussions) |
| A problem with the iOS or Android app itself | [Support](https://kyeot.app/en/support/). The app is not in this repository |

## Set up a development copy

You need Go (the version in `go.mod`, currently 1.27) and git. No other service is required; tests start their own server, fake agent and fake Hermes.

```sh
git clone https://github.com/kyeotapp/kyeot.git
cd kyeot
go test ./...
```

`ok` on every package means your copy works. To run the server locally, see [docs/self-host.md](docs/self-host.md).

## Before you write code

Open an issue first for anything larger than a small fix, so we can agree on the direction before you spend time on it. Issues labeled `good first issue` are a good place to start.

## Pull requests

Keep a pull request small and about one change. Before opening it, run:

```sh
go vet ./... && go test -race ./... && sh scripts/check-public.sh
```

`scripts/check-public.sh` fails if a tracked file contains a home path, a private or Tailscale address, a non-noreply email, or a secret-looking key. A legitimate example in a test or doc can stay if its line ends with `public-ok`.

New behavior needs a test. Commit messages are in English, one line with a prefix: `fix:`, `feat:`, `refactor:`, `docs:`, `test:`, `perf:`, `chore:`, `ci:` (for example `fix: keep finished tasks from reopening on a late task.end`).

Code comments, test names and log messages are in English. A document people read is written in English and gets a Korean copy next to it (`name.ko.md`).

## Contributor License Agreement

A pull request can be merged only after you agree to the [Contributor License Agreement](CLA.md). You keep your copyright; the agreement lets the project offer the code under more than one license. Confirm it with the checkbox in the pull request template; you do this once and it covers your later contributions.

## Protocol and compatibility

Old versions keep running. Every user runs their own server version while the app in the store is the same for everyone, so a change must not break an older app talking to a newer server, or the reverse.

Within one `protocol` version, only add fields and never change what an existing field means. Bump `protocol` only for a change that older apps cannot follow. A new capability gets a new name in `Features` (`internal/server/compat.go`); the app shows it only when the server lists that name. Never remove a name from `Features`.

Do not add requirements on agents. Agents are brought by the user and the contract stays `message`, `task.end` and `approval`; interpreting, normalizing and labeling is the server's job.

Message types and required fields live in `internal/protocol`. A new message type also goes into the table in `docs/connect-your-agent.md` (a test checks it).
