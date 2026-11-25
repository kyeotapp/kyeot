package server

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/kyeotapp/kyeot/internal/store"
)

// ErrLocalAgentUnlinked means the user unlinked the in-server agent. Do not reattach it.
var ErrLocalAgentUnlinked = errors.New("사용자가 연결을 해제한 에이전트다")

// LocalAgent issues a fresh agent token for the in-server adapter on every start.
// idPath holds only the agent id (not secret, 0600). ErrLocalAgentUnlinked if the agent was unlinked — delete that file to reattach.
// If it holds a token left by an older version, it is rewritten as that agent's id.
func (s *Server) LocalAgent(name, idPath string) (string, error) {
	if s.store.HandedOver() { // this computer's account is not the owner (after handover, before rotate)
		return "", fmt.Errorf("%w: 주인 권한이 넘어간 서버라 이 컴퓨터의 Hermes 를 붙이지 않는다", ErrLocalAgentUnlinked)
	}
	id, err := s.localAgentID(idPath)
	if err != nil {
		return "", err
	}
	if id != "" {
		removed, err := s.store.AgentRemoved(id)
		switch {
		case err == nil && removed:
			return "", fmt.Errorf("%w. 다시 붙이려면 %s 를 지운다", ErrLocalAgentUnlinked, idPath)
		case err == nil:
			return s.hub.RotateAgentToken(s.store.OwnerID(), id)
		case !errors.Is(err, store.ErrNotFound):
			return "", err
		}
	}
	a, token, err := s.hub.CreateAgent(s.store.OwnerID(), name)
	if err != nil {
		return "", err
	}
	if err := writePrivate(idPath, a.ID+"\n"); err != nil {
		s.hub.UnlinkAgent(s.store.OwnerID(), a.ID)
		return "", err
	}
	return token, nil
}

// localAgentID reads the agent id from the file. "" if absent. If it holds an old-version token, rewrites it as the id.
func (s *Server) localAgentID(path string) (string, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s: 일반 파일이 아니다(심볼릭 링크 등)", path)
	}
	b, err := readNoFollow(path)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", nil
	}
	if u, err := uuid.Parse(v); err == nil {
		return u.String(), nil
	}
	a, err := s.store.AgentByToken(v) // token left by an older version
	if errors.Is(err, store.ErrNotFound) {
		return "", fmt.Errorf("%s: id 도 쓸 수 있는 토큰도 아니다(해제된 에이전트의 옛 토큰일 수 있다). 새로 붙이려면 이 파일을 지운다", path)
	}
	if err != nil {
		return "", err
	}
	return a.ID, writePrivate(path, a.ID+"\n")
}

// readNoFollow opens without following links and reads. If permissions are too wide, narrows to 0600 on the opened file.
func readNoFollow(path string) ([]byte, error) {
	f, err := openNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: 일반 파일이 아니다", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		log.Printf("%s permissions too wide, fixing to 0600", path)
		if err := f.Chmod(0o600); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(f)
}

// writePrivate writes to a randomly named temp file (0600) in the same folder and swaps it in. Does not follow existing files or links.
func writePrivate(path, data string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".kyeot-agent-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
