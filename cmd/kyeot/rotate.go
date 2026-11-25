package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kyeotapp/kyeot/internal/server"
)

// uploadKeyFile is the attachment URL signing key file. Kept apart from the owner token.
func uploadKeyFile() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "uploads-key"), nil
}

// uploadKey reads the attachment signing key. If missing, it derives the key the current token used to give, so old attachment URLs keep working.
// A run given the token by environment variable (test server etc.) neither reads nor writes the key file in the config folder.
func uploadKey(token string) (key []byte, created bool, err error) {
	if getenv("OWNER_TOKEN") != "" {
		return server.LegacyUploadKey(token), false, nil
	}
	path, err := uploadKeyFile()
	if err != nil {
		return nil, false, err
	}
	key, err = readUploadKey(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return key, false, err
	}
	err = createPrivate(path, hex.EncodeToString(server.LegacyUploadKey(token))+"\n")
	if errors.Is(err, fs.ErrExist) {
		key, err = readUploadKey(path)
		return key, false, err
	}
	if err != nil {
		return nil, false, err
	}
	key, err = readUploadKey(path)
	return key, err == nil, err
}

func readUploadKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("첨부 서명 키 파일 %s 이 깨졌다. kyeot pair --rotate-uploads 로 새로 만든다 (옛 첨부 주소는 무효가 된다)", path)
	}
	return key, nil
}

// replacePrivate writes to a temp file with mode 600 and moves it into place. Replaces an existing file.
func replacePrivate(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".kyeot-secret-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func randomSecret() ([]byte, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return b, err
}

// ownerTokenFile is the file holding the current owner token. An error if the token came from an environment variable.
func ownerTokenFile() (string, error) {
	if getenv("OWNER_TOKEN") != "" {
		return "", errors.New("소유자 토큰을 KYEOT_OWNER_TOKEN 환경변수로 주고 있어 여기서 바꿀 수 없다. 그 값을 바꾸고 서버를 다시 켠다")
	}
	files, err := tokenFiles()
	if err != nil {
		return "", err
	}
	for _, f := range files {
		if _, err := os.Stat(f); err == nil {
			return f, nil
		}
	}
	return "", fmt.Errorf("소유자 토큰이 없다 (%s). 서버를 한 번 띄우면 만든다", files[0])
}

// rotateOwnerToken makes a new owner token. It fixes the attachment key first so old attachment URLs keep working.
func rotateOwnerToken() error {
	path, err := ownerTokenFile()
	if err != nil {
		return err
	}
	old, err := readOwnerToken()
	if err != nil {
		return err
	}
	if _, _, err := uploadKey(old); err != nil {
		return err
	}
	b, err := randomSecret()
	if err != nil {
		return err
	}
	return replacePrivate(path, base64.RawURLEncoding.EncodeToString(b)+"\n")
}

// rotateUploadKey makes a new attachment signing key. All old attachment URLs become invalid.
func rotateUploadKey() error {
	path, err := uploadKeyFile()
	if err != nil {
		return err
	}
	b, err := randomSecret()
	if err != nil {
		return err
	}
	return replacePrivate(path, hex.EncodeToString(b)+"\n")
}

// confirm is true only when the answer is y or yes.
func confirm(in io.Reader, out io.Writer, question string) bool {
	fmt.Fprint(out, question+" [y/N] ")
	line, _ := bufio.NewReader(in).ReadString('\n')
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes"
}

// rotateFlow is the flow of kyeot pair --rotate / --rotate-uploads. restart restarts the server service and returns false if none is registered.
// handedTo is the current owner's name if ownership was handed over in the app. Then --rotate asks once regardless of --yes.
func rotateFlow(token, uploads, yes bool, handedTo string, in io.Reader, out io.Writer, restart func(io.Writer) (bool, error)) error {
	if token && handedTo != "" && !confirm(in, out, fmt.Sprintf("주인 권한이 %[1]s에게 넘어가 있어요. 새 토큰을 만들면 이 컴퓨터에 새 주인 계정이 생기고 %[1]s은(는) 관리자가 돼요. 계속할까요?", handedTo)) {
		return errors.New("그만뒀다")
	}
	if uploads {
		if !yes && !confirm(in, out, "첨부 서명 키를 바꾸면 이미 나간 첨부 주소(앱이 받아 둔 것, 에이전트가 들고 있는 것)가 모두 열리지 않아요. 앱은 대화를 다시 열면 새 주소를 받아요. 바꿀까요?") {
			return errors.New("그만뒀다")
		}
		if err := rotateUploadKey(); err != nil {
			return err
		}
		fmt.Fprintln(out, "첨부 서명 키를 새로 만들었어요. 옛 첨부 주소는 서버를 다시 켜면 더는 열리지 않아요.")
	}
	if token {
		if err := rotateOwnerToken(); err != nil {
			return err
		}
		fmt.Fprintln(out, "소유자 토큰을 새로 만들었어요. 옛 토큰으로 연결한 폰은 서버를 다시 켠 뒤 끊기고, 새 QR 을 다시 찍어야 해요.")
		fmt.Fprintln(out, "첨부 주소는 토큰과 따로 서명해서 그대로 열려요.")
	}
	done, err := restart(out)
	if err != nil {
		return fmt.Errorf("새 값은 저장했지만 서버를 다시 켜지 못했다: %w", err)
	}
	if done {
		fmt.Fprintln(out, "서버 서비스를 다시 켰어요. 지금부터 새 값이 쓰여요.")
	} else {
		fmt.Fprintln(out, "서버가 떠 있다면 다시 켜야 반영돼요. 그 전까지는 옛 값이 계속 통해요.")
	}
	return nil
}

// restartServerService restarts the server service if one is registered.
func restartServerService(out io.Writer) (bool, error) {
	h := serverHost()
	if !registered(h) {
		return false, nil
	}
	done, err := restartKeepingEnv(h, out)
	if err == nil && !done {
		err = errors.New("서비스를 다시 켜지 못했다")
	}
	return done, err
}
