# netwatch

[English](README.md) · **한국어**

이 컴퓨터의 네트워크를 20초마다 재서 기록하고, 5분마다 곁 네트워크 탭의 `netwatch` 묶음으로 올린다. 공유기(기본 게이트웨이)와 외부(1.1.1.1) ping, DNS 응답을 재고, 상태가 비정상으로 바뀌면 그 순간의 라우팅표, arp, 열린 소켓, 전원 상태를 스냅샷으로 남긴다.

## 설치

맥:

```sh
install -m 755 netwatch.sh ~/.local/bin/netwatch.sh && install -m 644 netwatch_deck.py ~/.hermes/scripts/netwatch_deck.py
sed "s#__HOME__#$HOME#" local.netwatch.plist > ~/Library/LaunchAgents/local.netwatch.plist # public-ok
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/local.netwatch.plist # public-ok
```

리눅스:

```sh
install -m 755 netwatch.sh ~/.local/bin/netwatch.sh && install -m 644 netwatch_deck.py ~/.hermes/scripts/netwatch_deck.py
install -m 644 netwatch.service netwatch.timer ~/.config/systemd/user/
systemctl --user enable --now netwatch.timer
```

그다음 Hermes 루틴으로 `netwatch_deck.py` 를 5분마다 돌린다. 만드는 법과 주의점은 `deck-ui` 스킬의 「예시: 네트워크 탭」에 있다.

## 확인

```sh
tail -f ~/Library/Logs/netwatch/$(date +%F).tsv     # 리눅스는 ~/.local/state/netwatch
python3 ~/.hermes/scripts/netwatch_deck.py; echo $?  # 출력 없이 0 이면 묶음이 올라갔다
```

기록은 30일 지나면 지운다. 공유기 관리 화면까지 스냅샷에 담으려면 `~/.config/netwatch/router-snapshot.sh` 를 실행 파일로 둔다(사용자가 관리 주소와 키를 줄 때만).
