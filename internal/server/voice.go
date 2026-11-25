package server

import (
	"encoding/binary"
	"math"
	"regexp"
	"strings"
	"unicode"
)

// Below quietRMS, whisper is not called. Based on the peak 50ms-window RMS (16-bit, about -58dBFS)
const quietRMS = 40

// Phrases whisper makes up from silence. If the whole result is just repeats of these, drop it
var hallucination = regexp.MustCompile(`^(?:감사합니다|시청해주셔서감사합니다|구독과좋아요|끝|mbc뉴스\p{Hangul}{2,4}입니다|이영상은\p{Hangul}*제작되었습니다|thankyou|thanksforwatching)+$`)

func isHallucination(text string) bool {
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.Len() > 0 && hallucination.MatchString(b.String())
}

// peakRMS is the peak 50ms-window RMS of a 16-bit PCM WAV. ok=false if unreadable
func peakRMS(wav []byte) (rms float64, ok bool) {
	if len(wav) < 12 || string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return 0, false
	}
	var rate, bits, channels uint32
	var data []byte
	for p := 12; p+8 <= len(wav); {
		id, size := string(wav[p:p+4]), int(binary.LittleEndian.Uint32(wav[p+4:p+8]))
		body := wav[p+8:]
		if size < 0 || size > len(body) {
			size = len(body)
		}
		body = body[:size]
		switch id {
		case "fmt ":
			if len(body) < 16 || binary.LittleEndian.Uint16(body[0:2]) != 1 {
				return 0, false
			}
			channels = uint32(binary.LittleEndian.Uint16(body[2:4]))
			rate = binary.LittleEndian.Uint32(body[4:8])
			bits = uint32(binary.LittleEndian.Uint16(body[14:16]))
		case "data":
			data = body
		}
		p += 8 + size + size%2
	}
	if bits != 16 || channels == 0 || rate == 0 || data == nil {
		return 0, false
	}
	win := int(rate/20) * int(channels)
	var sum float64
	n := 0
	for i := 0; i+1 < len(data); i += 2 {
		s := float64(int16(binary.LittleEndian.Uint16(data[i:])))
		sum += s * s
		if n++; n == win {
			rms, sum, n = math.Max(rms, math.Sqrt(sum/float64(n))), 0, 0
		}
	}
	if n > 0 {
		rms = math.Max(rms, math.Sqrt(sum/float64(n)))
	}
	return rms, true
}
