package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Release signing keys. Must match RELEASE_PUBKEYS in scripts/install.sh (TestUpdateKeysMatchInstallScript).
var releasePubkeys = []string{
	"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILt18cm4BlLU7vnXLkrG9qQvvLbX+tJjDDV2lYNyFto2",
	"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPSO8iev2GSsKyIutJPHL9rHbejefUUC6G2ZTRLwsTHZ",
}

const (
	defaultReleaseBase = "https://github.com/kyeotapp/kyeot/releases/download"
	releaseNamespace   = "kyeot-release"
	rollbackDoc        = "https://github.com/kyeotapp/kyeot/blob/main/docs/self-host.md"
	maxAssetBytes      = 256 << 20
	maxSmallBytes      = 64 << 10
)

var semver = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// updatePlan holds what kyeot update touches. Tests swap in fakes.
type updatePlan struct {
	current  string // running version
	exe      string // file to replace
	goos     string
	goarch   string
	base     string // release download base, like KYEOT_RELEASE_BASE
	insecure bool   // KYEOT_INSECURE_TEST=1: allow http base and a test key
	keys     []string
	client   *http.Client
	restart  func(out io.Writer) (restarted bool, err error) // restart registered services, keeping their settings
	running  func() string                                   // non-empty if a server runs without a service
}

func realUpdatePlan() updatePlan {
	p := updatePlan{current: version, goos: runtime.GOOS, goarch: runtime.GOARCH, base: defaultReleaseBase, keys: releasePubkeys}
	if exe, err := os.Executable(); err == nil {
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		}
		p.exe = exe
	}
	if b := os.Getenv("KYEOT_RELEASE_BASE"); b != "" {
		p.base = b
	}
	if os.Getenv("KYEOT_INSECURE_TEST") == "1" {
		p.insecure = true
		if k := os.Getenv("KYEOT_TEST_RELEASE_PUBKEY"); k != "" {
			p.keys = strings.Split(strings.TrimSpace(k), "\n")
		}
	}
	p.client = releaseClient(p.insecure)
	p.restart = restartRegisteredServices
	addr := env("ADDR", defaultAddr)
	p.running = func() string {
		if registered(serverHost()) {
			return ""
		}
		return serverRunning(serverHost(), addr)
	}
	return p
}

// releaseClient refuses redirects that leave https, like curl --proto '=https' in install.sh.
func releaseClient(insecure bool) *http.Client {
	return &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if r.URL.Scheme != "https" && !insecure {
			return errors.New("redirect to non-https " + r.URL.Redacted())
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}}
}

// restartRegisteredServices restarts the server and connect services that are registered.
func restartRegisteredServices(out io.Writer) (bool, error) {
	restarted := false
	var errs []error
	for _, h := range []host{serverHost(), realHost()} {
		if !registered(h) {
			continue
		}
		done, err := restartKeepingEnv(h, out)
		if err == nil && !done {
			err = errors.New(tr("서비스를 다시 켜지 못했다"))
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		restarted = true
	}
	return restarted, errors.Join(errs...)
}

func runUpdate(p updatePlan, args []string, out io.Writer) error {
	check := false
	for _, a := range args {
		switch a {
		case "--check", "-check":
			check = true
		default:
			return errors.New(tr("쓰는 법: kyeot update [--check]"))
		}
	}
	if p.goos != "darwin" && p.goos != "linux" {
		return errors.New(tr("곁 서버는 지금 맥과 리눅스에서만 돈다"))
	}
	if !semver.MatchString(p.current) {
		return fmt.Errorf(tr("이 kyeot 는 릴리스 판이 아니다(%s). 설치 스크립트로 깐다: curl -fsSL https://kyeot.app/install | sh"), p.current)
	}
	base := strings.TrimSuffix(p.base, "/")
	switch {
	case strings.HasPrefix(base, "https://"):
	case strings.HasPrefix(base, "http://") && p.insecure:
	default:
		return fmt.Errorf(tr("릴리스 주소는 https 여야 한다: %s"), base)
	}
	ctx := context.Background()
	latestURL := strings.TrimSuffix(base, "/download") + "/latest/download"
	vb, err := fetch(ctx, p.client, latestURL+"/VERSION", maxSmallBytes)
	if err != nil {
		return fmt.Errorf(tr("최신 판을 알 수 없다: %w"), err)
	}
	latest := strings.TrimSpace(string(vb))
	if !semver.MatchString(latest) {
		return fmt.Errorf(tr("릴리스의 VERSION 이 올바르지 않다: %q"), latest)
	}
	if !versionLess(p.current, latest) {
		fmt.Fprintf(out, tr("이미 최신이다: %s\n"), p.current)
		return nil
	}
	if check {
		fmt.Fprintf(out, tr("새 판이 있다: %s → %s. 올리려면: kyeot update\n"), p.current, latest)
		return nil
	}
	if p.exe == "" || strings.Contains(p.exe, "/Cellar/") {
		return fmt.Errorf(tr("이 실행 파일(%s)은 update 로 바꾸지 않는다. 설치한 방법으로 올린다"), p.exe)
	}

	fmt.Fprintf(out, tr("kyeot %s 을 받는다 (%s/%s)\n"), latest, p.goos, p.goarch)
	rel := base + "/v" + latest
	name := "kyeot_" + p.goos + "_" + p.goarch
	sums, err := fetch(ctx, p.client, rel+"/checksums.txt", maxSmallBytes)
	if err != nil {
		return err
	}
	sig, err := fetch(ctx, p.client, rel+"/checksums.txt.sig", maxSmallBytes)
	if err != nil {
		return errors.New(tr("릴리스에 서명(checksums.txt.sig)이 없다. 설치하지 않는다"))
	}
	if err := verifySSHSig(sums, sig, p.keys, releaseNamespace); err != nil {
		return errors.New(tr("릴리스 서명이 맞지 않는다. 파일이 바꿔치기됐을 수 있다. 설치하지 않는다"))
	}
	want := checksumFor(sums, name)
	if want == "" {
		return errors.New(tr("파일이 변조됐거나 손상됐다. 설치하지 않는다"))
	}

	dir := filepath.Dir(p.exe)
	tmp, err := os.CreateTemp(dir, ".kyeot.new.*")
	if err != nil {
		return fmt.Errorf(tr("%s 에 쓸 수 없다: %w"), dir, err)
	}
	defer os.Remove(tmp.Name())
	if err := download(ctx, p.client, rel+"/"+name, tmp, want); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if got, err := exec.Command(tmp.Name(), "version").Output(); err != nil || strings.TrimSpace(string(got)) != latest {
		return fmt.Errorf(tr("받은 파일이 %s 판으로 실행되지 않는다. 설치하지 않는다"), latest)
	}

	prev := filepath.Join(dir, "kyeot.prev")
	if err := copyExecutable(p.exe, prev); err != nil {
		fmt.Fprintln(out, tr("이전 판을 남기지 못했다. 설치는 계속한다"))
	} else {
		fmt.Fprintf(out, tr("이전 판은 %s 에 남겼다\n"), prev)
	}
	if err := os.Rename(tmp.Name(), p.exe); err != nil {
		return err
	}
	fmt.Fprintf(out, tr("설치했다: %s (%s → %s)\n"), p.exe, p.current, latest)

	restarted, err := p.restart(out)
	if !restarted && err == nil {
		if p.running() != "" {
			fmt.Fprintln(out, tr("서비스 없이 떠 있는 서버는 아직 옛 판이다. 끄고 다시 띄운다:"), p.exe)
		}
	}
	fmt.Fprintln(out, tr("문제가 생기면 kyeot.prev 로 되돌린다. 서비스 설정을 지키는 순서는 여기 있다:"), rollbackDoc)
	return err
}

// versionLess reports a < b for x.y.z strings.
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range 3 {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return false
}

func fetch(ctx context.Context, c *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: too large", url)
	}
	return b, nil
}

// download writes url to w and checks its sha256 against want (hex).
func download(ctx context.Context, c *http.Client, url string, w io.Writer, want string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(resp.Body, maxAssetBytes+1))
	if err != nil {
		return err
	}
	if n > maxAssetBytes || hex.EncodeToString(h.Sum(nil)) != want {
		return errors.New(tr("파일이 변조됐거나 손상됐다. 설치하지 않는다"))
	}
	return nil
}

// checksumFor finds name's hash in sha256sum output ("<hex>  name" or "<hex> *name").
func checksumFor(sums []byte, name string) string {
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && (f[1] == name || f[1] == "*"+name) && len(f[0]) == 64 {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

// copyExecutable copies src to dst (0755) through a temp file so dst is never half written.
func copyExecutable(src, dst string) error {
	if st, err := os.Stat(dst); err == nil && st.IsDir() {
		return errors.New("kyeot.prev is a folder")
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".kyeot.prev.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// verifySSHSig checks an armored `ssh-keygen -Y sign` signature (SSHSIG, ed25519) over msg
// made by one of keys ("ssh-ed25519 <base64>") in namespace.
func verifySSHSig(msg, armored []byte, keys []string, namespace string) error {
	block, _ := pem.Decode(armored)
	if block == nil || block.Type != "SSH SIGNATURE" {
		return errors.New("not an SSH signature")
	}
	r := sshReader{b: block.Bytes}
	if magic := r.raw(6); string(magic) != "SSHSIG" {
		return errors.New("bad magic")
	}
	if v := r.u32(); v != 1 {
		return errors.New("unsupported version")
	}
	pub, ns, reserved, hashAlg, sigBlob := r.str(), r.str(), r.str(), r.str(), r.str()
	if r.err != nil || len(r.b) != 0 {
		return errors.New("malformed signature")
	}
	if string(ns) != namespace {
		return errors.New("wrong namespace")
	}
	trusted := false
	for _, k := range keys {
		f := strings.Fields(k)
		if len(f) < 2 || f[0] != "ssh-ed25519" {
			continue
		}
		if blob, err := base64.StdEncoding.DecodeString(f[1]); err == nil && bytes.Equal(blob, pub) {
			trusted = true
		}
	}
	if !trusted {
		return errors.New("unknown key")
	}
	pr := sshReader{b: pub}
	if string(pr.str()) != "ssh-ed25519" {
		return errors.New("not ed25519")
	}
	key := pr.str()
	sr := sshReader{b: sigBlob}
	if string(sr.str()) != "ssh-ed25519" {
		return errors.New("not ed25519")
	}
	sig := sr.str()
	if pr.err != nil || sr.err != nil || len(pr.b) != 0 || len(sr.b) != 0 || len(key) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return errors.New("malformed key or signature")
	}
	var digest []byte
	switch string(hashAlg) {
	case "sha512":
		d := sha512.Sum512(msg)
		digest = d[:]
	case "sha256":
		d := sha256.Sum256(msg)
		digest = d[:]
	default:
		return errors.New("unsupported hash")
	}
	var signed bytes.Buffer
	signed.WriteString("SSHSIG")
	for _, s := range [][]byte{ns, reserved, hashAlg, digest} {
		binary.Write(&signed, binary.BigEndian, uint32(len(s)))
		signed.Write(s)
	}
	if !ed25519.Verify(ed25519.PublicKey(key), signed.Bytes(), sig) {
		return errors.New("signature does not verify")
	}
	return nil
}

// sshReader reads SSH wire-format fields; the first error sticks.
type sshReader struct {
	b   []byte
	err error
}

func (r *sshReader) raw(n int) []byte {
	if r.err != nil || len(r.b) < n {
		r.err = errors.New("short")
		return nil
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}

func (r *sshReader) u32() uint32 {
	b := r.raw(4)
	if b == nil {
		return 0
	}
	return binary.BigEndian.Uint32(b)
}

func (r *sshReader) str() []byte {
	n := r.u32()
	if r.err != nil || uint64(n) > uint64(len(r.b)) {
		r.err = errors.New("short")
		return nil
	}
	return r.raw(int(n))
}
