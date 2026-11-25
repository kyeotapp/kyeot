package wordfilter

import "testing"

func TestMatch(t *testing.T) {
	for _, s := range []string{"이 시발", "시 . 발", "병신아", "you fucking idiot", "SHIT!", "stfu", "개 새 끼"} {
		if !Match(s) {
			t.Errorf("should be caught: %q", s)
		}
	}
	for _, s := range []string{"", "저녁 뭐 먹을까?", "그거 보지 마", "시발점이 어디야", "니 엄마 전화 왔어", "Dick Smith", "불이 꺼져 있어", "class assignment", "Dickens novel", "shiitake mushroom", "Scunthorpe"} {
		if Match(s) {
			t.Errorf("should not be caught: %q", s)
		}
	}
}
