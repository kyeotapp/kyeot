package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/kyeotapp/kyeot/internal/server"
)

// proxyMarkFile is the marker file written when the server sees a same-PC proxy signal.
func proxyMarkFile() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "proxy-detected"), nil
}

// runService installs or removes the server as a background service that starts at login.
//
//	kyeot service install    register and start. Creates the owner key first if missing
//	kyeot service uninstall  stop and unregister. Data stays
//	kyeot service status     check whether it is registered
func runService(h host, args []string, out io.Writer) error {
	if len(args) != 1 {
		return errors.New(tr("쓰는 법: kyeot service install | uninstall | restart | status"))
	}
	switch args[0] {
	case "install":
		return installService(h, out)
	case "uninstall":
		removed, err := removeService(h, out)
		if err == nil && !removed {
			fmt.Fprintln(out, tr("등록된 서버 서비스가 없다"))
		}
		return err
	case "restart": // the install script calls this after laying down a new version. Ends quietly if not registered
		if !registered(h) {
			fmt.Fprintln(out, tr("등록된 서버 서비스가 없다"))
			return nil
		}
		done, err := restartKeepingEnv(h, out)
		if err == nil && !done {
			return errors.New(tr("서비스를 다시 켜지 못했다"))
		}
		return err
	case "status":
		if h.proxyMark != nil {
			if mark := h.proxyMark(); mark != "" {
				fmt.Fprintf(out, tr("주의: 서버가 프록시 신호를 봤다 (%s). %s\n"), mark, server.ProxyWarning)
			}
		}
		s, ok := serviceFor(h)
		if !ok {
			fmt.Fprintln(out, tr("이 운영체제는 서비스 등록을 지원하지 않는다"))
			return nil
		}
		if registered(h) {
			fmt.Fprintln(out, tr("등록돼 있다:"), s.file)
			fmt.Fprintln(out, tr("기록:"), s.log)
		} else {
			fmt.Fprintln(out, tr("등록돼 있지 않다. 켜려면: kyeot service install"))
		}
		return nil
	}
	return fmt.Errorf(tr("모르는 명령 %q. install, uninstall, restart, status 중 하나"), args[0])
}

func installService(h host, out io.Writer) error {
	if _, skip, err := plan(h); err != nil {
		return err
	} else if skip != "" {
		return errors.New(skip)
	}
	if h.ensureToken != nil {
		created, err := h.ensureToken()
		if err != nil {
			return err
		}
		if created {
			fmt.Fprintln(out, tr("소유자 키를 만들었다"))
		}
	}
	done, err := startService(h, false, out)
	if err != nil {
		return err
	}
	if !done {
		return errors.New(tr("서비스로 등록하지 못했다"))
	}
	fmt.Fprintln(out, tr("앱 연결 QR 을 보려면: kyeot pair"))
	return nil
}

// serverHost is the host for the server service. Key creation happens on the real files.
func serverHost() host {
	h := realHostFor(serverSpec)
	h.ensureToken = func() (bool, error) {
		_, created, err := ownerToken()
		return created, err
	}
	h.proxyMark = func() string {
		path, err := proxyMarkFile()
		if err != nil {
			return ""
		}
		mark, _ := server.ReadProxyMark(path)
		return mark
	}
	return h
}
