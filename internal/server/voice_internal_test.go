package server

import (
	"encoding/binary"
	"math"
	"testing"
)

// pcmWAV puts a FLLR chunk between fmt and data, like afconvert does
func pcmWAV(samples []int16) []byte {
	le := binary.LittleEndian
	b := []byte("RIFF\x00\x00\x00\x00WAVEfmt ")
	b = le.AppendUint32(b, 16)
	b = le.AppendUint16(b, 1)
	b = le.AppendUint16(b, 1)
	b = le.AppendUint32(b, 16000)
	b = le.AppendUint32(b, 32000)
	b = le.AppendUint16(b, 2)
	b = le.AppendUint16(b, 16)
	b = append(b, "FLLR"...)
	b = le.AppendUint32(b, 7)
	b = append(b, make([]byte, 8)...) // odd size + padding byte
	b = append(b, "data"...)
	b = le.AppendUint32(b, uint32(2*len(samples)))
	for _, s := range samples {
		b = le.AppendUint16(b, uint16(s))
	}
	le.PutUint32(b[4:], uint32(len(b)-8))
	return b
}

func TestPeakRMS(t *testing.T) {
	quiet := make([]int16, 16000)
	burst := make([]int16, 16000)
	for i := range burst {
		if i%2 == 0 {
			quiet[i] = 30
		} else {
			quiet[i] = -30
		}
		if i >= 8000 && i < 8800 { // only one 50ms window is loud
			burst[i] = 1000
		}
	}
	for name, c := range map[string]struct {
		wav  []byte
		want float64
		ok   bool
	}{
		"silence":   {pcmWAV(make([]int16, 16000)), 0, true},
		"quiet":     {pcmWAV(quiet), 30, true},
		"one burst": {pcmWAV(burst), 1000, true},
		"not wav":   {[]byte("AUDIO"), 0, false},
		"no data":   {pcmWAV(nil)[:44], 0, false},
	} {
		got, ok := peakRMS(c.wav)
		if ok != c.ok || math.Abs(got-c.want) > 0.5 {
			t.Errorf("%s: %v %v, want %v %v", name, got, ok, c.want, c.ok)
		}
	}
}

func TestIsHallucination(t *testing.T) {
	for text, want := range map[string]bool{
		"MBC 뉴스 이준범입니다.":  true,
		"MBC 뉴스 김지경입니다":   true,
		"-감사합니다. -감사합니다.": true,
		"[감사합니다]":         true,
		"(끝)":             true,
		"시청해 주셔서 감사합니다!":  true,
		"이 영상은 대화가 안정돈으로 제작되었습니다.": true,
		"Thank you.": true,
		"":           false,
		"내일 아침 아홉시에 회의 알려줘": false,
		"감사합니다. 내일 회의 알려줘":  false,
		"MBC 뉴스 틀어 줘":       false,
	} {
		if got := isHallucination(text); got != want {
			t.Errorf("%q: %v", text, got)
		}
	}
}
