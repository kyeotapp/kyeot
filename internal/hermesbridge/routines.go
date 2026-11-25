package hermesbridge

import (
	"cmp"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

const runWindow = 14 * 24 * time.Hour

// routineRuns turns the last two weeks of cron/output/<routine>/<time>.md into routine.run messages, oldest first.
// "<routine>/<time>" entries marked in seen are skipped. The body is cut at 64KB.
func routineRuns(home string, now time.Time, seen func(key string) bool) []protocol.AgentMessage {
	names := map[string]string{}
	for _, j := range readJobs(home) {
		names[j.ID] = cmp.Or(j.Name, j.ID)
	}
	root := filepath.Join(home, "cron", "output")
	dirs, _ := os.ReadDir(root)
	var out []protocol.AgentMessage
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(root, d.Name()))
		for _, f := range files {
			run, ok := strings.CutSuffix(f.Name(), ".md")
			if !ok {
				continue
			}
			at, err := time.ParseInLocation("2006-01-02_15-04-05", run, time.Local)
			if err != nil || now.Sub(at) > runWindow || (seen != nil && seen(d.Name()+"/"+run)) {
				continue
			}
			raw, err := readHead(filepath.Join(root, d.Name(), f.Name()), 4*maxRoutineText)
			if err != nil {
				continue
			}
			text := runBody(string(raw))
			quiet := text == "" || quietRun(text) // kept only in the run history, not as a conversation
			if quiet {
				text = ""
			}
			out = append(out, protocol.AgentMessage{Type: "routine.run", Routine: d.Name(),
				RoutineName: cmp.Or(names[d.Name()], d.Name()), Run: run, Text: clipText(text, maxRoutineText), At: at.UnixMilli(), Quiet: quiet})
		}
	}
	slices.SortFunc(out, func(a, b protocol.AgentMessage) int { return cmp.Compare(a.At, b.At) })
	return out
}

// quietRun checks whether the body is only a Hermes [SILENT] marker or a local-address success reply ({"ok":true,...}, no reason).
// false if there is even one line of failure reply or human-visible text.
func quietRun(body string) bool {
	for line := range strings.Lines(body) {
		line = strings.TrimSpace(line)
		if line == "" || line == "[SILENT]" {
			continue
		}
		var ack struct {
			OK     *bool  `json:"ok"`
			Reason string `json:"reason"`
		}
		if json.Unmarshal([]byte(line), &ack) != nil || ack.OK == nil || !*ack.OK || ack.Reason != "" {
			return false
		}
	}
	return true
}

// readHead reads only the first n bytes.
func readHead(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, n))
}

// runBody strips the "# Cron Job: …" header up to the first --- line.
func runBody(md string) string {
	if !strings.HasPrefix(md, "# Cron Job") {
		return strings.TrimSpace(md)
	}
	if strings.Contains(md, "**Status:** silent") {
		return "" // a run that ended without output
	}
	if _, body, ok := strings.Cut(md, "\n---\n"); ok {
		return strings.TrimSpace(body)
	}
	return strings.TrimSpace(md)
}
