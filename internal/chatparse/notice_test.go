package chatparse

import (
	"reflect"
	"strings"
	"testing"
)

func yellow(line string) []string {
	n := len(strings.Fields(line))
	out := make([]string, n)
	for i := range out {
		out[i] = "ffff00"
	}
	return out
}

func TestNoticeLines(t *testing.T) {
	lines := []string{
		"Chiến trường sẽ bắt đầu sau 5 phút nữa",
		"^[14:41] [DR] [SV] +5 rag ngay",
		"Hệ thống bảo trì lúc 15 giờ, các bạn",
		"vui lòng thoát game",
		"*[14:42] [Tổng thống] [FinalChapter] tru ae",
	}
	colors := [][]string{
		yellow(lines[0]),
		{"ea2222", "ea2222", "ea2222", "ea2222", "ea2222"},
		yellow(lines[2]),
		yellow(lines[3]),
		{"00ffff", "00ffff", "00ffff", "00ffff", "00ffff", "00ffff"},
	}
	got := NewNameBook().MessagesColored(lines, colors)
	var texts []string
	for _, m := range got {
		texts = append(texts, m.Text)
	}
	want := []string{
		"§ Chiến trường sẽ bắt đầu sau 5 phút nữa",
		"^[14:41] [DR] [SV] +5 rag ngay",
		"§ Hệ thống bảo trì lúc 15 giờ, các bạn vui lòng thoát game",
		"*[14:42] [Tổng thống] [FinalChapter] tru ae",
	}
	if !reflect.DeepEqual(texts, want) {
		t.Fatalf("got %q\nwant %q", texts, want)
	}
	if n := len(got[2].Colors); n != len(strings.Fields(want[2]))-1 {
		t.Fatalf("notice colours = %d", n)
	}
	for _, s := range want {
		if !Acceptable(s) {
			t.Errorf("Acceptable(%q) = false", s)
		}
	}
}

func TestYellowChatWrapIsNotNotice(t *testing.T) {
	lines := []string{"^[14:46] [Sky_Jun] [BHC] chem rong bcu trtc ae vao", "nhanh len nao ae oi"}
	colors := [][]string{
		{"ea2222", "ea2222", "ffff00", "ffff00", "ffff00", "ffff00", "ffff00", "ffff00", "ffff00"},
		yellow(lines[1]),
	}
	got := NewNameBook().MessagesColored(lines, colors)
	if len(got) != 1 || !strings.HasSuffix(got[0].Text, "ae vao nhanh len nao ae oi") {
		t.Fatalf("got %+v", got)
	}
}

func TestNoticeRejectsJunk(t *testing.T) {
	for _, s := range []string{"§ ·Dlliiø€ ..", "§ [14:20] [Audi] ban DP", "§ "} {
		if Acceptable(s) {
			t.Errorf("Acceptable(%q) = true", s)
		}
	}
}
