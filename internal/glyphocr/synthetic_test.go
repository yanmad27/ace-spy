package glyphocr

import (
	"testing"
)

// drawFrame renders chat lines with the atlas glyphs the way the game does:
// coloured text on a dark-blue gradient, 15px line pitch.
func drawFrame(t *testing.T, e *Engine, lines []string, colors [][3]byte, w int) ([]byte, int, int) {
	t.Helper()
	h := len(lines)*linePitch + 6
	pix := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			pix[i], pix[i+1], pix[i+2], pix[i+3] = byte(90+x/12), byte(40+y/8), byte(15+x/40), 255
		}
	}
	byText := map[string]*glyph{}
	for i := range e.glyphs {
		byText[e.glyphs[i].text] = &e.glyphs[i]
	}
	paint := func(x, top int, col uint32, c [3]byte) {
		for r := 0; r < e.cellH; r++ {
			if col>>r&1 == 1 {
				if y := top + r; y >= 0 && y < h && x >= 0 && x < w {
					i := (y*w + x) * 4
					pix[i], pix[i+1], pix[i+2] = c[2], c[1], c[0]
				}
			}
		}
	}
	for li, line := range lines {
		top := 2 + li*linePitch - e.top + 1
		x := 7
		for _, r := range line {
			g := byText[string(r)]
			if g == nil {
				t.Fatalf("no glyph for %q", r)
			}
			for i, c := range g.cols {
				paint(x+i, top, c, colors[li%len(colors)])
			}
			for i, c := range g.over {
				paint(x+g.adv+i, top, c, colors[li%len(colors)])
			}
			x += g.adv
		}
	}
	return pix, w, h
}

func TestSyntheticFrame(t *testing.T) {
	e, err := New()
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{
		"^[15:38] [Sky_Luxury] [BC] tốn cmdt vl =))",
		"*[16:59] [Phó tổng thống] [Drift] ý kiến nhiều e gọi Thehulk bây giờ :D",
		"$[17:37] [ANTD_HD] Mua thẻ Chiến Công 60/1 - wp 40k/1m SLL",
		"![17:40] [Line] new xin suport DE PIN RADA",
		"@[17:41] [___Meta___] [AC] trong shop có mà, được chưa? ừ ữ ự ở đây",
		"[15:26] [TheHulk] du` go~ bua` ra mas\"",
		"%[09:05] [Đội_Trưởng] [CoP] Ấy Ộ Ữ Ẵ Ặ ỹ ỵ ĩ ũ",
	}
	colors := [][3]byte{{225, 45, 45}, {95, 155, 255}, {235, 205, 50}, {70, 230, 50}, {185, 110, 255}, {225, 225, 225}, {240, 150, 40}}
	pix, w, h := drawFrame(t, e, lines, colors, 640)
	// The game's mouse cursor drawn over the chat, far right of the text.
	for y := 18; y < 30; y++ {
		for x := 600; x < 600+(y-18); x++ {
			i := (y*w + x) * 4
			pix[i], pix[i+1], pix[i+2] = 255, 200, 120
		}
	}
	got, err := e.Recognize(pix, w, h)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(lines) {
		t.Fatalf("got %d lines: %q", len(got), got)
	}
	for i := range lines {
		if got[i] != lines[i] {
			t.Errorf("line %d\n got  %q\n want %q", i, got[i], lines[i])
		}
	}
}

func TestSkipsLineCutByEdge(t *testing.T) {
	e, _ := New()
	pix, w, h := drawFrame(t, e, []string{"^[15:38] [Sky_Luxury] [BC] ok", "^[15:39] [Daddy] [BC] haha"}, [][3]byte{{225, 45, 45}}, 400)
	h -= 9 // cut the second line in half
	got, _ := e.Recognize(pix[:w*h*4], w, h)
	if len(got) != 1 || got[0] != "^[15:38] [Sky_Luxury] [BC] ok" {
		t.Fatalf("got %q", got)
	}
}

// Background art glued to the end of a message (no gap) must be cut off.
func TestTrimsBackgroundJunk(t *testing.T) {
	e, _ := New()
	lines := []string{
		"^[10:19] [_MuuHenKeBan_] [BC] bg sai ose ngon hay agamenon ngon ta",
		"^[10:20] [_Next_] [BC] cả 2 đều ngon",
		"*[10:21] [Tổng thống] [FinalChapter] tru ae",
		"$[10:22] [ahihihihi] Ban the LM vang",
	}
	colors := [][3]byte{{0xea, 0x22, 0x22}, {0xea, 0x22, 0x22}, {0x00, 0xff, 0xff}, {0xfe, 0xae, 0x00}}
	pix, w, h := drawFrame(t, e, lines, colors, 520)
	// streaks of varying bright greyish-blue colour right after the first line's text
	end := 7
	for _, r := range lines[0] {
		end += e.byText[string(r)].adv
	}
	top := 2 - e.top + 1
	for k, x := range []int{end + 1, end + 3, end + 4, end + 7, end + 9, end + 10, end + 13} {
		for y := top + e.top + 2; y < top+e.top+11; y++ {
			i := (y*w + x) * 4
			v := byte(150 + (k*17+y*9)%90)
			pix[i], pix[i+1], pix[i+2] = v, v-20, v-60
		}
	}
	got, _ := e.Recognize(pix, w, h)
	if len(got) != len(lines) {
		t.Fatalf("got %q", got)
	}
	for i := range lines {
		if got[i] != lines[i] {
			t.Errorf("line %d\n got  %q\n want %q", i, got[i], lines[i])
		}
	}
}
