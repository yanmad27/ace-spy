"""Builds the glyph atlas for the template-matching OCR (internal/glyphocr).

The game renders chat with Verdana 12px without anti-aliasing. Each glyph is
rendered alone into a fixed-height cell with a common baseline; the advance
width is the hinted advance, so a line is the concatenation of its glyphs.

Usage: python3 atlas.py FONT_PATH OUT_JSON
"""
import json
import sys
import unicodedata

from PIL import Image, ImageDraw, ImageFont

FONT, OUT = sys.argv[1], sys.argv[2]
font = ImageFont.truetype(FONT, 12)
CELL_H, TOP = 20, 3  # rows; glyphs are drawn at y=TOP so tall diacritics fit

ascii_chars = [chr(c) for c in range(33, 127)]
vowels = "aăâeêioôơuưyAĂÂEÊIOÔƠUƯY"
tones = ["", "̀", "́", "̉", "̃", "̣"]
viet = sorted({unicodedata.normalize("NFC", v + t) for v in vowels for t in tones} | set("đĐ"))
extra = list("“”‘’…–—°©®×÷¤§¥€£•·¡¿ØøÆæßÄÖÜäöüÀÁÈÉÌÍÒÓÙÚÝàáèéìíòóùúý")
chars = sorted(set(ascii_chars + viet + extra))

PAD = 4

def bitmap(text):
    w = int(font.getlength(text)) + 2 * PAD + 4
    im = Image.new("1", (w, CELL_H), 0)
    d = ImageDraw.Draw(im)
    d.fontmode = "1"
    d.text((PAD, TOP), text, font=font, fill=1)
    return im

glyphs = {}
for c in chars + [" "]:
    adv = font.getlength(c)
    if abs(adv - round(adv)) > 1e-6:
        sys.exit(f"non-integer advance for {c!r}: {adv}")
    adv = int(round(adv))
    im = bitmap(c)
    cols = []
    for x in range(PAD, PAD + adv):
        v = 0
        for y in range(CELL_H):
            if im.getpixel((x, y)):
                v |= 1 << y
        cols.append(v)
    overhang = any(im.getpixel((x, y)) for x in list(range(PAD)) + list(range(PAD + adv, im.size[0])) for y in range(CELL_H))
    glyphs[c] = {"adv": adv, "cols": cols, "overhang": overhang}

# Verify the concatenation property on real chat text.
tests = ["^[15:38] [Sky_Luxury] [BC] tốn cmdt vl", "$[15:41] [GoD_HienXinh] em cho thuê DP - 100 = 160 DP (Giáp tím 8x 300 /1 )",
         "*[16:59] [Phú tổng tư lãnh] [Audi] 5 slot danh trụ, Ấy Ộ Ữ ợ ỹ đĐ ``~_|"]
bad = 0
for t in tests:
    whole = bitmap(t)
    x = 0
    for c in t:
        g = glyphs[c]
        for i, v in enumerate(g["cols"]):
            for y in range(CELL_H):
                if bool(v >> y & 1) != bool(whole.getpixel((PAD + x + i, y))):
                    bad += 1
        x += g["adv"]
print(f"{len(glyphs)} glyphs, overhangs: {[c for c, g in glyphs.items() if g['overhang']]}, concat mismatches: {bad}")
json.dump({"cellHeight": CELL_H, "top": TOP, "glyphs": glyphs}, open(OUT, "w", encoding="utf-8"), ensure_ascii=False)
