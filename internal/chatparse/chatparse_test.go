package chatparse

import (
	"reflect"
	"testing"
)

func TestIsSyllable(t *testing.T) {
	valid := []string{"mua", "đầy", "tối", "nghiêng", "khuya", "tuân", "xuân", "giữa", "gì", "quả", "chuyện", "người", "di", "san", "khong", "bú", "ăn", "uống"}
	invalid := []string{"bark", "zÁZ", "căk", "xyz", "bäk", "ÍXe", "Hellsing", "tiếng̃", "ab", "mm"}
	for _, s := range valid {
		if !IsSyllable(s) {
			t.Errorf("%q should be a syllable", s)
		}
	}
	for _, s := range invalid {
		if IsSyllable(s) {
			t.Errorf("%q should not be a syllable", s)
		}
	}
}

// Frames below are real OCR output captured in production.
func TestMessagesFromRealFrame(t *testing.T) {
	frame := []string{
		"bark ae",
		"*[11:10] [Tổng tư lệnh] [valHellsing]ibark AE",
		"nháy nháy cmm. chú hùa. chì biết cắn chạy",
		"*[11:12] [Tổng tư lệnh] [valHellsing] bäk 2E",
		": zr cú vài mang thế này đánh đấm gì",
		"*[11:12] [Tổng tư lệnh] [valHellsing] ra AE",
		"bú căk tap nè 1 với 1 dám. cắn đã rủ cả bầy lên. chú hùa",
		"*[11:15] [Tổng tư lệnh] [valHellsing] +5 bùf liuen tục",
		"vl chay]",
		"*[11:2D] [Tổng tư lãnh] [valHellsing] chem cơn ig ` LÍ",
		"3 lã (D Ấ* m ® ® té mi",
		"..ˆ —_ = )(ÍXe ì",
		"3V l4 (0 4 li ®% t2 ma",
		"—zCP—\"—unneEZ — —",
		"HS — —",
		"“Chat - —>=— - -",
		"I[11:05) IANTD-HID]. bán",
		"$[11:08] [valHellsing] mua ax+7 đầy AE",
		"Tớ lò G) A4 “I Ấ ® le mi",
	}
	want := []string{
		"*[11:10] [Tổng tư lệnh] [valHellsing] ibark AE nháy nháy cmm. chú hùa. chì biết cắn chạy",
		"*[11:12] [Tổng tư lệnh] [valHellsing] bäk 2E : zr cú vài mang thế này đánh đấm gì",
		"*[11:12] [Tổng tư lệnh] [valHellsing] ra AE bú căk tap nè 1 với 1 dám. cắn đã rủ cả bầy lên. chú hùa",
		"*[11:15] [Tổng tư lệnh] [valHellsing] +5 bùf liuen tục vl chay]",
		"*[11:20] [Tổng tư lãnh] [valHellsing] chem cơn ig ` LÍ",
		"![11:05] [ANTD-HID] bán",
		"$[11:08] [valHellsing] mua ax+7 đầy AE",
	}
	got := Messages(frame)
	if !reflect.DeepEqual(got, want) {
		for i, g := range got {
			t.Logf("got[%d] %q", i, g)
		}
		t.Fatalf("got %d messages, want %d", len(got), len(want))
	}
}

func TestMessagesDropsNoiseOnlyFrames(t *testing.T) {
	frames := [][]string{
		{"—zÁZ — —", "——ÁIZ — —", "“Chat - —>— - -", "Tm Ð Y7”. XI", "G) [oior.lerinltvalitelleingljtusnrehiemcon mg"},
		{"Add region... Edit... Remove TestOCR", "x.. mm Em ,2 „ ¿4 KT: mm mm ra", "sẽ mm c5 c „ s ÔDx mm [mm", "® ace-spy-0.2.0-windows-amd64.zip"},
	}
	for i, f := range frames {
		if got := Messages(f); len(got) != 0 {
			t.Errorf("frame %d: expected nothing, got %q", i, got)
		}
	}
}

func TestMessagesRejectsTruncatedHeaders(t *testing.T) {
	for _, line := range []string{"[11:05] [A", "[11:07] [Tí", "[11:11] IT", "[11:05] [Ab] ® ‡ „ “"} {
		if got := Messages([]string{line}); len(got) != 0 {
			t.Errorf("%q: expected nothing, got %q", line, got)
		}
	}
}

func TestMessagesRequireTimestamp(t *testing.T) {
	if got := Messages([]string{"tối nay đi săn boss không", "ai bán đá cường hóa không"}); len(got) != 0 {
		t.Fatalf("got %q", got)
	}
}

func TestChannelSymbols(t *testing.T) {
	cases := map[string]string{
		"![11:05] [Diana] tối nay đi săn": "![11:05] [Diana] tối nay đi săn",
		"I[11:05) [Diana] tối nay đi săn": "![11:05] [Diana] tối nay đi săn",
		"@[11:05] [Diana] tối nay đi săn": "@[11:05] [Diana] tối nay đi săn",
		"©[11:05] [Diana] tối nay đi săn": "@[11:05] [Diana] tối nay đi săn",
		"#[11:05] [Diana] tối nay đi săn": "#[11:05] [Diana] tối nay đi săn",
		"$l1:05] [Diana] tối nay đi săn":  "$[11:05] [Diana] tối nay đi săn",
		"%[11:05] [Diana] tối nay đi săn": "%[11:05] [Diana] tối nay đi săn",
		"ˆ[11:05] [Diana] tối nay đi săn": "^[11:05] [Diana] tối nay đi săn",
		"[11:05] [Diana] tối nay đi săn":  "[11:05] [Diana] tối nay đi săn",
	}
	for in, want := range cases {
		got := Messages([]string{in})
		if len(got) != 1 || got[0] != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestKeyFoldsDiacritics(t *testing.T) {
	a := Key("[11:15] [Tổng tư lệnh] [valHellsing] +5 bùf liên tục")
	b := Key("[11:15] [Tong tu lenh] [valHellsing] +5 buf lien tuc")
	if a != b {
		t.Fatalf("%q != %q", a, b)
	}
}

// Real production lines where OCR merged several messages into one line.
func TestMessagesSplitsMergedLines(t *testing.T) {
	got := Messages([]string{
		"[14:11] [Tổng tư lệnh] [ValHellsing] icshay røpi | *l14:11]I©astlerCaromadn] [LAO MØOC] con gà này =))) | *l14:1Z] [Castle.Cgronada] [LAO MØC] sáng giờ bị đấm cho mấy mạng cả AG lắn MG",
	})
	want := []string{
		"[14:11] [Tổng tư lệnh] [ValHellsing] icshay røpi",
		"*[14:11] [©astlerCaromadn] [LAO MØOC] con gà này =)))",
		"*[14:12] [Castle.Cgronada] [LAO MØC] sáng giờ bị đấm cho mấy mạng cả AG lắn MG",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestMessagesRejectsOwnLogEcho(t *testing.T) {
	got := Messages([]string{"[14:16] 43 [ANI] [14:12] [Castle] cay để bụng là có thật"})
	want := []string{"[14:12] [Castle] cay để bụng là có thật"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	if Acceptable("[14:16] 48 [ANI] [14:12] [Castle] cay để bụng là có thật") {
		t.Fatal("log echo accepted")
	}
}

// Real chat is often teencode or emoticons; with a proper header it must be kept.
func TestMessagesKeepsShortChat(t *testing.T) {
	for _, line := range []string{
		"^[15:38] [Daddy] [BC] =))",
		"^[15:49] [Sky_Luxury] [AC] :V",
		"^[14:47] [That_Sat_] [BC] ms",
		"^[15:43] [Daddy] [BC] cmndt",
		"^[15:48] [NGNhatAnHy] [AC] MG",
		"^[15:42] [Daddy] [BC] mua 1m wp",
	} {
		if got := Messages([]string{line}); len(got) != 1 {
			t.Errorf("%q dropped", line)
		}
	}
}
