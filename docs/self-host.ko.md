# 직접 올리는 서버

[English](self-host.md) · **한국어**

곁 서버는 사용자가 자기 컴퓨터에 올리는 실행 파일 하나다. 폰 앱이 이 서버에 붙고, 서버가 그 컴퓨터의 에이전트를 붙인다. 이 문서는 서버가 왜 사용자 컴퓨터에 있는지, 어떤 기본값으로 도는지, 첫 실행에서 무엇을 하는지 적는다.

## 왜 이 구조인가

에이전트는 대화와 일정, 비용, 파일 같은 개인 데이터를 다룬다. 서버가 사용자 컴퓨터에 있으면 그 기록과 에이전트의 키(언어 모델 API 키, 메신저 봇 토큰)가 제작자 손을 거치지 않는다. 제작자 서버가 털려도 사용자 데이터는 거기 없고, 제작자가 서버 비용을 낼 일도 없다.

다만 언어 모델로 답을 만드는 순간 대화 내용은 사용자가 고른 모델 회사로 나간다. 그래서 "모든 것이 내 컴퓨터에만 있다"라고 말하지 않는다. "기록과 키는 내 컴퓨터에 있고, 답을 만들 때만 내가 고른 모델 회사로 간다"라고 밝힌다.

"내 서버라 안전하다"는 조건이 붙는 말이다. 보안 책임이 사용자에게 넘어가기 때문이다. 포트를 열어 서버를 인터넷에 내놓거나, 업데이트를 하지 않거나, 백업이 없으면 오히려 위험하다. 그 조건들을 설치 도구와 앱이 대신 챙겨 주는 것이 이 구조의 과제다.

## 업데이트와 사람마다 다른 곁

곁은 사람마다 다르게 자란다. 그 차이는 서버 코드가 아니라 데이터와 에이전트 쪽에 쌓이게 한다. 그래야 제작자가 새 서버를 내도 각자 키운 곁과 부딪히지 않는다.

| 사람마다 다른 것 | 들어 있는 곳 | 서버를 업데이트하면 |
|---|---|---|
| 탭, 화면 설계, 숨김, 잠금, 링크 | 서버 DB 의 설계도(JSON) | 그대로 남는다 |
| 화면에 들어가는 숫자 | 서버 DB 의 데이터 묶음 | 그대로 남는다 |
| 대화 기록 | 서버 DB | 그대로 남는다 |
| 에이전트가 할 줄 아는 일, 루틴 | 에이전트 쪽(Hermes 의 스킬과 설정) | 곁 서버와 무관하다 |
| 쓸 수 있는 부품 종류 | 앱과 서버의 카탈로그(코드) | 제작자가 늘린다 |

이것을 지키려면 제작자 쪽에 규칙이 넷 있다.

| 규칙 | 내용 | 지금 |
|---|---|---|
| 데이터 옮기기 | DB 구조가 바뀌면 서버가 시작할 때 옛 데이터를 새 구조로 옮긴다 | 있다(`internal/store` 의 `migrate`) |
| 옛 설계도 읽기 | 옛 버전에서 만든 설계도를 새 버전이 그대로 그린다. 옵션은 늘리되 기존 뜻은 바꾸지 않고, 옵션이 없으면 예전 모양 그대로다 | 지키는 중 |
| 프로토콜 버전 | 앱과 서버가 서로 버전을 알리고, 새 앱이 옛 서버에 붙어도(또는 그 반대도) 깨지지 않는다. 모자란 쪽은 「서버를 올려 주세요」를 띄운다 | 있다(11-16). `GET /version` 이 `protocol`, `minClientProtocol`, `features` 를 내고(`internal/server/compat.go`), 앱은 `app/src/lib/compat.ts` 로 판단해 띠를 띄우며 `useDeck().features.has(이름)` 으로 새 기능 버튼을 켠다. 서버 판을 모르면(개발판) 다 켠다 |
| 모르는 부품 건너뛰기 | 옛 앱이 새 부품을 만나면 그 칸만 「모르는 부품」으로 두고 나머지를 그린다 | 있다(`app/src/ui/Screen.tsx`) |

카탈로그에 없는 부품을 쓰고 싶다는 요구는 서버 코드를 고치게 하지 않고, `docs/agent-ui.ko.md` 의 「직접 만드는 부품」(에이전트가 만든 작은 부품을 데이터로 저장해 격리된 칸에 그림)으로 받는다. 새 에이전트나 새 데이터 출처를 붙이고 싶다는 요구는 어댑터 자리로 받는다. 서버 코드를 직접 고친 갈래(포크)는 공식 지원 밖이고, 업데이트를 합치는 것은 그 갈래를 만든 사람 몫이다.

업데이트한 곁이 처음 뜰 때 DB 구조가 바뀌어야 하면, 바꾸기 전의 DB 를 데이터 폴더에 `kyeot.db.bak-<옛 구조 번호>-<시각>` 으로 먼저 복사해 둔다. 서버 로그에 그 경로가 한 줄 남고, 가장 최근 세 개만 보관한다. 첨부 폴더는 업데이트가 건드리지 않아서 복사하지 않는다. 거꾸로 이전 판 kyeot 이 더 새 판이 만든 DB 를 만나면 「이 데이터는 더 새 판 곁이 만들었다」는 로그를 남기고 DB 를 건드리지 않은 채 끝난다.

업데이트는 `kyeot update` 로 한다. 최신 릴리스를 읽고, `checksums.txt` 의 서명을 kyeot 에 박힌 키로, 파일의 sha256 을 확인한 뒤(설치 스크립트와 같은 확인), 돌던 판을 옆에 `kyeot.prev` 로 남기고 바꾸고, 등록된 서버·연결 서비스를 다시 켠다. `kyeot update --check` 는 새 판이 있는지만 알린다. 설치 명령을 다시 실행해도 업데이트된다.

어느 쪽이든 등록된 서비스는 등록 파일에 저장된 설정(`KYEOT_ADDR`, `KYEOT_NETS`, `KYEOT_HERMES` 등)을 이어 쓴다. 지금 셸에 준 값이 있으면 그 값이 저장된 값을 바꾼다. 저장된 설정을 빼려면 `kyeot service uninstall` 뒤 원하는 설정만 준 셸에서 `kyeot service install` 한다. 설치 스크립트는 소유자 토큰 QR 을 처음 설치 때만 띄우고 업데이트 때는 띄우지 않는다. 서비스 없이 깔아 쓰던 kyeot 는 업데이트해도 서비스를 새로 등록하지 않는다. 터미널에서 띄운 서버는 끄고(Ctrl-C) 다시 띄울 때까지 옛 판으로 돈다.

두 방법 모두 바로 앞 판을 `kyeot.prev` 로 남긴다. 업데이트 뒤 문제가 생기면 이렇게 되돌린다.

1. 저장된 설정이 남도록 등록 파일은 지우지 않고 서버만 멈춘다. 맥은 `launchctl bootout gui/$(id -u)/app.kyeot.server`(실패하면 `launchctl bootout user/$(id -u)/app.kyeot.server`), 리눅스는 `systemctl --user stop kyeot-server`. 터미널이면 Ctrl-C.
2. 이번 업데이트가 DB 구조를 바꿨으면(서버 로그에 `DB schema … Backup before the change` 줄과 경로가 있다) 데이터 폴더의 `kyeot.db-wal` 과 `kyeot.db-shm` 을 지우고 그 bak 파일을 `kyeot.db` 로 바꿔 넣는다. 그 줄이 없으면 DB 는 그대로 둔다. 지난 업데이트의 bak 을 넣으면 그 뒤의 기록을 잃는다.
3. `~/.local/bin/kyeot.prev` 를 `~/.local/bin/kyeot` 로 바꿔 넣고 다시 띄운다. 맥은 `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/app.kyeot.server.plist`(실패하면 `gui/$(id -u)` 대신 `user/$(id -u)`), 리눅스는 `systemctl --user start kyeot-server`. `kyeot update` 가 없는 옛 판으로 돌아가도 그대로 된다.

이전 판으로 돌아가지 않고 새 판으로 띄우면 같은 업데이트를 다시 거치며 백업을 하나 더 만든다. `kyeot uninstall` 은 `kyeot.prev` 도 지운다.

## 백업과 다른 컴퓨터로 옮기기

서버는 켜져 있는 동안 하루 한 번 DB 를 데이터 폴더의 `backups/kyeot-<시각>.db` 로 복사한다(600). 켜질 때와 한 시간마다 마지막 백업이 24시간 지났는지 보고, 최근 7개만 남긴다. 첨부(`uploads/`)는 한 번 쓰면 고치지 않아서 넣지 않는다. 실패하면 로그만 남기고 서버는 계속 돈다. 앱에서 서버째 지우거나 `kyeot uninstall --purge=mine` 으로 내 것만 지우면 하루 백업, `kyeot.db.bak-…`, restore 전 묶음도 함께 지운다(내 것만 지울 때는 남은 사람 몫으로 하루 백업을 새로 하나 만든다). 사용자가 `kyeot backup` 으로 따로 만든 파일은 지우지 않는다. 앱에서 탈퇴하거나 주인을 넘긴 사람의 기록은 하루 백업에 최대 7일 남는다.

하루 백업은 같은 디스크에 남는다. 디스크가 고장 나거나 컴퓨터를 바꿀 때는 `kyeot backup` 으로 파일 하나를 만들어 다른 곳에 둔다.

| 명령 | 하는 일 |
|---|---|
| `kyeot backup <파일>` | DB, 첨부, 센서 기록, 앱 업데이트 묶음, 같은 컴퓨터 Hermes 의 에이전트 id, 소유자 토큰, 첨부 서명 키를 tar.gz 하나로 만든다(600). 서버가 켜져 있어도 된다. 있는 파일은 덮지 않는다 |
| `kyeot restore [--yes] <파일>` | 서버가 꺼져 있을 때만 한다. 먼저 지금 데이터를 `backups/before-restore-<시각>.tar.gz` 로 백업하고(최근 3개만 남긴다), 백업 속 것으로 바꾼다. 백업 DB 가 더 새 판 구조이거나 묶음이 깨졌으면 아무것도 바꾸지 않고 멈춘다 |

서버는 도는 동안 DB 옆 `kyeot.db.lock` 을 쥔다. restore 는 그 잠금을 잡은 채로 바꾸므로, 다른 포트로 띄운 서버가 있어도 바꾸지 않고, 바꾸는 사이 서버가 뜨지도 못한다(서버는 15초 기다린 뒤 끝난다).

백업 파일에는 소유자 토큰이 들어 있다. 이 파일만 있으면 앱 연결 없이 서버에 들어갈 수 있으니 남에게 주지 않는다. 링크 미리보기 캐시, `connect.json`(다른 서버에 붙는 이 컴퓨터의 연결), 앱 업데이트 서명 개인키, 하루 백업은 넣지 않는다. 소유자 토큰을 `KYEOT_OWNER_TOKEN` 으로 주고 있으면 토큰은 빠지고, 되살린 뒤에도 그 환경변수를 그대로 준다.

다른 컴퓨터로 옮기는 순서.

1. 옛 컴퓨터에서 `kyeot backup ~/kyeot.backup` 을 실행한다.
2. 파일을 새 컴퓨터로 옮긴다(AirDrop, `scp` 등).
3. 새 컴퓨터에 곁을 깐다(`curl -fsSL --proto '=https' https://kyeot.app/install | sh`). 설치가 서버를 띄웠으면 `kyeot service uninstall` 로 내린다.
4. `kyeot restore ~/kyeot.backup` 을 실행하고 `y` 로 답한다.
5. `kyeot service install` 로 다시 띄운다. 백업이 옛 판 구조면 이때 새 구조로 바꾸고 그 전 DB 를 `kyeot.db.bak-…` 로 남긴다.
6. 옛 컴퓨터의 서버를 끈다(`kyeot service uninstall`). 두 컴퓨터가 같은 데이터로 함께 돌면 대화가 갈라진다.

서버 id 와 소유자 토큰이 같이 옮겨진다. 주소가 그대로면 폰 앱은 할 일이 없다. 주소가 바뀌었으면 폰 앱의 설정 › 서버에서 그 서버의 주소를 새 주소로 고친다. 새 주소가 같은 서버 id 를 알려 오므로 앱은 같은 서버로 보고 읽음 표시와 이름을 그대로 쓴다. 탭 순서·고정·그룹은 서버 DB 에 있어 함께 옮겨진다. QR 로 새 주소를 찍으면 앱은 「이미 있는 서버와 같은 서버」라며 주소 고치기를 안내한다. 다른 컴퓨터에서 `kyeot connect` 로 붙던 에이전트는 그 컴퓨터에서 새 주소로 다시 `kyeot connect` 한다.

## 지금 기본값

환경변수를 하나도 주지 않고 `kyeot` 를 실행하면 아래처럼 돈다. 환경변수를 주면 그 값이 이긴다.

| 항목 | 기본값 | 바꾸는 법 |
|---|---|---|
| 듣는 주소 | `0.0.0.0:7700`. 휴대폰이 닿아야 해서 모든 곳에서 듣는다 | `KYEOT_ADDR` |
| 받는 망 | `loopback,tailscale,lan`. 이 기기와 Tailscale, 사설망(같은 Wi-Fi)만 받고 공인 주소는 403 | `KYEOT_NETS`. 이름(`loopback`, `tailscale`, `lan`)이나 CIDR 목록. 전부 열려면 `all`. 같은 컴퓨터의 Hermes 어댑터가 켜져 있으면(`KYEOT_HERMES` 가 off 가 아니면) loopback 이 빠져 있어도 넣고 경고한다. 사설, Tailscale, loopback 밖의 대역(`all`, 공인 CIDR)을 열었는데 https 가 없으면 시작을 거부한다(종료 코드 1) |
| https | 끔. 평문 http 로 받는다(Tailscale 이 암호화를 대신한다) | `KYEOT_TLS_CERT`, `KYEOT_TLS_KEY`(둘 다). 켜면 같은 컴퓨터의 Hermes 어댑터용 평문 입구를 `127.0.0.1` 빈 포트에 따로 연다(인증서가 도메인 앞이라 루프백 https 는 검사를 못 넘는다). `kyeot pair` 의 QR 주소는 `https://<인증서의 첫 도메인>`. 인증서는 `tailscale cert`, Let's Encrypt 등에서 받는다. 같은 컴퓨터의 https 프록시나 Tailscale serve·Funnel 뒤에 두는 것은 지원하지 않는다. 모든 요청이 이 기기에서 온 것으로 보여 `KYEOT_NETS` 가 거르지 못하고 짝짓기 잠금이 모두에게 걸린다. 서버는 그런 요청을 보면 기록에 경고하고 `kyeot service status` 에도 표시한다(막지는 않는다). 다른 컴퓨터의 프록시라면 `KYEOT_NETS` 를 그 주소로 좁힌다 |
| 평문 허용 | 끔. 인터넷에 연 서버는 https 가 있어야 뜨고, `kyeot pair` 도 공인 주소에 `http` 를 담지 않는다 | `KYEOT_ALLOW_PLAIN=1`. 경고를 찍고 평문으로 띄운다 |
| 데이터 폴더 | 설정 폴더(`os.UserConfigDir()`)의 `kyeot/`. 맥은 `~/Library/Application Support/kyeot`, 리눅스는 `~/.config/kyeot`. 폴더 권한 700. 작업 폴더의 `data/kyeot.db` 는 쓰지 않는다(봤다는 로그만 남긴다). DB 는 그 안의 `kyeot.db`, 첨부(`uploads/`)와 앱 업데이트(`updates/`), Hermes 에이전트 id(`hermes-agent`)는 DB 옆 | `KYEOT_DB` |
| 사용자별 첨부 총량 | 20GB. 글에 붙은 파일도 센다. 넘으면 올리기가 507 | `KYEOT_UPLOAD_TOTAL_MB`(MB, 0 이면 기본값) |
| 소유자 토큰 | 처음 실행 때 무작위로 만들어 데이터 폴더의 `owner-token`(권한 600)에 둔다. 옛 자리 `~/.config/kyeot/owner-token` 이 있으면 그것을 읽는다 | `KYEOT_OWNER_TOKEN`(16자 이상). 바꾸려면 `kyeot pair --rotate` — 새 토큰을 쓰고 서버 서비스를 다시 켜고 새 QR 을 그린다. 옛 토큰으로 연결한 폰은 새 QR 을 찍어야 한다. 환경변수로 준 토큰은 그 값을 바꾼다. 주인이 앱에서 주인 권한을 넘기면 서버가 이 파일을 새 값으로 바꾸고, 그 토큰은 어느 계정으로도 들어가지 않는다. 넘긴 뒤 `pair --rotate` 는 한 번 묻고 이 컴퓨터에 새 주인 계정을 만들며, 이어받았던 사람은 관리자가 된다. 환경변수로 준 토큰이면 앱에서 넘기기를 막는다 |
| 첨부 서명 키 | 데이터 폴더의 `uploads-key`(권한 600). 처음 시작 때 지금 소유자 토큰에서 나오던 키로 만들어 옛 첨부 주소를 잇고, 그 뒤로는 토큰과 무관하다 | 바꾸면 이미 나간 첨부 주소가 모두 열리지 않는다: `kyeot pair --rotate-uploads` |
| 앱 연결 | 토큰을 처음 만든 실행에서 터미널이면 연결 QR 을 띄운다. 로그 파일로 나갈 때는 QR 을 쓰지 않는다. 다시 보려면 `kyeot pair`. QR 의 서버 주소는 Tailscale, 사설망 순으로 고른다 | `kyeot pair -host <주소>`. 이름(`x.example.com`)이면 scheme 과 듣는 포트를 붙이고, 전체 주소(`https://x.example.com`, `https://x:8443`)면 그대로 담는다 |
| 공개 주소 | 없음 | `KYEOT_PUBLIC_URL=https://kyeot.example.com`. 프록시나 도메인 뒤에 둘 때 첫 실행 QR 과 `kyeot pair` 가 이 주소를 담는다(`-host` 가 이긴다). 있으면 `KYEOT_ADDR=127.0.0.1:…` 의 「폰이 닿지 않는다」 경고를 내지 않는다. 공인 주소에 `http` 를 주면 시작을 거부한다 |
| 접속 기록 | 끔 | `KYEOT_ACCESS_LOG=1`. 요청마다 서버 기록에 한 줄: 시각, 계정 id 앞 8자, 메서드, 경로(쿼리·본문 없이), 상태, 걸린 시간, 접속 IP, 짧은 User-Agent. 웹소켓은 열림과 닫힘에 한 줄씩. 토큰은 남기지 않는다. 남에게 열어 두는 데모 서버용이며, 켜면 방문자의 IP 가 기록에 남는다 |
| 주소의 토큰 | `?token=` 은 웹소켓 접속(`/client`, `/agent`)에서만 받는다. REST 는 `Authorization: Bearer` 헤더만. 폰 앱(iOS, 안드로이드)은 웹소켓도 헤더로 보내고, 주소 토큰은 브라우저(웹 판)와 옛 앱만 쓴다 | 없다 |
| 대화 언어 | 앱 언어를 Hermes 실행마다 `/v1/runs` 의 `instructions` 로 짧게 넘긴다. Hermes 설정 파일은 고치지 않는다 | 없다 |
| 서버 판 | `GET /version` 이 `{"version":"x.y.z"}` 를 인증 없이 낸다. 다른 컴퓨터의 설치 스크립트가 이 판을 받는다. 판이 `dev` 면 503 | 빌드 때 `-ldflags "-X main.version=<판>"` (`VERSION` 파일) |
| 자동 실행 | `kyeot service install` 이 launchd(맥, label `app.kyeot.server`)나 systemd(리눅스, `kyeot-server.service`)에 등록한다. 에이전트 연결 서비스(`kyeot connect`)와 같은 코드(`cmd/kyeot/service.go`)를 쓰고, 소유자 토큰은 서비스 파일에 넣지 않는다. | `kyeot service uninstall`, `status` |
| 지우기 | `kyeot uninstall` 은 서비스와 실행 파일만 내리고 기록을 남긴다. `--purge` 는 키와 기록까지 지운다. 다른 사용자가 있으면 사람마다 방과 대화 수를 보이고 「모두 지우기」와 「내 것만 지우기」(다른 사람 기록은 남기고 프로그램만 내린다) 가운데 고르게 한다. 물을 수 없는 실행(스크립트)이면 아무것도 지우지 않고 멈춘다. | `--purge=all`, `--purge=mine` |

## 실행 파일 릴리스

다른 컴퓨터가 받는 실행 파일과 설치 스크립트는 이 저장소(`kyeotapp/kyeot`)의 GitHub Releases 에 둔다. `scripts/release-build.sh <판>` 이 `dist/<판>/` 에 `kyeot_darwin_arm64`, `kyeot_darwin_amd64`, `kyeot_linux_arm64`, `kyeot_linux_amd64`, `install.sh`(`scripts/install.sh` 사본)와 `checksums.txt`(이 다섯 파일의 `shasum -a 256`)를 만든다. 이어서 `~/.config/kyeot/release-signing/id_ed25519`(없으면 `scripts/release-key.sh` 로 만든다) 로 `checksums.txt` 에 `ssh-keygen -Y sign -n kyeot-release` 서명을 해 `checksums.txt.sig` 를 만든다. 키가 없으면 빌드하지 않는다. `install.sh` 는 스크립트에 박힌 공개 키로 이 서명을 확인한 뒤에만 체크섬을 믿고, 서명이 없거나 맞지 않으면 설치하지 않는다. 키를 바꾸면 `install.sh` 의 `RELEASE_PUBKEY` 도 바꿔 새 릴리스를 latest 로 올린다. 판은 `x.y.z` 숫자 꼴만 받는다. 빌드는 `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=<판>"` 이고, DB 가 순수 Go 인 `modernc.org/sqlite` 라 맥에서 리눅스 판까지 교차 빌드된다. 그 폴더의 파일을 전부 `v<판>` 태그 릴리스에 올린다(`gh release create v<판> dist/<판>/kyeot_* dist/<판>/install.sh dist/<판>/checksums.txt dist/<판>/VERSION dist/<판>/checksums.txt.sig -R kyeotapp/kyeot`). 사용자는 `releases/latest/download/install.sh` 로 가장 최근 릴리스의 스크립트를 받으므로, 스크립트를 바꾼 릴리스는 최신(latest)으로 올려야 한다. 스크립트는 서버의 `/version` 이 알려 준 판을 받으므로 서버도 같은 판으로 빌드해 둔다. 스크립트 상단의 `MIN_VERSION` 보다 낮은 판은 받지 않으니, 옛 판에 보안 결함이 있으면 그 값을 올린다. 판은 `kyeot version` 으로 본다.

## 서버 첫 실행과 에이전트 연결

에이전트는 주지 않고 깔린 에이전트를 서버가 찾아 붙인다. 서버는 시작할 때 이 컴퓨터의 Hermes 를 찾기만 하고, 붙이는 것은 사용자가 앱에서 연결을 누른 뒤다. 앱이 서버에 붙으면 「이 컴퓨터에서 Hermes 를 찾았어요. 연결할까요?」를 묻는다.

```
$ kyeot
  데이터 폴더를 만들었어요: ~/Library/Application Support/kyeot
  Hermes 를 찾았다. 앱에서 연결하면 붙는다
  ? 맥이 켜지면 곁이 저절로 뜨게 할까요? (Y/n)
  에이전트가 사진이나 문서 같은 보호된 폴더를 처음 열면 맥이 허락을 물어요.
  그 창은 맥에서만 답할 수 있어요. 맡길 일인지 보고 고르면 다시 묻지 않아요
  폰의 곁 앱으로 이 QR 을 찍어 주세요
  (QR)
```

| 경우 | 서버가 하는 일 |
|---|---|
| Hermes 가 깔려 있다 | 시작 때는 찾기만 한다(`HERMES_HOME` 폴더나 `API_SERVER_KEY` 가 있으면 찾음). 앱이 `GET /api/local-hermes` 로 상태를 보고 묻고, 사용자가 누르면 `POST /api/local-hermes/connect` 가 Hermes API 에 닿는지 본 뒤 내부 에이전트로 등록한다. 에이전트 id 파일(데이터 폴더의 `hermes-agent`)이 동의 기록이다. 에이전트 키는 서버 안에서만 오가고 어댑터는 서버 안에서 띄운다 |
| 이미 연결한 적이 있다 | 에이전트 id 파일이 있으면 시작 때 묻지 않고 바로 붙인다. 앱에서 연결을 해제했으면 시작 때 다시 붙이지 않고, 앱에서 연결을 다시 누르면 새 에이전트로 붙인다 |
| Hermes 의 키가 없다 | 상태의 `problem` 이 `no_key`. 연결을 눌러도 붙이지 않는다 |
| Hermes 의 API 서버가 꺼져 있다 | 연결 응답의 `problem` 이 `unreachable`(키가 틀리면 `bad_key`). 앱이 「Hermes 의 API 서버를 켜야 해요」와 켜는 명령을 알린다. Hermes 설정을 몰래 바꾸지 않는다 |
| 에이전트가 없다 | 「에이전트가 필요해요」와 추천 에이전트 설치 링크를 보여 준다. QR 은 그대로 띄워 앱 연결은 먼저 해 둔다 |
| 다른 컴퓨터의 Hermes | 앱에서 에이전트를 추가하면 6자리 짝짓기 코드와 `curl -fsSL --proto '=https' https://kyeot.app/install \| sh -s -- '<서버 주소>' <코드>` 한 줄이 나온다. 그 컴퓨터에 `kyeot` 가 없으면 서버와 같은 판을 릴리스에서 받아 `~/.local/bin` 에 깔고 `kyeot connect` 까지 한다. 맥과 리눅스만. 토큰은 사람 눈에 안 보인다 |
| 직접 만든 에이전트 | 같은 짝짓기 코드로 `POST /pair/agent {code}` → `{token}` 을 받아 붙는다. 붙이는 방법은 `docs/connect-your-agent.ko.md` |
| 나중에 에이전트를 더한다 | 앱의 「에이전트 추가」(지금 있다) 또는 위 명령 |
| 폰을 다시 연결한다 | `kyeot pair` 가 QR 을 다시 띄운다 |

에이전트 쪽 설정 파일은 바꾸지 않는다. 대화 언어는 실행마다 Hermes 에 넘기는 지시(`instructions`)로 전한다. 이 지시는 그 실행에만 붙고 저장되지 않는다.

맥의 보호 폴더 권한 창(TCC)은 맥 앞에서만 답할 수 있고 곁이 대신 누를 수 없다. 그래서 첫 실행에서 사람이 맥 앞에 있을 때 미리 알린다. 허용할지는 사용자가 정한다. 곁이 검색 범위를 묶어 창을 피하게 하지는 않는다. 작업 폴더 밖을 다루는 것도 에이전트의 일이기 때문이다. 대신 창에 걸려 도구가 멈추면 앱의 도구 줄이 20초 뒤 경과 시간과 실행 중인 명령을 보여 준다(`Chat.tsx` 의 `SLOW_TOOL_SECS`).
