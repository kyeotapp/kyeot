package main

// en is the English dictionary for tr. Keys are the Korean source text verbatim. App button names use the same words as app/src/lib/i18n-en.ts.
var en = map[string]string{
	// kyeot help, unknown command
	`쓰는 법: kyeot [명령]

  (명령 없음)                     서버를 띄운다
  service install|uninstall|restart|status
                                  로그인 때 저절로 뜨는 서비스로 등록, 해제, 재시작, 상태 확인
  pair [-host 주소] [-copy]       폰 앱이 찍을 연결 QR 을 그린다. 주소는 이름이나 https://… 전체
  pair --rotate                   소유자 토큰을 새로 만들고 새 QR 을 그린다. 서버 서비스는 다시 켠다
  pair --rotate-uploads [--yes]   첨부 서명 키를 새로 만든다. 이미 나간 첨부 주소는 무효가 된다
  connect <서버 주소> <코드>      이 컴퓨터의 Hermes 를 다른 컴퓨터의 서버에 붙인다
  disconnect                      connect 로 붙인 것을 뗀다
  uninstall [--purge]             서비스를 내리고 실행 파일을 지운다. --purge 면 키와 대화 기록까지
  backup <파일>                   대화 기록, 첨부, 키를 파일 하나로 백업한다. 서버가 켜져 있어도 된다
  restore [--yes] <파일>          백업으로 되살린다. 서버를 끄고 한다. 지금 데이터는 먼저 백업해 둔다
  update [--check]                최신 릴리스로 올리고 서비스를 다시 켠다. --check 는 확인만
  version                         판을 출력한다
  help                            이 도움말
`: `Usage: kyeot [command]

  (no command)                    Start the server
  service install|uninstall|restart|status
                                  Register, remove, restart or check the service that starts at login
  pair [-host address] [-copy]    Draw the connection QR for the phone app. Address is a name or a full https://…
  pair --rotate                   Make a new owner token and draw a new QR. Restarts the server service
  pair --rotate-uploads [--yes]   Make a new attachment signing key. Attachment links already sent stop working
  connect <server address> <code> Connect this computer's Hermes to a server on another computer
  disconnect                      Remove what connect set up
  uninstall [--purge]             Stop the service and delete the binary. --purge also deletes keys and chat history
  backup <file>                   Back up chat history, attachments and keys into one file. The server may keep running
  restore [--yes] <file>          Restore from a backup. Stop the server first. Current data is backed up first
  version                         Print the version
  help                            This help
`,
	"모르는 명령: %s\n\n": "Unknown command: %s\n\n",

	// kyeot backup, restore
	"다른 곁 서버나 kyeot restore 가 이 데이터를 쓰고 있다: %s":                                     "Another kyeot server or kyeot restore is using this data: %s",
	"다른 곁 서버나 kyeot restore 가 이 데이터를 쓰고 있다":                                         "Another kyeot server or kyeot restore is using this data",
	"서버가 서비스로 등록돼 있다. kyeot service uninstall 로 먼저 내린다":                             "The server is registered as a service. Remove it first with kyeot service uninstall",
	"서버가 %s 에서 돌고 있다. 먼저 끈다":                                                        "A server is running on %s. Stop it first",
	"쓰는 법: kyeot backup <파일>":                                                       "Usage: kyeot backup <file>",
	"%s 가 이미 있다. 다른 이름을 준다":                                                         "%s already exists. Choose another name",
	"백업할 데이터가 없다: %w":                                                               "No data to back up: %w",
	"백업했다: %s (DB 구조 %d, 파일 %d개, 키 %d개)\n":                                          "Backed up: %s (DB schema %d, %d files, %d keys)\n",
	"이 파일로 앱 연결 없이 서버에 들어갈 수 있다. 남에게 주지 않는다":                                        "This file lets anyone into the server without pairing. Don't share it",
	"곁 백업 파일이 아니다: %w":                                                              "Not a kyeot backup file: %w",
	"백업 파일 안에 받을 수 없는 항목이 있다: %q":                                                   "The backup contains an entry that can't be restored: %q",
	"곁 백업 파일이 아니다(목록이나 DB 가 없다)":                                                    "Not a kyeot backup file (no manifest or DB)",
	"더 새 판 곁이 만든 백업 형식이다. kyeot 을 업데이트한 뒤 다시 한다":                                    "The backup format is from a newer kyeot. Update kyeot and try again",
	"묻지 않고 바꾼다":                                                                     "Replace without asking",
	"쓰는 법: kyeot restore [--yes] <파일>":                                              "Usage: kyeot restore [--yes] <file>",
	"되살리지 않았다: %s":                                                                  "Didn't restore: %s",
	"백업 속 DB 를 읽지 못했다: %w":                                                          "Couldn't read the DB in the backup: %w",
	"이 백업은 더 새 판 곁이 만들었다(구조 %d, 이 판은 %d). kyeot 을 업데이트한 뒤 다시 한다":                    "This backup was made by a newer kyeot (schema %d, this version %d). Update kyeot and try again",
	"백업: %s 에 판 %s 가 만듦, DB 구조 %d, 키 %d개\n":                                         "Backup: made %s by version %s, DB schema %d, %d keys\n",
	"지금 데이터를 바꿀지 물어볼 수 없다. --yes 를 붙여 다시 한다":                                        "Can't ask whether to replace the current data. Run again with --yes",
	"이 컴퓨터의 곁 데이터를 백업 내용으로 바꿀까요? 지금 데이터는 먼저 따로 백업해 둔다":                              "Replace kyeot data on this computer with the backup? Current data is backed up first",
	"그만뒀다. 아무것도 바꾸지 않았다":                                                            "Cancelled. Nothing was changed",
	"지금 데이터를 백업하지 못해 바꾸지 않았다: %w":                                                   "Couldn't back up the current data, so nothing was replaced: %w",
	"지금 데이터를 먼저 백업했다: %s\n":                                                         "Backed up the current data first: %s\n",
	"바꾸다 멈췄다. 앞의 백업 파일로 다시 되살린다: %w":                                                "Stopped partway. Restore again from the backup file above: %w",
	"되살렸다. 서버를 띄우면(kyeot service install 또는 kyeot) 옛 구조는 새 구조로 바뀌고 그 전 DB 를 따로 남긴다": "Restored. When the server starts (kyeot service install or kyeot), an older schema is upgraded and the previous DB is kept",
	"백업에 소유자 토큰이 없어 지금 토큰을 그대로 둔다. 폰 앱은 kyeot pair 로 다시 잇는다":                        "The backup has no owner token, so the current one stays. Reconnect the phone app with kyeot pair",

	// kyeot pair
	"폰이 닿을 주소. 이름(x.example.com)이면 scheme 과 포트를 붙이고, 전체 주소(https://x.example.com)면 그대로 쓴다. 비우면 KYEOT_PUBLIC_URL, Tailscale, 사설망 순": "Address the phone can reach. A name (x.example.com) gets a scheme and port; a full address (https://x.example.com) is used as is. Empty means KYEOT_PUBLIC_URL, then Tailscale, then the local network",
	"QR 대신 소유자 토큰을 클립보드에 복사한다 (맥 pbcopy. 안 되면 화면의 토큰을 쓴다)":                                                                         "Copy the owner token to the clipboard instead of drawing a QR (macOS pbcopy; if that fails, use the token on screen)",
	"소유자 토큰을 새로 만든다. 옛 토큰으로 연결한 폰은 새 QR 을 찍어야 한다":                                                                                  "Make a new owner token. Phones connected with the old token must scan the new QR",
	"첨부 서명 키를 새로 만든다. 이미 나간 첨부 주소가 모두 무효가 된다":                                                                                      "Make a new attachment signing key. All attachment links already sent stop working",
	"--rotate-uploads 를 묻지 않고 한다": "Do --rotate-uploads without asking",
	"모르는 인수 %q":                   "Unknown argument %q",
	"새 QR:":                       "New QR:",
	"클립보드에 복사하지 못했어요. 토큰은 kyeot pair 화면에 있어요.":             "Couldn't copy to the clipboard. The token is on the kyeot pair screen.",
	"소유자 토큰을 클립보드에 복사했어요. 앱 「직접 입력하기」의 소유자 토큰 칸에 붙여 넣으세요.": "Copied the owner token to the clipboard. Paste it into the Owner token field under “Enter manually” in the app.",
	"서버 주소:": "Server address:",
	"곁 앱의 「QR 찍기」로 찍으세요. 다시 보려면 kyeot pair": "Scan this with “Scan QR” in the Kyeot app. To show it again: kyeot pair",
	"직접 입력할 때":    "To enter manually",
	"  서버 주소    ": "  Server address",
	"  소유자 토큰  ":  "  Owner token  ",
	"(터미널이 아니라 가렸어요. 터미널에서 kyeot pair 로 확인하세요)":                                "(hidden because this is not a terminal; run kyeot pair in a terminal to see it)",
	"주의: 서버가 이 기기 안(%s)에서만 받고 있어 폰이 닿지 않는다. KYEOT_ADDR=0.0.0.0:%s 로 띄워야 한다.\n": "Note: the server only listens inside this computer (%s), so the phone can't reach it. Start it with KYEOT_ADDR=0.0.0.0:%s.\n",
	"위 QR 과 토큰은 이 서버의 주인 키예요. 다른 사람에게 보여 주지 마세요.":                              "The QR above and this token are the owner key to this server. Don't show them to anyone else.",

	// kyeot service
	"쓰는 법: kyeot service install | uninstall | restart | status": "usage: kyeot service install | uninstall | restart | status",
	"등록된 서버 서비스가 없다":                                             "No server service is registered",
	"서비스를 다시 켜지 못했다":                                             "Couldn't restart the service",
	"주의: 서버가 프록시 신호를 봤다 (%s). %s\n":                              "Note: the server saw signs of a proxy (%s). %s\n",
	"이 운영체제는 서비스 등록을 지원하지 않는다":                                   "This operating system doesn't support service registration",
	"등록돼 있다:": "Registered:",
	"기록:":     "Log:",
	"등록돼 있지 않다. 켜려면: kyeot service install":               "Not registered. To turn it on: kyeot service install",
	"모르는 명령 %q. install, uninstall, restart, status 중 하나": "unknown command %q. Use one of install, uninstall, restart, status",
	"소유자 키를 만들었다":                                         "Created the owner key",
	"서비스로 등록하지 못했다":                                       "Couldn't register the service",
	"앱 연결 QR 을 보려면: kyeot pair":                           "To show the app connection QR: kyeot pair",
	"연결했다. 백그라운드 서비스로 돌기 때문에 터미널을 닫거나 재부팅해도 계속 연결된다":                                   "Connected. It runs as a background service, so it stays connected after you close the terminal or restart",
	"서버를 켰다. 백그라운드 서비스로 돌기 때문에 터미널을 닫거나 재부팅해도 계속 뜬다":                                   "Server started. It runs as a background service, so it keeps running after you close the terminal or restart",
	"화면 로그인 세션이 없어 사용자 도메인(user)에 등록했다. 터미널을 닫아도 돌지만, 재부팅 뒤에는 이 계정으로 로그인해야 다시 켜질 수 있다": "No graphical login session, so it was registered in the user domain. It keeps running after you close the terminal, but after a restart it may need you to log in to this account to start again",
	"로그인하지 않은 동안에도 돌게 하려면 `loginctl enable-linger $USER` 를 한 번 실행한다(관리자 권한이 필요할 수 있다)": "To keep it running while you're logged out, run `loginctl enable-linger $USER` once (may need admin rights)",
	"이 운영체제는 자동 등록을 지원하지 않는다. 터미널을 열어 둬야 연결이 유지된다":                                     "This operating system doesn't support automatic registration. Keep the terminal open to stay connected",
	"실행 파일이나 홈 폴더를 찾지 못해 등록하지 않는다. 터미널을 열어 둬야 연결이 유지된다":                                "Couldn't find the binary or home folder, so it isn't registered. Keep the terminal open to stay connected",
	"주의: 실행 파일이 임시 폴더에 있다(%s). 지워질 수 있어 등록하지 않고 여기서 돌린다. 설치한 kyeot 로 다시 연결한다":          "Note: the binary is in a temporary folder (%s). It may be deleted, so it runs here without registering. Connect again with the installed kyeot",
	"관리자 권한(sudo) 없이 실행한다. root 로는 서비스를 등록하지 않는다":                                      "Run without admin rights (sudo). The service isn't registered as root",
	"실행 파일 경로에 개행이나 제어문자가 있어 서비스로 등록하지 않는다":                                            "The binary path contains a newline or control character, so the service isn't registered",
	"%s 에 개행이나 제어문자가 있어 서비스로 등록하지 않는다":                                                 "%s contains a newline or control character, so the service isn't registered",
	"서비스를 켜지 못했다: %w": "Couldn't start the service: %w",
	"서비스를 다시 켜지 못했다: %w\n등록 파일은 남겼다(%s). 다음 로그인 때 저절로 뜬다. 지금 켜려면 잠시 뒤 다시: kyeot service restart": "Couldn't restart the service: %w\nThe registration file was kept (%s). It starts by itself at next login. To start it now, wait a moment and run: kyeot service restart",
	"끄려면:": "To turn off:",
	"서비스를 멈추지 못했다: %s. 로그아웃하거나 재부팅하면 멈춘다\n": "Couldn't stop the service: %s. It stops when you log out or restart\n",
	"등록 파일은 지웠다:":   "Deleted the registration file:",
	"서비스를 멈추고 지웠다:": "Stopped and removed the service:",
	"주인 권한이 %s에게 넘어가 있어요. 넘긴 뒤라 이 토큰으로는 들어갈 수 없어요. 새로 주인이 되려면 kyeot pair --rotate\n": "The owner role was handed over to %s, so this token no longer signs anyone in. To become the owner again: kyeot pair --rotate\n",
	// kyeot update
	"쓰는 법: kyeot update [--check]": "usage: kyeot update [--check]",
	"곁 서버는 지금 맥과 리눅스에서만 돈다":        "The Kyeot server runs only on macOS and Linux for now",
	"이 kyeot 는 릴리스 판이 아니다(%s). 설치 스크립트로 깐다: curl -fsSL https://kyeot.app/install | sh": "This kyeot is not a release build (%s). Install with the install script: curl -fsSL https://kyeot.app/install | sh",
	"릴리스 주소는 https 여야 한다: %s":                           "The release address must be https: %s",
	"최신 판을 알 수 없다: %w":                                  "Can't find the latest version: %w",
	"릴리스의 VERSION 이 올바르지 않다: %q":                        "The release VERSION is invalid: %q",
	"이미 최신이다: %s\n":                                     "Already up to date: %s\n",
	"새 판이 있다: %s → %s. 올리려면: kyeot update\n":            "A new version is available: %s → %s. To update: kyeot update\n",
	"이 실행 파일(%s)은 update 로 바꾸지 않는다. 설치한 방법으로 올린다":       "kyeot update does not replace this executable (%s). Update it the way you installed it",
	"kyeot %s 을 받는다 (%s/%s)\n":                          "Downloading kyeot %s (%s/%s)\n",
	"릴리스에 서명(checksums.txt.sig)이 없다. 설치하지 않는다":          "The release has no signature (checksums.txt.sig). Not installing",
	"릴리스 서명이 맞지 않는다. 파일이 바꿔치기됐을 수 있다. 설치하지 않는다":         "The release signature doesn't match. The files may have been swapped. Not installing",
	"파일이 변조됐거나 손상됐다. 설치하지 않는다":                          "The file was tampered with or is damaged. Not installing",
	"%s 에 쓸 수 없다: %w":                                   "Can't write to %s: %w",
	"받은 파일이 %s 판으로 실행되지 않는다. 설치하지 않는다":                  "The downloaded file doesn't run as version %s. Not installing",
	"이전 판을 남기지 못했다. 설치는 계속한다":                           "Couldn't keep the previous version. Installing anyway",
	"이전 판은 %s 에 남겼다\n":                                  "The previous version is kept at %s\n",
	"설치했다: %s (%s → %s)\n":                              "Installed: %s (%s → %s)\n",
	"서비스 없이 떠 있는 서버는 아직 옛 판이다. 끄고 다시 띄운다:":              "A server running without a service is still the old version. Stop it and start it again:",
	"문제가 생기면 kyeot.prev 로 되돌린다. 서비스 설정을 지키는 순서는 여기 있다:": "If something goes wrong, roll back to kyeot.prev. The steps that keep your service settings are here:",
}
