# 내 에이전트를 곁에 연결하기

[English](connect-your-agent.md) · **한국어**

이 문서는 직접 짠 봇이나 다른 에이전트 프레임워크를 곁에 붙이려는 개발자를 위한 것이다. 곁은 세 부분으로 되어 있다. 휴대폰과 웹에서 도는 앱, 대화와 화면을 저장하고 중계하는 덱 서버, 그리고 실제로 일을 하는 에이전트다. 앱과 덱 서버는 모든 사용자가 같은 코드를 쓰고, 사용자마다 다른 것은 에이전트뿐이다. 에이전트는 덱 서버에 밖에서 웹소켓(WebSocket) — 한 번 연결하면 양쪽이 아무 때나 메시지를 주고받을 수 있는 통로 — 으로 접속한다. 연결을 거는 쪽이 에이전트라서, 에이전트가 집 맥에서 돌더라도 집 공유기의 포트를 열 필요가 없다. 덱 서버만 에이전트가 닿을 수 있는 곳에 있으면 된다.

메시지는 모두 JSON 한 덩어리이고 `type` 칸으로 종류를 구분한다. 필드 이름과 뜻은 `internal/protocol/protocol.go` 가 원본이며, 이 문서의 메시지 표는 테스트(`internal/protocol/guide_test.go`)가 그 원본과 맞는지 검사한다.

## 최소 계약

에이전트가 반드시 해야 하는 일은 셋뿐이다. 받은 대화(`task.start`, `user_message`)에 `message` 로 답하고, 끝나면 `task.end` 를 보내고, 사람의 확인이 필요하면 `approval` 을 보낸다. 나머지 메시지(`status`, `hello`, `report`, `routine.run`, `room.*`, `ui.*`, `data.*`)는 보내지 않아도 덱 서버는 거절하지 않고 앱은 그 기능의 화면을 숨긴다. 덱이 보내는 메시지 중 모르는 것(`routine.*`, `ui.ack`, `error`)은 무시해도 서버 상태가 깨지지 않는다.

`task.end` 를 깜빡해도 두 가지로 보완된다. 에이전트가 답한 뒤 `status` 를 `idle` 로 보내면 덱 서버가 그 대화를 `done` 으로 닫는다. 에이전트가 끊긴 채 1분(`KYEOT` 서버 설정 `AgentGrace`)이 지나면 진행 중이던 대화를 `failed` 로 닫는다. 덱 서버가 다시 켜졌을 때도 같다. 1분 안에 다시 붙어 그 대화에 무언가 보내거나 `status` 를 `working`·`blocked` 로 알리지 않으면 `failed` 로 닫고, 일하는 중이라고 알렸으면 `idle` 이 될 때까지 기다린다. 그래도 매 답 끝에 `task.end` 를 보내는 쪽이 정확하다.

곁은 특정 에이전트 제품을 전제하지 않는다. Hermes 브리지(`internal/hermesbridge`)는 이 규약을 구현한 한 예일 뿐이다. 보고서의 상태값이나 일정 문구, 루틴 이름 표기처럼 해석이 필요한 것은 덱 서버가 맡는다(아래 「보고서」).

## 에이전트 등록과 토큰

앱의 사이드바에서 에이전트 추가를 누르고 이름을 정하면 덱 서버가 에이전트 토큰을 만들어 보여 준다. 토큰은 이 에이전트가 누구인지 증명하는 비밀값이다. 덱 서버는 토큰 원문을 저장하지 않고 해시(hash) — 원문을 되돌릴 수 없게 뒤섞은 지문 — 만 남기므로, 토큰은 등록할 때 **한 번만** 보인다. 잃어버리면 에이전트를 다시 등록해야 한다. 코드 저장소나 대화방에 붙여 넣지 말고 환경 변수나 비밀 파일로 넘긴다.

앱 대신 HTTP 로 등록할 수도 있다. 소유자 토큰은 덱 서버를 띄울 때 정한 `KYEOT_OWNER_TOKEN` 값이다. 응답의 `token` 이 에이전트 토큰이다.

```sh
curl -s -H "Authorization: Bearer $KYEOT_OWNER_TOKEN" -d '{"name":"내 봇"}' http://127.0.0.1:7700/api/agents
```

## 접속

주소는 `ws://<덱 주소>/agent` 이고, 덱 서버가 TLS(인터넷 구간을 암호화하는 방식) 뒤에 있으면 `wss://` 를 쓴다. 웹소켓을 열 때 HTTP 머리말에 `Authorization: Bearer <에이전트 토큰>` 을 싣는다. 토큰이 없거나 틀리면 덱 서버는 401 로 거절하고, 덱 서버가 `KYEOT_NETS` 로 접속 가능한 망을 좁혀 두었다면 그 밖에서 온 연결은 403 이다.

접속에 성공하면 덱 서버는 에이전트를 `idle` 로 표시한다. 에이전트는 접속 직후 자기 상태를 `status` 로 한 번 보내는 것이 좋다. 지금 일을 하는 중이었다면 `working` 이라고 알려야 앱이 맞게 그린다.

```json
{"type": "status", "state": "idle"}
```

같은 토큰으로 두 번째 연결이 들어오면 덱 서버는 앞 연결을 닫는다(닫기 코드 4000, 이유 `replaced by a new connection`). 그러니 재접속 코드는 이전 연결을 확실히 버린 뒤 새로 걸어야 둘이 서로를 밀어내지 않는다. 덱 서버가 받는 메시지 하나는 1MB 까지다.

형식이 틀린 메시지를 보내면 덱 서버는 아무것도 저장하지 않고 `error` 를 돌려준다. `reason` 이 `invalid message` 면 `type` 을 모르거나 필수 필드가 비었거나 값이 허용 범위 밖이라는 뜻이다. 다른 에이전트의 대화나 없는 대화 번호를 쓰면 `unknown task <번호>` 가 온다.

## 에이전트가 보내는 메시지

필드 칸에 종류를 따로 적지 않은 것은 문자열이고, 괄호 안은 넣지 않아도 되는 필드다. `task` 는 대화 번호다. 곁에서는 대화 하나가 작업 하나다.

| type | 필드 | 언제 |
|---|---|---|
| `status` | `state` (`idle`, `working`, `blocked`) | 에이전트의 상태가 바뀔 때. `blocked` 는 사람의 답을 기다리는 중 |
| `hello` | 모두 선택. `kind`, `version`, `models` (`[{role, name}]`, 10개까지), `features` 문자열 배열 | 접속 직후 한 번. 앱의 에이전트 정보에 종류와 역할별 모델이 보인다. `role` 은 `chat`, `delegate`, `title` 처럼 영어 키로 보낸다. `features` 는 `routines`, `costs`, `ui`, `routines.run`(루틴 바로 실행, 선택), `routines.manage`(`routine.create`·`routine.toggle`·`routine.remove` 처리. 선언했는데 빠지면 앱이 켜고 끄기를 막는다) 중 쓰는 것. 안 보내도 덱 서버가 받은 메시지로 추론한다. 보내면 빠진 기능은 없다고 보므로 쓰는 것을 다 적는다 |
| `task.open` | `task`, `title` | 에이전트가 스스로 새 대화를 열 때. `task` 는 에이전트가 정하고, 이미 있는 번호면 `error` 가 온다 |
| `task.title` | `task`, `title` | 대화 내용으로 제목을 지어 알릴 때. 앞뒤 공백을 빼고 1~80자, 줄바꿈은 공백이 된다. 사용자가 앱에서 바꾼 제목은 덮지 않고 조용히 무시한다 |
| `message` | `task`, `text` | 답을 보낼 때. 마크다운의 제목, 목록, 코드, 인용, 굵게, 링크를 그린다 |
| `draft` | `task`, `text` | 쓰는 중인 답을 미리 보여 줄 때. 화면에만 흘리고 저장하지 않는다. 끝나면 `message` 로 완성본을 보낸다 |
| `tool` | `task`, `call`, `phase` (`started`, `completed`), `name`, `summary`, (`ok` 참거짓, `detail`) | 도구를 부를 때와 끝났을 때. 같은 `call` 의 두 메시지가 화면에서 한 줄로 합쳐진다 |
| `approval` | `task`, `id`, `question`, `options` 문자열 배열, (`kind`) | 사람에게 확인받을 때. 대화가 `waiting` 으로 바뀐다. `kind` 가 `choice` 면 확인 버튼 대신 대화 안에 번호 목록으로 그린다. 같은 대화에서 `id` 를 다시 보내면 앞의 열린 요청을 대신한다 |
| `artifact` | `task`, `name`, `url` | 만든 파일이나 페이지를 보여 줄 때 |
| `task.end` | `task`, `result` (`done`, `failed`, `cancelled`) | **매 답 끝에 반드시.** 대화 한 번을 끝낼 때. 이미 끝났거나 사용자가 취소한 대화의 상태는 바꾸지 않는다. 안 보내면 대화가 진행 중으로 남는다(「최소 계약」의 보완 규칙 참고) |
| `reaction` | `task`, `emoji` (16바이트 이하) | 사용자의 마지막 말에 이모지를 달 때 |
| `routine.run` | `routine`, `run`, `text`, (`routineName`, `at` 밀리초, `ok`, `quiet`) | 예약 작업(루틴)이 한 번 돈 결과를 끝난 대화로 남길 때. 같은 `routine` 과 `run` 은 한 번만 저장되므로 재접속 때 다시 보내도 된다. 돌지 못했으면 `ok: false` 와 이유를 `text` 에 담는다 — 실패한 실행으로 남는다. 보낼 글 없이 끝났으면 `quiet: true` 로 보내고 `text` 는 비워도 된다 — 대화가 되지 않고 루틴의 실행 이력에만 남는다 |
| `report` | `report` 객체 | 루틴 목록과 비용을 알릴 때. 덱 서버가 정규화해 저장하고 앱에 넘긴다(아래 「보고서」). 객체가 아니면 `error` |
| `room.create` | `name`, (`routines`, `routine`) | 대화를 묶어 둘 방을 만들 때. 이름은 40자에서 자른다. `routines` 는 이 방에 결과를 보낼 루틴 id 배열(최대 20개), `routine` 은 옛 한 개짜리 — 둘 다 오면 합친다. 만든 뒤 루틴을 더하고 빼는 것은 사용자가 앱에서 한다 |
| `room.rename` | `id`, `name` | 이 에이전트의 방 이름을 바꿀 때. 다른 에이전트의 방이면 `error` |
| `ui.put` | `screen` 객체, (`place`) | 화면 설계도를 올리거나 고칠 때. 결과는 `ui.ack` 로 온다. `place` 는 화면 자리로 `tab`(탭 끝, 차 있으면 탭 밖에 두고 `reason` 에 알림), `off`(탭 밖), `hidden`(숨김, 링크로만 열림) 중 하나다. 비우면 새 화면만 칸이 남을 때 탭 끝에 두고(차 있으면 탭 밖에 두고 `reason` 에 알림), 이미 있던 화면은 자리를 바꾸지 않는다 |
| `ui.remove` | `id` | 화면을 지울 때. 결과는 `ui.ack` |
| `ui.layout` | `tabs` 문자열 배열 | 탭 막대에 보일 화면 id 와 순서를 정할 때. 결과는 `ui.ack` |
| `ui.list` | 없음 | 지금 화면과 탭, 데이터 묶음, 조립 부품을 읽을 때. 답은 `ui.state` |
| `ui.versions` | `id` | 화면의 저장된 판을 볼 때(최근 20개). 답은 `ui.ack` 이고 `ui` 가 `{versions: [{version, createdAt, current, summary}]}`, 최신부터. `summary` 는 한 줄(제목, 블록 수, 블록 종류) |
| `ui.revert` | `id`, `to` 숫자 | 앞선 판으로 되돌릴 때. 덱이 그 판 설계도를 새 판으로 다시 올린다. 결과는 `ui.ack`, 새 판 번호는 `reason` 에 |
| `ui.part.put` | `part` 객체 | 조립 부품(기본 부품 여러 개를 묶은 틀)을 올리거나 고칠 때. 화면은 `{"type": "use", "part": id, "with": {...}}` 로 부른다. 결과는 `ui.ack`, `id` 는 부품 id. 고치면 쓰는 화면이 다 바뀐다 |
| `ui.part.remove` | `id` | 조립 부품을 지울 때. 쓰는 화면이 있으면 거절하고 `reason` 에 화면 id 를 적는다 |
| `data.put` | `name`, `data` 객체 | 데이터 묶음을 통째로 올리거나 바꿀 때. 결과는 `ui.ack` |
| `data.remove` | `name` | 데이터 묶음을 지울 때. 결과는 `ui.ack` |

`ui.*` 와 `data.*` 에 `req` 문자열을 붙이면 `ui.ack` 에 그대로 돌아온다. 동시에 보낸 요청의 답을 구별할 때 쓴다.

## 보고서

`report` 는 원본만 보내면 된다. 빠진 목록은 덱 서버가 빈 목록으로 채우고, 표시용 값은 덱 서버가 만든다(`internal/report`). 최소 모양은 이렇다.

```json
{"type": "report", "report": {
  "routines": [
    {"id": "daily-news", "name": "daily-news", "state": "ok", "cron": "0 9 * * *", "about": "아침 뉴스를 모아 알린다"},
    {"id": "sync", "name": "sync", "state": "off", "every": 120}
  ],
  "periods": [{"key": "week", "name": "최근 7일", "cost": 1.23}]
}}
```

| 칸 | 규칙 |
|---|---|
| `routines[].id` | 루틴 번호. `routine.run` 의 `routine` 과 같은 값. `id` 나 `name` 중 하나는 있어야 한다 |
| `routines[].state` | `ok`(켜짐), `off`(꺼짐), `done`(한 번 돌고 끝남), `error`(지난 실행 실패). 흔한 동의어(`paused`, `completed`, `failed` 등)도 받는다 |
| `routines[].cron` / `every`(분) / `at`(밀리초) | 일정 원본. 덱 서버가 `schedule` 문구를 만들고 앱이 자기 언어로 바꾼다 |
| `routines[].on` | 스위치 값. 없으면 `state` 로 정한다 |
| `routines[].about`, `info.body` | 「하는 일」 한 문장과 펼쳐 보는 세부 동작(마크다운). 아래 「루틴 설명」 |
| `title` | 덱 서버가 만든 표시 이름. Hermes 는 하이픈을 공백으로 풀고, 다른 에이전트는 이름 그대로 |
| `periods`, `daily`, `forecast` | 비용. 없으면 앱이 비용 탭을 비운다. 모양은 `app/src/lib/protocol.ts` 의 `Period`, `DayCost`, `Forecast` |

보고서에 `routines` 가 있으면 덱 서버는 에이전트에 `routines` 기능을, `periods` 나 `daily` 에 값이 있으면 `costs` 기능을 표시한다. `ui.put` 이나 `data.put` 을 보내면 `ui` 기능이다. 앱은 표시된 기능으로 화면을 가른다. 아무것도 못 받았으면 「아직 받지 못했어요」, 받았는데 그 기능이 없으면 「이 에이전트는 …를 알려 주지 않아요」. 보고서 없이 `routine.run` 만 보내도 덱 서버가 루틴 번호와 이름을 배워 두어 방에 묶을 수 있다.

## 덱 서버가 보내는 메시지

| type | 필드 | 언제 |
|---|---|---|
| `task.start` | `task`, `text`, (`context`, `attachments`, `lang`, `askedBy`, `askedByName`) | 사용자가 앱에서 새 대화를 시작했을 때. `lang` 은 앱 언어(`ko`, `en`)라 대화 제목을 이 언어로 지으면 된다. `askedBy` 가 있으면 공유 방의 멤버가 주인의 에이전트를 부른 것이다(주인이면 없다) |
| `user_message` | `task`, `text`, (`context`, `quote`, `edit`, `attachments`, `askedBy`, `askedByName`) | 이어지는 대화에 사용자가 말했을 때. 끝난 대화였다면 다시 열린 것이다. `edit` 은 `{id, original}` 로, 사용자가 앞서 한 말을 고친 것이다 |
| `approval_result` | `task`, `id`, `choice` | 사용자가 `approval` 에 답했을 때. `choice` 는 보낸 `options` 중 하나 그대로다 |
| `cancel` | `task` | 사용자가 대화를 멈췄을 때 |
| `routine.create` | `name`, `schedule`, `text` | 사용자가 앱에서 루틴을 만들었을 때. `text` 는 루틴이 할 일이다 |
| `routine.toggle` | `routine`, `on` 참거짓 | 사용자가 루틴을 켜거나 껐을 때 |
| `routine.remove` | `routine` | 사용자가 루틴을 지웠을 때 |
| `routine.trigger` | `routine` | 사용자가 화면의 갱신 버튼으로 루틴을 지금 한 번 돌리라고 했을 때. **선택 기능**이다 — `hello` 의 `features` 에 `routines.run` 을 넣은 에이전트에게만 온다. 돌린 결과는 평소처럼 `routine.run` 으로 보낸다 |
| `data.refresh` | `name` | 사용자가 화면의 갱신 버튼으로 그 묶음을 지금 다시 구하라고 했을 때. **선택 기능**이다 — `hello` 의 `features` 에 `data.refresh` 를 넣은 에이전트에게만 온다. 곁 브리지의 센서가 쓴다. 다시 구한 묶음은 `data.put` 으로 보낸다 |
| `ui.ack` | `id`, `ok` 참거짓, (`reason`, `req`) | `ui.put`, `ui.remove`, `ui.layout`, `ui.versions`, `ui.revert`, `ui.part.put`, `ui.part.remove`, `data.put`, `data.remove` 의 결과. `data.*` 이면 `id` 가 묶음 이름이다. 거절이면 `reason` 에 고칠 점이 적혀 있다. `ok` 인데 `reason` 이 있으면 받았지만 요청과 다르게 처리했다는 알림이다(탭이 차서 탭 밖에 둠). `req` 는 요청에 붙인 값 그대로 |
| `ui.state` | `ui` 객체 `{screens, tabs, data, parts}` | `ui.list` 의 답. `screens` 와 `parts` 는 올린 원문 그대로(`use` 포함) |
| `error` | `reason` | 형식이 틀렸거나 남의 대화, 없는 방을 가리켰을 때 |

몇 필드는 설명이 더 필요하다. `context` 는 에이전트가 모르는 앞 이야기를 실어 준다. 루틴 결과로 생긴 대화에 사용자가 처음 답할 때는 그 결과 원문이고, 방에서 새 대화를 시작할 때(`task.start`)는 방 이름과 최근 방 글(에이전트 답, 루틴 결과, 사람끼리 한 말, 2500자까지)이다. 「아직 안 잡힌 항목」처럼 질문이 방 글을 가리킬 수 있기 때문이다. 읽지 않아도 대화는 돈다. `quote` 는 사용자가 답장으로 인용한 원문이며 300자에서 자른다. 사용자가 자기가 보낸 말을 고친 경우에는 `edit` 에 원래 이벤트 번호와 원문이 오고, `quote` 에도 원문이 오며, `text` 는 고친 글 그대로다. 안내 문장을 붙이지 않으므로 에이전트가 자기 말로 다룬다(Hermes 브리지는 한국어 안내 줄을 붙인다).

`attachments` 는 사용자가 붙인 파일 목록이고 하나는 `{id, name, mime, size, url}` 모양이다. `url` 은 `/api/uploads/<id>?exp=…&sig=…` 처럼 덱 주소에 이어 붙이는 경로이고 서명이 들어 있어서, 에이전트는 머리말 없이 그대로 GET 해서 받으면 된다. 서명은 24시간 뒤 만료된다. 그 뒤에는 `/api/uploads/<id>` 를 `Authorization: Bearer <에이전트 토큰>` 으로 받으면 된다(그 에이전트의 작업에 붙은 파일만).

`schedule` 은 cron 다섯 칸(`0 9 * * 1-5`)이거나 간격(`every 2h`, `30m`)이다. cron 은 분, 시, 일, 월, 요일 다섯 칸으로 반복 일정을 적는 형식이다.

루틴 설명은 `report` 의 루틴마다 `about` 과 `info.body` 두 칸으로 보낸다. 앱은 `about` 을 「하는 일」에 한 문장으로 보이고, `info.body` 는 「세부 동작」을 펼쳤을 때 마크다운으로 그린다. 대상, 조건, 우선순위, 하지 않는 것, 멈추는 조건처럼 사용자가 루틴을 믿고 맡기는 데 필요한 정책은 `info.body` 에 제목과 목록으로 쓴다. 코드 상수에만 있는 값은 앱에 보이지 않는다. Hermes 브리지는 스크립트 루틴이면 스크립트 맨 위 설명문(파이썬 docstring)의 첫 줄을 `about`, 그 아래를 `info.body` 로 쓰고, 프롬프트 루틴이면 프롬프트 첫 문장만 `about` 으로 쓴다. 프롬프트 전문은 계정이나 로그인 방법이 들어 있을 수 있어서 보내지 않는다.

## 대화 한 번의 흐름

사용자가 앱에서 "내일 일정 알려 줘"라고 시작하면 에이전트는 이런 메시지를 받는다.

```json
{"type": "task.start", "task": "t_8f2c", "text": "내일 일정 알려 줘"}
```

에이전트는 일을 시작한다고 알리고, 도구를 부르기 전과 후에 같은 `call` 로 `tool` 을 두 번 보낸다. 앱은 이 둘을 "내일 일정 조회 → 3건" 한 줄로 합쳐 보여 준다.

```json
{"type": "status", "state": "working"}
{"type": "tool", "task": "t_8f2c", "call": "c1", "phase": "started", "name": "calendar.list", "summary": "내일 일정 조회"}
{"type": "tool", "task": "t_8f2c", "call": "c1", "phase": "completed", "name": "calendar.list", "summary": "3건", "ok": true}
```

답을 보내고 대화를 끝낸 뒤 쉬는 상태로 돌아간다.

```json
{"type": "message", "task": "t_8f2c", "text": "내일은 **3건**이 있어요. 10시 회의, 13시 점심, 19시 저녁."}
{"type": "task.end", "task": "t_8f2c", "result": "done"}
{"type": "status", "state": "idle"}
```

중간에 사람의 확인이 필요하면 `task.end` 전에 `approval` 을 보내고 `status` 를 `blocked` 로 바꾼다. 사용자가 고르면 `approval_result` 가 오고, 그 `choice` 를 보고 일을 이어 간다.

```json
{"type": "approval", "task": "t_8f2c", "id": "a1", "question": "19시에 예약할까요?", "options": ["승인", "거절"]}
{"type": "approval_result", "task": "t_8f2c", "id": "a1", "choice": "승인"}
```

목록에 보이는 대화 제목은 처음에 사용자의 첫 말을 80자에서 자른 것이다. 에이전트가 내용을 보고 더 나은 제목을 지었다면 `task.title` 로 알린다. 아무 때나, 몇 번이든 보내도 되고 같은 제목이면 아무 일도 일어나지 않는다.

```json
{"type": "task.title", "task": "t_8f2c", "title": "내일 일정 확인"}
```

사용자가 앱에서 대화 이름을 직접 바꿨다면 그 뒤로 에이전트가 보낸 제목은 덮지 않고 조용히 무시한다. `error` 도 오지 않는다. 다른 에이전트의 대화나 없는 대화 번호면 `unknown task <번호>` 가 온다.

끝난 대화에 사용자가 다시 말을 걸면 `user_message` 가 같은 `task` 번호로 온다. 대화가 다시 열린 것이니 같은 순서로 답하고 다시 `task.end` 를 보낸다.

## 화면 만들기

에이전트는 사용자의 탭을 직접 만들 수 있다. 앱은 정해진 부품만 그릴 줄 알고, 에이전트는 그 부품을 어떻게 늘어놓을지 적은 설계도를 올린다. 에이전트가 앱 안에서 코드를 실행할 길은 없다.

먼저 부품 목록을 읽는다. 카탈로그(catalog) — 앱이 그릴 수 있는 부품과 각 부품이 받는 속성, 크기 한도를 적은 목록 — 는 인증 없이 읽힌다.

```sh
curl -s http://127.0.0.1:7700/ui/catalog
```

카탈로그의 `components` 에는 부품마다 속성과 그 종류(`kind`)가 있다. `text` 는 글, `number` 는 숫자, `enum` 은 `values` 중 하나, `items` 는 배열이고, `required` 가 참인 속성은 빠지면 안 된다. `limits` 는 한 화면의 부품 수(`maxBlocks`), 설계도 크기(`maxSpecBytes`), 탭 막대에 보일 화면 수(`maxVisibleTabs`), 에이전트 하나가 가질 화면 수(`maxScreens`), 데이터 묶음 하나의 크기(`maxDataBytes`)와 에이전트 하나가 가질 묶음 수(`maxDataBundles`)다. `deckData` 는 덱이 이미 아는 값의 목록이며 아래 「데이터 묶음」 절에서 쓴다.

설계도는 화면 하나를 설명하는 JSON 이다. `version` 은 1, `id` 는 소문자와 숫자와 `-` 로 32자 이하, `title` 은 20자 이하, `icon` 은 iOS 시스템 아이콘(SF Symbols) 이름이다. 아래는 수익 탭을 올리는 `ui.put` 이다.

```json
{"type": "ui.put", "screen": {
  "version": 1,
  "id": "revenue",
  "title": "수익",
  "icon": "chart.line.uptrend.xyaxis",
  "blocks": [
    {"type": "stat", "label": "이번 달", "value": 1200000, "unit": "원", "compare": 950000, "better": "up"},
    {"type": "bars", "title": "하루씩", "items": [{"label": "11/09", "value": 42000}, {"label": "11/10", "value": 51000}]},
    {"type": "hbars", "title": "어디서 벌었나", "items": [{"label": "앱스토어", "value": 800000}, {"label": "광고", "value": 400000}]},
    {"type": "button", "label": "지금 다시 계산", "say": "수익 탭 숫자 지금 다시 계산해 줘"}
  ]
}}
```

덱 서버는 받은 설계도를 카탈로그로 검사한다. 통과하면 새 판으로 쌓고, 탭 막대에 빈 칸이 있으면 끝에 붙인 뒤 이렇게 답한다.

```json
{"type": "ui.ack", "id": "revenue", "ok": true}
```

거절하면 `ok` 가 거짓이고 `reason` 에 무엇이 틀렸는지 적혀 있다. 예를 들어 모르는 부품을 쓰면 `blocks[2]: 모르는 부품 "map"`, 필수 속성이 빠지면 `blocks[0]: stat 에 value 가 필요하다` 같은 문장이 온다. 에이전트는 이 문장을 읽고 설계도를 고쳐 다시 올리면 된다. 언어 모델 에이전트라면 `reason` 을 그대로 모델에게 돌려주는 것만으로 대개 스스로 고친다.

같은 `id` 로 다시 올리면 그 화면의 새 판이 된다. 이전 판은 지워지지 않고, 소유자는 `POST /api/agents/{id}/ui/screens/{sid}/revert` 로 예전 판을 다시 올릴 수 있다. 화면을 지울 때는 `ui.remove`, 탭 순서를 바꿀 때는 `ui.layout` 을 보낸다. `ui.layout` 의 `tabs` 에는 이미 있는 화면 id 만, 겹치지 않게, `maxVisibleTabs` 개까지 넣을 수 있다.

```json
{"type": "ui.layout", "tabs": ["revenue", "rooms", "costs"]}
```

고치기 전에 지금 상태를 보려면 `ui.list` 를 보낸다. 답으로 오는 `ui.state` 의 `screens` 는 화면마다 최신 판 하나씩이며 `{id, version, spec, updatedAt}` 모양이고 `spec` 이 설계도 원문이다. `tabs` 는 탭 막대에 보이는 화면 id 순서다. 처음 읽는 에이전트에게는 방, 루틴, 비용 견본 화면 셋이 이미 들어 있다.

```json
{"type": "ui.list"}
{"type": "ui.state", "ui": {"screens": [{"id": "rooms", "version": 1, "spec": {"version": 1, "id": "rooms", "title": "방", "icon": "number", "blocks": [{"type": "rooms"}]}, "updatedAt": 1762851600000}], "tabs": ["rooms"]}}
```

`data` 는 아래 「데이터 묶음」 절의 묶음들이다. 묶음이 하나도 없으면 빈 객체다.

고친 것을 되돌릴 때는 옛 설계도를 기억으로 다시 쓰지 않는다. 판 목록을 보고 번호로 되돌린다. 되돌리기도 새 판을 하나 더하므로 그것도 다시 되돌릴 수 있다. 되돌릴 수 있는 판은 최근 20개이고, 지운 화면도 되돌리면 다시 생긴다.

```json
{"type": "ui.versions", "id": "revenue"}
{"type": "ui.ack", "id": "revenue", "ok": true, "ui": {"versions": [{"version": 3, "createdAt": 1762851900000, "current": true, "summary": "\"수익\" · 2 blocks: stat, chart"}, {"version": 2, "createdAt": 1762851700000, "summary": "\"수익\" · 1 blocks: stat"}]}}
{"type": "ui.revert", "id": "revenue", "to": 2}
{"type": "ui.ack", "id": "revenue", "ok": true, "reason": "restored version 2 as version 4"}
```

## 데이터 묶음

숫자가 매일 바뀌는 화면은 설계도와 숫자를 따로 올리는 편이 낫다. 설계도에는 숫자 자리에 `{"$data": "경로"}` 만 두고, 숫자는 데이터 묶음(bundle) — 이름 하나에 붙은 JSON 객체 하나 — 으로 올린다. 그러면 숫자를 갈 때 설계도를 다시 올리지 않아도 되고 화면의 판 기록도 숫자 때문에 쌓이지 않는다.

```json
{"type": "data.put", "name": "revenue", "data": {"month": {"total": 1200000, "before": 950000}, "daily": [{"label": "11/09", "value": 42000}, {"label": "11/10", "value": 51000}]}}
{"type": "ui.ack", "id": "revenue", "ok": true}
```

`data.put` 은 그 이름의 묶음을 통째로 바꾼다. 일부만 고치는 방법은 없으므로 매번 전체를 보낸다. 이름은 소문자와 숫자와 `-` 로 32자 이하이고 `deck` 은 쓸 수 없다. 값은 JSON 객체여야 하고 공백을 뺀 크기가 `maxDataBytes`(256KB) 이하, 묶음 수는 에이전트마다 `maxDataBundles`(20개)까지다. 이미 있는 이름을 바꾸는 것은 개수에 들지 않는다. 덱 서버는 받은 시각을 갱신 시각으로 붙여 저장하고, 앱은 그 시각으로 「3시간 전 갱신」을 보여 준다. 묶음을 지울 때는 `data.remove` 를 보낸다. 소유자는 앱에서 `DELETE /api/agents/{id}/ui/data/{name}` 으로 지울 수 있다.

`ui.state` 의 `data` 에는 묶음마다 값과 갱신 시각이 들어 있다.

```json
{"screens": [...], "tabs": [...], "data": {"revenue": {"value": {"month": {"total": 1200000}}, "updatedAt": 1762830000000}}}
```

`$data` 의 값은 점으로 이은 경로다. 첫 마디가 묶음 이름이고 나머지는 그 객체 안으로 들어가는 키이며, 배열 안은 숫자 마디로 고른다. 위 묶음이라면 `revenue.month.total` 이 1200000 이고 `revenue.daily` 가 막대 목록, `revenue.daily.0.value` 가 42000 이다. 숫자 자리(`number`)에는 숫자를, 목록 자리(`items`)에는 배열을 가리켜야 한다. 값이 없거나 형식이 맞지 않으면 앱은 숫자 자리에 "—", 목록 자리에 「데이터가 아직 없어요」를 그린다. 서버는 경로를 검사하지 않으므로 이름을 틀리면 오류 없이 빈 자리로 보인다.

```json
{"type": "stat", "label": "이번 달", "value": {"$data": "revenue.month.total"}, "compare": {"$data": "revenue.month.before"}, "unit": "원", "better": "up"}
{"type": "bars", "title": "하루씩", "items": {"$data": "revenue.daily"}}
```

경로가 `deck.` 으로 시작하면 묶음이 아니라 덱이 이미 아는 값이다. 앱이 지금 가진 상태에서 계산하므로 에이전트가 올릴 것이 없다. 목록은 카탈로그의 `deckData` 에 있고 지금은 이렇다.

| 경로 | 자리 | 뜻 |
|---|---|---|
| `deck.costs.today` | number | 이 에이전트의 오늘 비용(USD) |
| `deck.costs.week` | number | 최근 7일 비용 |
| `deck.costs.month` | number | 이번 달 비용 |
| `deck.costs.daily` | items | 최근 7일 `{label: "11/5", value}` |
| `deck.approvals.count` | number | 이 에이전트가 기다리는 확인 요청 수 |
| `deck.tasks.today` | number | 오늘 시작한 대화 수 |
| `deck.routines.upcoming` | items | 다음 실행 예정 루틴 `{title, subtitle, value}` 5개까지. `subtitle` 은 일정 글, `value` 는 다음 시각 |

숫자를 매일 갈려면 에이전트 쪽 예약 작업이 필요하다. 덱 서버는 숫자를 계산하지 않는다. 사용자가 「매일 아침 갱신해 줘」라고 하면, 에이전트는 자기 루틴 기능으로 매일 아침 도는 작업을 하나 만들고 그 작업이 숫자를 계산해 `data.put` 을 보내게 한다. 설계도는 처음 한 번만 올리고 루틴은 묶음만 갈아 끼운다. 외부 서비스의 비밀번호나 API 키 같은 인증 정보는 묶음에 넣지 않는다. 묶음은 앱 화면에 그대로 내려가는 값이다.

## 덱 메시지를 직접 말하지 못하는 에이전트

이미 있는 에이전트 프레임워크는 대개 자기 HTTP API 나 채팅 연동 방식이 따로 있고, 웹소켓으로 곁에 접속하는 코드를 넣기 어렵다. 그럴 때는 사이에 브리지(bridge) — 두 쪽의 말을 서로 옮겨 주는 작은 프로그램 — 를 둔다. 브리지가 에이전트 대신 덱 서버에 접속하고, 덱 메시지를 받으면 에이전트의 API 를 부르고, 에이전트가 낸 결과를 덱 메시지로 바꿔 돌려준다.

`internal/hermesbridge` 가 그 예시다. Hermes 라는 에이전트의 HTTP API 를 덱 메시지로 옮긴다. `task.start` 를 받으면 Hermes 에 새 실행을 요청하고, 같은 대화는 같은 Hermes 세션으로 보내 맥락이 이어지게 한다. Hermes 가 흘려보내는 도구 사용 이벤트는 `tool` 로, 확인 요청은 `approval` 로, 쓰는 중인 글은 `draft` 로, 최종 답은 `message` 와 `task.end` 로 바꾼다. 반대로 `approval_result` 와 `cancel` 은 Hermes 의 승인 API 와 중지 API 호출이 된다. 어떤 Hermes 이벤트가 어떤 덱 메시지가 되는지는 `AGENTS.md` 의 「Hermes 브리지」 표에 있다.

브리지가 해야 할 일은 크게 세 가지다. 첫째는 접속을 유지하는 일이다. 덱 서버가 재시작하거나 망이 끊기면 웹소켓이 닫히므로 다시 접속해야 한다. `hermesbridge` 는 1초에서 시작해 실패할 때마다 두 배씩, 30초까지 기다렸다가 다시 걸고, 한 번 1분 넘게 붙어 있었으면 대기 시간을 1초로 되돌린다(`bridge.go` 의 `Run`). 다시 붙을 때마다 `status` 를 먼저 보낸다.

둘째는 메시지 번역이다. 덱의 대화 번호와 에이전트 쪽 세션이나 실행 번호를 짝지어 기억해 두어야 나중에 온 `user_message`, `approval_result`, `cancel` 을 맞는 곳으로 보낼 수 있다. 승인 선택지도 마찬가지다. 덱은 `options` 에 적은 글자를 그대로 `choice` 로 돌려주므로, 에이전트 API 가 다른 값을 원하면 브리지가 되돌려 바꾼다.

셋째는 결과를 빠짐없이 돌려주는 일이다. 대화마다 끝에 `task.end` 를 보낸다(덱 서버는 `status idle` 과 끊김 유예로 보완하지만, 그건 안전망이다). 에이전트 쪽 호출이 실패해도 `message` 로 이유를 알리고 `result` 를 `failed` 로 해서 끝낸다. 하나 조심할 점이 있다. `hermesbridge` 는 덱과 끊겨 있는 동안 생긴 메시지를 버리고 다시 보내지 않는다. 결과를 잃으면 안 되는 에이전트라면 브리지에 보낼 줄을 두고 재접속 뒤 다시 보내야 한다. `routine.run` 은 같은 실행을 덱 서버가 한 번만 저장하므로 다시 보내도 겹치지 않는다.

브리지는 덱 프로토콜을 못 하는 에이전트에게 화면 기능을 열어 주는 데도 쓸 수 있다. Hermes 는 웹소켓 메시지를 보낼 수 없지만 터미널 도구로 `curl` 은 부를 수 있다. 그래서 `hermesbridge` 는 맥 안에서만 열리는 작은 HTTP 주소(기본 `127.0.0.1:7701`, `uiapi.go`)를 두고, 거기 들어온 요청을 `ui.put` 같은 덱 메시지로 바꿔 보낸 뒤 덱의 답을 HTTP 응답으로 돌려준다. 루프백(loopback) — 같은 컴퓨터 안에서만 닿는 주소 — 에만 묶으므로 인증을 따로 두지 않는다. 환경 변수 `BRIDGE_UI_ADDR` 로 주소를 바꾸고, 빈 값을 주면 열지 않는다. 서버가 같은 컴퓨터의 Hermes 를 붙일 때는 모든 profile 이 이 주소 하나를 나눠 쓴다. default profile 은 `/ui/…`, 다른 profile 은 각자의 무작위 키로 `/h/<키>/ui/…`(키는 데이터 폴더의 `hermes-agent.<profile>.ui-key`)이다. 각 profile 폴더에 까는 스킬에 그 주소가 이미 들어 있다. 모르는 키는 404 다.

| 경로 | 덱 메시지 | 답 |
|---|---|---|
| `GET /ui/catalog` | 없음. 덱의 `GET /ui/catalog` 를 그대로 전달 | 카탈로그 원문 |
| `GET /ui` | `ui.list` | `ui.state` 의 `ui` |
| `POST /ui/put` | 본문 설계도가 `ui.put` 의 `screen`. 자리는 `?place=tab\|off\|hidden` | `{"ok", "id", "reason"}` |
| `POST /ui/remove` | 본문 `{"id"}` 가 `ui.remove` | 같은 모양 |
| `POST /ui/layout` | 본문 `{"tabs"}` 가 `ui.layout` | 같은 모양 |
| `GET /ui/screens/<id>/versions` | `ui.versions` | `{"versions": [...]}`, 그 에이전트에게 없는 화면이면 404 |
| `POST /ui/screens/<id>/revert` | 본문 `{"version"}` 이 `ui.revert` 의 `to` | `{"ok", "id", "reason"}` |
| `POST /ui/part/put` | 본문 조립 부품이 `ui.part.put` 의 `part` | 같은 모양 |
| `POST /ui/part/remove` | 본문 `{"id"}` 가 `ui.part.remove` | 같은 모양 |
| `POST /data/put` | 본문 `{"name", "data"}` 가 `data.put` | 같은 모양 |
| `POST /data/remove` | 본문 `{"name"}` 가 `data.remove` | 같은 모양 |

덱이 거절하면 HTTP 400 에 `reason` 을 그대로 실어서 에이전트가 읽고 고칠 수 있게 한다. 덱에 붙어 있지 않으면 503, 5초 안에 답이 없으면 504 다. 브리지는 요청을 하나씩 보내고 답을 받은 뒤에 다음 요청을 보낸다(`req` 를 붙이면 동시에 보내도 구별할 수 있다). 에이전트가 이 주소를 쓰는 법은 스킬(`integrations/hermes/deck-ui/SKILL.md`)로 알려 준다. 다른 에이전트를 붙일 때도 그 에이전트가 부를 수 있는 도구에 맞춰 같은 모양의 주소를 두면 된다.

## 시험해 보기

`internal/fakeagent` 가 가장 작은 참고 구현이다. 접속하는 `Dial`, 서버 메시지마다 반응하는 `Run` 이 한 파일에 있다. 작업을 받으면 도구 한 번, 승인 요청, 결과, 종료를 차례로 흉내 낸다. 직접 만드는 에이전트도 이 순서를 따라 하면 앱에서 모양이 같게 나온다. 서버 쪽 테스트(`internal/server/server_test.go`, `internal/server/ui_test.go`)는 이 가짜 에이전트를 붙여 실제 메시지를 주고받으므로 각 메시지에 덱 서버가 어떻게 답하는지 볼 때 함께 읽으면 좋다.

로컬에서 덱 서버를 띄우고 가짜 에이전트를 붙여 보는 순서는 이렇다. 서버의 기본 주소는 `127.0.0.1:7700` 이다.

```sh
go build -o bin/kyeot ./cmd/kyeot
go build -o bin/fake-agent ./cmd/fake-agent

export KYEOT_OWNER_TOKEN=$(openssl rand -hex 24)
bin/kyeot &

curl -s -H "Authorization: Bearer $KYEOT_OWNER_TOKEN" -d '{"name":"시험"}' http://127.0.0.1:7700/api/agents
AGENT_TOKEN=<받은 token> bin/fake-agent &
```

배포된 곁에서는 토큰을 직접 받지 않는다. 앱 「연결 방법 보기」의 6자리 코드를 `POST /pair/agent` 로 보내면 `{"token"}` 이 온다(10분, 한 번). 브라우저 페이지에서 부를 때만(`Origin` 머리말이 있을 때) `X-Kyeot-Pair: 1` 머리말이 필요하다. Hermes 라면 `kyeot connect <서버 주소> <코드>` 가 이 일을 대신한다.

```sh
curl -X POST 'http://<덱 주소>/pair/agent' -H 'Content-Type: application/json' -d '{"code":"123456"}'
```

앱이 보여 주는 명령은 `curl -fsSL --proto '=https' https://kyeot.app/install | sh -s -- '<서버 주소>' <코드>` 한 줄이다. 그 컴퓨터에 `kyeot` 가 없어도 된다. 설치 스크립트는 곁 서버가 아니라 이 저장소(`kyeotapp/kyeot`)의 릴리스 자산(`scripts/install.sh` 원본)으로 https 에서 받는다. 같은 와이파이에서 평문 http 응답을 바꿔치기해 스크립트째 갈아 끼우는 공격을 막으려는 것이다. 앱은 서버 주소가 `http(s)://호스트[:포트]` 꼴(호스트 이름, IPv4, `[IPv6]`. 경로, 사용자 정보, 따옴표 없음)일 때만 작은따옴표로 감싸 명령을 만들고, 아니면 「서버 주소가 올바르지 않아요」를 보인다. 스크립트도 같은 꼴과 숫자 6자리 코드만 받고(아니면 종료 코드 2), root 로는 돌지 않는다. 서버의 `GET /version`(인증 없음, 허용 망 검사는 다른 주소와 같다)에서 `{"version":"x.y.z"}` 를 받아 그 판을 깐다. 판이 `x.y.z` 숫자 꼴이 아니거나 스크립트에 박힌 `MIN_VERSION` 보다 낮으면 거부한다. 개발판 서버(`go run`, ldflags 없이 빌드)는 `/version` 이 503 이라 설치하지 않는다. `~/.local/bin/kyeot` 가 같은 판이면 받지 않는다. 아니면 `v<판>/kyeot_<os>_<arch>` 와 `checksums.txt` 와 그 서명 `checksums.txt.sig` 를 https 로만 받아, 스크립트에 박힌 공개 키로 `ssh-keygen -Y verify`(OpenSSH 8.0 이상) 한 뒤 sha256 을 맞춰 보고, 틀리면 설치하지 않고 끝난다. 맞으면 같은 폴더의 임시 이름으로 복사해 `~/.local/bin/kyeot` 로 바꿔 끼우고(맥은 격리 속성을 지운다) `kyeot connect <서버 주소> <코드>` 를 부른다. 맥과 리눅스(arm64, x86_64)만 지원한다. 서버가 판을 속이더라도 받는 파일은 릴리스의 체크섬과 맞아야 하므로 고를 수 있는 것은 `MIN_VERSION` 이상의 정식 릴리스뿐이다. 시험할 때는 세 번째 인자로 `--foreground` 를 주면 connect 에 그대로 넘기고, `KYEOT_RELEASE_BASE` 로 릴리스 주소를 바꿀 수 있다. 이 주소가 https 가 아니면 `KYEOT_INSECURE_TEST=1` 일 때만 받아 준다. 스크립트 시험은 `go test ./scripts`.

`kyeot connect <서버 주소> <코드>` 는 받은 토큰을 설정 폴더의 `kyeot/connect.json`(0600)에 저장한 뒤, 자신을 백그라운드 서비스로 등록하고 켜고 끝난다. 그래서 터미널을 닫거나 재부팅해도 연결이 유지된다. 맥에서는 `~/Library/LaunchAgents/app.kyeot.connect.plist` 를 만들어 `launchctl bootstrap` 으로 올리고, 기록은 `~/Library/Logs/kyeot-connect.log` 에 쌓인다. 비정상으로 끝나면 30초 간격으로 다시 띄운다. 리눅스에서는 `~/.config/systemd/user/kyeot-connect.service` 를 만들어 `systemctl --user enable --now` 로 켜고, 기록은 `journalctl --user -u kyeot-connect` 로 본다. 리눅스의 사용자 서비스는 로그인해 있는 동안만 돌기 때문에, 로그인하지 않아도 돌게 하려면 `loginctl enable-linger $USER` 를 한 번 실행해야 한다(관리자 권한이 필요할 수 있다). 서비스는 설치할 때의 `PATH` 에서 빈 항목과 상대 경로를 뺀 값을 물려받아 `hermes` 를 찾고, 그때 설정돼 있던 `HERMES_HOME`, `HERMES_URL`, `BRIDGE_UI_ADDR`, `KYEOT_CONNECT_FILE` 도 함께 넘겨받는다. 등록하기 전에는 언제나 옛 등록을 먼저 내리고 새 연결로 다시 올린다. brew 로 설치했다면 버전이 박힌 `Cellar/<이름>/<버전>/…` 경로 대신 업그레이드 뒤에도 남는 `opt/<이름>/…` 경로를 등록한다.

맥에서 화면에 로그인한 세션이 없으면(SSH 로만 들어온 경우) `gui/<uid>` 도메인에 올리지 못한다. 이때는 `user/<uid>` 도메인에 한 번 더 시도하고, 그렇게 켜졌다면 재부팅 뒤에는 이 계정으로 로그인해야 다시 켜질 수 있다고 알려 준다. 두 도메인 모두 실패하면 등록 파일을 지우고 오류로 끝나서 반쯤 등록된 상태를 남기지 않는다.

서비스는 셸에서 설정한 환경변수를 보지 못한다. 그래서 Hermes 의 `API_SERVER_KEY` 가 셸 변수에만 있고 `<HERMES_HOME>/.env`(기본 `~/.hermes/.env`)에 없으면 등록하지 않고 오류로 끝난다. 키를 그 파일에 넣고 다시 연결하거나 `--foreground` 로 실행한다. 이 키는 비밀값이라 누구나 읽을 수 있는 plist 나 유닛 파일에는 절대 적지 않는다. 이 확인과 Hermes 가 있는지 보는 확인, root(sudo)로 실행했는지 보는 확인은 코드를 서버에 보내기 전에 하므로 막혀도 코드를 버리지 않는다. 실행 파일 경로나 넘길 값에 개행 같은 제어문자가 섞여 있어도 등록을 거부한다.

윈도우처럼 자동 등록을 지원하지 않는 운영체제에서는 지금 터미널에서 바로 돌고, 터미널을 열어 둬야 연결이 유지된다. `go run` 이나 임시 폴더에 있는 실행 파일도 지워질 수 있어서 등록하지 않고 앞에서 돈다. 개발하거나 문제를 볼 때 등록 없이 앞에서 돌리려면 `kyeot connect --foreground <서버 주소> <코드>` 를 쓴다. 이미 서비스로 연결돼 있으면 둘이 같은 토큰으로 겹치므로 먼저 `kyeot disconnect` 하라는 오류로 끝난다. 인자 없이 `kyeot connect` 를 부르면 저장된 연결로 앞에서 돌며, 서비스가 부르는 것도 이 명령이다. 저장된 연결이 이미 있는데 코드로 다시 연결하면 「이미 <주소>에 연결돼 있다. 덮어쓸까? [y/N]」 를 묻고, y 가 아니면 코드를 서버에 보내지 않고 끝난다. 터미널이 아니면(파이프) 묻지 않고 경고 한 줄만 내고 덮어쓴다. `--yes`(또는 `KYEOT_CONNECT_YES=1`)는 묻지 않는다. 설치 스크립트는 환경변수 쪽을 넘긴다 — 옛 판 실행 파일은 모르는 인자를 쓰는 법 오류로 받지만 모르는 환경변수는 무시하기 때문이다.

연결을 끊으려면 `kyeot disconnect` 를 부른다. 서비스를 멈추고 등록 파일과 `connect.json` 을 지우며(리눅스는 그 뒤 `systemctl --user daemon-reload`), 무엇을 지웠는지 출력한다. 없는 것은 조용히 넘어간다. 서비스를 멈추는 명령이 모두 실패하면 그 오류와 함께 로그아웃하거나 재부팅하면 멈춘다고 알려 준다. 이 명령은 이 컴퓨터 쪽만 정리하므로, 서버 쪽 연결까지 끊으려면 앱에서 이 에이전트를 지운다.

가짜 에이전트 자리에 내 에이전트를 넣으면 된다. 덱 서버 주소가 기본값이 아니면 `KYEOT_URL=http://<주소>` 로 넘긴다. 앱 화면까지 함께 보며 고치려면 `sh scripts/dev.sh` 를 쓴다. 데모 데이터가 든 개발용 서버(`127.0.0.1:7796`), 데모 에이전트, Expo 웹, 아이폰 크기 Chrome 창을 한 번에 띄우고 Ctrl+C 로 모두 끈다. 이때 에이전트 토큰은 `dev/agent-token` 에 저장되어 있다.

앱이 없어도 확인할 수 있다. 소유자 토큰으로 `GET /api/tasks` 를 부르면 대화 목록이, `GET /api/agents/<에이전트 id>/ui` 를 부르면 그 에이전트의 화면과 탭이 나온다. 에이전트가 올린 설계도가 거기 보이면 연결은 끝난 것이다.
