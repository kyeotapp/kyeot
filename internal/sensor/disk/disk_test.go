package disk

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestBuildMatchesDfRule(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 5, 30, 0, time.Local)
	// df -k: used 400,000,000 KB, avail 100,000,000 KB → 80.0%, 102.4 GB
	b := Build(Usage{Used: 400_000_000 * 1024, Avail: 100_000_000 * 1024}, nil, now)
	if b.UsedPct == nil || *b.UsedPct != 80 || b.FreeGB == nil || *b.FreeGB != 102.4 || b.Checked != "2026-11-17 09:05" || b.Status != "정상" {
		t.Fatalf("%+v %v %v", b, *b.UsedPct, *b.FreeGB)
	}
	if b.UsedGB == nil || *b.UsedGB != 409.6 || b.TotalGB == nil || *b.TotalGB != 512 || b.Usage != "409.6 / 512 GB" {
		t.Fatalf("capacity: %+v", b)
	}
}

func TestBuildFailureIsShown(t *testing.T) {
	b := Build(Usage{}, errors.New("permission denied"), time.Now())
	raw, _ := json.Marshal(b)
	if b.Status != "측정 못 함: permission denied" || string(raw) == "" || b.FreeGB != nil {
		t.Fatalf("%s", raw)
	}
	if b := Build(Usage{}, nil, time.Now()); b.FreeGB != nil || b.Status == "정상" {
		t.Fatalf("size 0 is unmeasurable: %+v", b)
	}
}

func TestParseDf(t *testing.T) {
	out := "Filesystem 1024-blocks Used Available Capacity iused ifree %iused  Mounted on\n/dev/disk3s5 482797652 297642884 156410208 66% 3 1564102080 0% /System/Volumes/Data\n"
	u, err := parseDf(out)
	if b := Build(u, err, time.Now()); err != nil || *b.UsedPct != 65.6 || *b.FreeGB != 160.2 {
		t.Fatalf("%+v %v", u, err)
	}
	if _, err := parseDf("x"); err == nil {
		t.Fatal("read failure should error")
	}
}

func TestMeasureOnThisComputer(t *testing.T) {
	u, err := measureDisk()
	if err != nil || u.Used == 0 || u.Avail == 0 {
		t.Fatalf("%+v %v", u, err)
	}
}

func TestRunPublishesAndRefreshes(t *testing.T) {
	got := make(chan Bundle, 4)
	n := 0
	s := &Sensor{Every: time.Hour, Measure: func() (Usage, error) { n++; return Usage{Used: uint64(n) * 1e9, Avail: 9e9}, nil },
		Push: func(raw json.RawMessage) { var b Bundle; json.Unmarshal(raw, &b); got <- b }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	if b := <-got; *b.UsedPct != 10 {
		t.Fatalf("first measurement %+v", b)
	}
	s.Refresh()
	select {
	case b := <-got:
		if *b.UsedPct != 18.2 {
			t.Fatalf("refresh %v", *b.UsedPct)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh was not uploaded")
	}
}
