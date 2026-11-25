package server_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
	"github.com/kyeotapp/kyeot/internal/server"
)

// Runs only with KYEOT_LOAD=1. Measures round-trip latency with people x 20 agents exchanging tasks at once.
// Round trip = a person POSTs /api/tasks -> the agent receives task.start and replies + task.end -> done arrives on the person's screen.
// Uses a file DB (WAL). Measurements include the test-side connections (goroutines, memory) in the same process.
//
//	KYEOT_LOAD=1 go test -run TestManyAgentsLoad -v ./internal/server/
func TestManyAgentsLoad(t *testing.T) {
	if os.Getenv("KYEOT_LOAD") != "1" {
		t.Skip("only with KYEOT_LOAD=1")
	}
	rounds := 10
	for _, c := range []struct {
		name    string
		owners  int
		stalled bool
	}{{"60", 3, false}, {"60+stalled", 3, true}, {"200", 10, false}} {
		t.Run(c.name, func(t *testing.T) { loadOnce(t, c.owners, rounds, c.stalled) })
	}
}

func loadOnce(t *testing.T, owners, rounds int, stalled bool) {
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	g0 := runtime.NumGoroutine()

	h := decktest.NewWith(t, func(o *server.Options) { o.DBPath = filepath.Join(t.TempDir(), "kyeot.db") })
	people := newRigOwners(t, h, owners, 20)
	for _, o := range people {
		o.connect(t, h)
		for _, a := range o.agents {
			a.start(t, h)
		}
	}
	allIdle(t, people)
	if stalled { // one screen that does not read. Check whether it blocks others until the server queue fills and drops it
		ctx, cancel := context.WithCancel(context.Background())
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.URL, "http")+"/client?token="+people[0].token, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.CloseNow(); cancel() })
	}
	gConnected := runtime.NumGoroutine()

	var peakHeap atomic.Uint64
	var peakG atomic.Int64
	stop := make(chan struct{})
	go func() {
		tk := time.NewTicker(50 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				if m.HeapInuse > peakHeap.Load() {
					peakHeap.Store(m.HeapInuse)
				}
				if g := int64(runtime.NumGoroutine()); g > peakG.Load() {
					peakG.Store(g)
				}
			}
		}
	}()

	var mu sync.Mutex
	var rtts, posts []time.Duration
	var fails atomic.Int32
	var wg sync.WaitGroup
	begin := time.Now()
	for _, o := range people {
		for _, a := range o.agents {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range rounds {
					t0 := time.Now()
					var task protocol.Task
					if s := h.APIAs(o.token, "POST", "/api/tasks", map[string]any{"agentId": a.id, "text": fmt.Sprintf("to:%s quick %d", a.id, i)}, &task); s != http.StatusCreated {
						fails.Add(1)
						continue
					}
					t1 := time.Now()
					at, ok := o.client.waitDone(task.ID, decktest.WaitLimit)
					if !ok {
						fails.Add(1)
						continue
					}
					mu.Lock()
					rtts = append(rtts, at.Sub(t0))
					posts = append(posts, t1.Sub(t0))
					mu.Unlock()
				}
			}()
		}
	}
	wg.Wait()
	wall := time.Since(begin)
	close(stop)

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	pct := func(d []time.Duration, p float64) time.Duration {
		if len(d) == 0 {
			return 0
		}
		slices.Sort(d)
		return d[min(len(d)-1, int(float64(len(d))*p))]
	}
	n := len(rtts)
	t.Logf("agents=%d stalled=%v rounds=%d ok=%d fail=%d wall=%s throughput=%.0f/s",
		owners*20, stalled, rounds, n, fails.Load(), wall.Round(time.Millisecond), float64(n)/wall.Seconds())
	t.Logf("roundtrip p50=%s p99=%s max=%s | POST p50=%s p99=%s",
		pct(rtts, .5), pct(rtts, .99), pct(rtts, 1), pct(posts, .5), pct(posts, .99))
	t.Logf("goroutines start=%d connected=%d peak=%d | heap inuse peak=%.1fMB, total alloc during=%.1fMB, sys=%.1fMB",
		g0, gConnected, peakG.Load(), float64(peakHeap.Load())/1e6, float64(after.TotalAlloc-before.TotalAlloc)/1e6, float64(after.Sys)/1e6)
	if fails.Load() > 0 {
		t.Errorf("%d round trips failed", fails.Load())
	}
}
