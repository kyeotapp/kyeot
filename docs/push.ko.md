# 휴대폰 알림 (APNs)

[English](push.md) · **한국어**

곁 서버는 앱이 닫혀 있을 때 아이폰으로 알림을 보낼 수 있다. 에이전트가 작업을 끝냈을 때(성공이나 실패)와 승인이나 선택을 물을 때 보낸다. 루틴 실행 결과도 같다. 앱이 열려 있으면 화면이 바로 보여 주므로 보내지 않는다. 앱이 백그라운드로 가면 iOS 가 웹소켓을 끊기 때문에, 서버는 연결된 앱이 하나도 없는 것을 "앱이 닫혀 있다"로 본다.

알림은 Apple 의 푸시 서버인 APNs(Apple Push Notification service)를 거친다. 서버가 APNs 에 보낼 자격을 증명하려면 Apple Developer Program 에서 받은 키가 있어야 한다. 키 설정이 없으면 서버는 알림 기능을 꺼 두고 다른 일은 그대로 한다. 앱이 기기를 등록하는 `POST /api/devices` 도 계속 받아 둔다.

## 가입한 뒤 할 일

Apple Developer 사이트의 Certificates, Identifiers & Profiles 에서 Keys 로 들어가 새 키를 만들고 Apple Push Notifications service (APNs) 를 체크한다. 내려받은 `AuthKey_<키ID>.p8` 은 한 번만 받을 수 있으니 잃어버리지 않게 둔다. 키 ID 는 키 상세 화면에, 팀 ID 는 Membership 화면에 있다. 번들 ID 가 `app.kyeot` 가 아니면 그 값도 적어 둔다.

키 파일은 서버 맥의 설정 폴더에 두고 본인만 읽게 한다.

```sh
mkdir -p ~/.config/kyeot
mv ~/Downloads/AuthKey_*.p8 ~/.config/kyeot/apns.p8
chmod 600 ~/.config/kyeot/apns.p8
```

키 ID 와 팀 ID 는 `~/.config/kyeot/apns.env` 에 적는다. 이 파일도 권한을 600 으로 둔다.

```sh
cat > ~/.config/kyeot/apns.env <<'EOF'
KYEOT_APNS_KEY_ID=ABCDE12345
KYEOT_APNS_TEAM_ID=FGHIJ67890
# 번들 ID 가 다를 때만
# KYEOT_APNS_TOPIC=app.kyeot
EOF
chmod 600 ~/.config/kyeot/apns.env
```

`kyeot` 은 이 두 파일을 스스로 읽지 않는다. 띄울 때 `KYEOT_APNS_KEY`(키 파일 경로)와 `KYEOT_APNS_KEY_ID`, `KYEOT_APNS_TEAM_ID` 를 환경변수로 넘긴다. 셋이 다 있어야 켜지고, 일부만 있으면 무엇이 빠졌는지 `push off:` 줄로 남긴다. `kyeot service install` 은 그때 셸에 있는 이 변수들을 서비스 파일에 적으므로 아래처럼 한 번 등록하면 된다.

```sh
set -a; . ~/.config/kyeot/apns.env; set +a
KYEOT_APNS_KEY=~/.config/kyeot/apns.p8 kyeot service install
```

서버를 다시 띄우고 로그에 `push on: APNs (topic …)` 한 줄이 나오면 된 것이다. 키 파일을 못 읽거나 형식이 틀리면 `push off:` 로 시작하는 줄이 나오고 서버는 알림 없이 계속 돈다. 키 내용과 서명한 토큰은 로그에 남기지 않고, 기기 토큰은 앞 8자만 남긴다.

```sh
launchctl kickstart -k gui/$(id -u)/app.kyeot.server
```

## 기기마다 다른 APNs 서버

Xcode 에서 직접 설치한 개발 빌드는 sandbox 서버로, TestFlight 나 App Store 빌드는 production 서버로 받아야 한다. 앱이 기기를 등록할 때 `env` 로 어느 쪽인지 알려 주고, 서버는 기기마다 저장된 값으로 고른다. APNs 가 410 이나 `Unregistered` 로 답하면 서버는 그 기기를 지운다. 앱이 다시 열리면 새로 등록한다. `BadDeviceToken` 은 환경을 잘못 고른 때도 같은 답이 오므로 다른 환경으로 한 번 더 보내 보고, 그쪽에서도 같은 답일 때만 지운다. 그쪽으로 가면 알림은 간 것이고 `ErrWrongEnv` 로 알린다.

일시 오류는 다시 보낸다. 429 와 5xx, 망 오류는 0.5초와 2초 뒤에 두 번 더 보내고, 서명 토큰(JWT)이 만료됐거나 틀렸다는 403(`ExpiredProviderToken`, `InvalidProviderToken`)이면 토큰을 새로 서명해 한 번 더 보낸다. 서명 토큰은 벽시계로 50분마다 바꾼다. 맥이 잠든 사이에도 시간이 흐른 것으로 친다.

## 앱 쪽

앱은 `KYEOT_PUSH=1` 로 빌드해야 알림 권한을 묻고 기기를 등록한다.

스위치를 따로 둔 까닭은 무료 Apple ID(Personal Team) 때문이다. 알림을 받으려면 앱에 `aps-environment` 권한이 들어가야 하는데, 무료 팀은 이 권한으로 서명하지 못해 설치 자체가 깨진다. 그리고 `expo-notifications` 는 설치만 돼 있어도 이 권한을 넣는다. 그래서 `app/app.config.js` 가 스위치가 꺼져 있으면 권한을 빼고, 켜져 있으면 플러그인을 넣는다. 꺼진 앱은 알림 코드를 아예 부르지 않는다(`app/src/lib/push.ts`).

가입한 뒤에는 `app/app.json` 의 `ios.appleTeamId` 를 유료 팀 ID 로 바꾸고 이렇게 설치한다. Xcode 에서 설치한 빌드는 sandbox 서버를 쓴다.

```sh
KYEOT_PUSH=1 sh scripts/ios-install.sh
```

TestFlight 나 App Store 로 낼 빌드는 `KYEOT_PUSH_ENV=production` 을 함께 준다. 앱은 연결될 때마다 알림 권한을 확인하고 기기 토큰을 `POST /api/devices` 로 맡긴다. 알림을 누르면 그 대화가 열린다. 앱이 꺼져 있다가 알림으로 열린 경우도 같다.

스위치가 제대로 먹는지는 prebuild 결과로 확인한다. 꺼져 있으면 아래 명령이 0 을 낸다.

```sh
cd app && npx expo prebuild -p ios && grep -c aps-environment ios/kyeot/kyeot.entitlements
```
