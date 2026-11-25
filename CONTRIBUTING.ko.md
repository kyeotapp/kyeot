# 기여하기

[English](CONTRIBUTING.md) · **한국어**

도와주셔서 고맙습니다. 이 글은 문제를 알리는 법, 개발용 사본을 만드는 법, 풀 리퀘스트가 머지되기 전에 갖출 것을 다룹니다. 이슈와 풀 리퀘스트는 영어나 한국어로 써도 됩니다.

## 버그 알리기, 기능 요청하기

먼저 기존 이슈를 찾아본 뒤 **Bug report** 나 **Feature request** 양식으로 새 이슈를 엽니다. 버그 양식은 `kyeot version`, 앱 판, OS, 연결한 에이전트, 재현 절차, 기대한 결과와 실제 결과를 묻습니다.

이슈는 모두에게 보입니다. 로그를 붙이기 전에 토큰, 짝짓기 코드, 서버 주소, 개인 정보를 지웁니다.

| 가진 것 | 보낼 곳 |
|---|---|
| 보안 문제 | [SECURITY.md](SECURITY.md) 에 적힌 비공개 경로. 공개 이슈에 쓰지 않습니다 |
| 설치나 사용에 관한 질문 | [Discussions](https://github.com/kyeotapp/kyeot/discussions) |
| iOS, 안드로이드 앱 자체의 문제 | [지원](https://kyeot.app/ko/support/). 앱은 이 저장소에 없습니다 |

## 개발용 사본 만들기

Go(`go.mod` 에 적힌 판, 지금은 1.27)와 git 이 필요합니다. 다른 서비스는 필요 없습니다. 시험이 서버, 가짜 에이전트, 가짜 Hermes 를 스스로 띄웁니다.

```sh
git clone https://github.com/kyeotapp/kyeot.git
cd kyeot
go test ./...
```

모든 패키지가 `ok` 면 사본이 제대로 된 것입니다. 서버를 직접 띄우는 법은 [docs/self-host.ko.md](docs/self-host.ko.md) 를 봅니다.

## 코드를 쓰기 전에

작은 수정보다 큰 일이면 먼저 이슈를 열어 방향을 맞춥니다. 시간을 들이기 전에 합의하는 편이 서로 덜 헛수고합니다. 처음이면 `good first issue` 라벨이 붙은 이슈부터 보세요.

## 풀 리퀘스트

풀 리퀘스트 하나에는 작은 변경 하나만 담습니다. 열기 전에 돌립니다.

```sh
go vet ./... && go test -race ./... && sh scripts/check-public.sh
```

`scripts/check-public.sh` 는 추적 파일에 홈 경로, 사설·Tailscale 주소, noreply 가 아닌 이메일, 비밀 키처럼 보이는 값이 있으면 실패합니다. 시험이나 문서의 정당한 예시는 그 줄 끝에 `public-ok` 를 붙이면 됩니다.

새 동작에는 시험이 있어야 합니다. 커밋 메시지는 영어로, 접두어를 붙인 한 줄로 씁니다: `fix:`, `feat:`, `refactor:`, `docs:`, `test:`, `perf:`, `chore:`, `ci:` (예: `fix: keep finished tasks from reopening on a late task.end`).

코드 주석, 시험 이름, 로그 문구는 영어로 씁니다. 사람이 읽는 문서는 영어로 쓰고 옆에 한국어 사본(`이름.ko.md`)을 둡니다.

## 기여자 라이선스 동의(CLA)

[기여자 라이선스 동의](CLA.md)에 동의해야 풀 리퀘스트를 머지할 수 있습니다. 저작권은 기여한 사람에게 남고, 이 동의는 프로젝트가 같은 코드를 둘 이상의 라이선스로 내놓을 수 있게 합니다. 풀 리퀘스트 양식의 체크 칸으로 동의하며, 한 번 하면 이후 기여에도 적용됩니다.

## 프로토콜과 호환

옛 판은 계속 돕니다. 사용자마다 자기 서버 판을 돌리지만 스토어의 앱은 모두 같습니다. 그래서 옛 앱이 새 서버와, 새 앱이 옛 서버와 이야기할 때 깨지면 안 됩니다.

같은 `protocol` 판 안에서는 필드를 더하기만 하고, 있던 필드의 뜻을 바꾸지 않습니다. 옛 앱이 따라올 수 없는 변경일 때만 `protocol` 을 올립니다. 새 기능은 `Features`(`internal/server/compat.go`)에 새 이름을 얻고, 앱은 서버가 그 이름을 알릴 때만 보여 줍니다. `Features` 에서 이름을 빼지 않습니다.

에이전트에게 새 요구를 만들지 않습니다. 에이전트는 사용자가 가져오고, 계약은 `message`, `task.end`, `approval` 셋으로 유지합니다. 해석, 정리, 표시 문구는 서버가 맡습니다.

메시지 종류와 필수 필드는 `internal/protocol` 에 있습니다. 새 메시지 종류는 `docs/connect-your-agent.md` 표에도 적습니다(시험이 검사합니다).
