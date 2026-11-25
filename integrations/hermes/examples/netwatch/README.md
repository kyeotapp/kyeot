# netwatch

**English** · [한국어](README.ko.md)

Measures this computer's network every 20 seconds, logs it, and uploads it every 5 minutes as the `netwatch` bundle for the kyeot Network tab. It pings the router (default gateway) and an outside host (1.1.1.1) and measures DNS response. When the status turns abnormal, it snapshots the routing table, arp, open sockets, and power state at that moment.

## Install

macOS:

```sh
install -m 755 netwatch.sh ~/.local/bin/netwatch.sh && install -m 644 netwatch_deck.py ~/.hermes/scripts/netwatch_deck.py
sed "s#__HOME__#$HOME#" local.netwatch.plist > ~/Library/LaunchAgents/local.netwatch.plist # public-ok
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/local.netwatch.plist # public-ok
```

Linux:

```sh
install -m 755 netwatch.sh ~/.local/bin/netwatch.sh && install -m 644 netwatch_deck.py ~/.hermes/scripts/netwatch_deck.py
install -m 644 netwatch.service netwatch.timer ~/.config/systemd/user/
systemctl --user enable --now netwatch.timer
```

Then run `netwatch_deck.py` every 5 minutes as a Hermes routine. How to set it up and what to watch for are in "Example: Network tab" in the `deck-ui` skill.

## Check

```sh
tail -f ~/Library/Logs/netwatch/$(date +%F).tsv     # Linux: ~/.local/state/netwatch
python3 ~/.hermes/scripts/netwatch_deck.py; echo $?  # 0 with no output means the bundle was uploaded
```

Logs are deleted after 30 days. To include the router admin page in snapshots, put an executable at `~/.config/netwatch/router-snapshot.sh` (only when the user provides the admin address and key).
