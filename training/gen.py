"""Synthetic Ace Online chat lines for fine-tuning Tesseract.

The game draws chat with Verdana 12px, no anti-aliasing (verified pixel-exact
against real captures). Each line is rendered the way the game shows it,
upscaled x2 like the client's capture, then run through the same
preprocessing as internal/tesseract.Preprocess (max(R,G) -> x1.5 bilinear ->
Otsu -> invert), so training images match what Tesseract sees in production.

Usage: python3 gen.py OUT_DIR COUNT SEED FONT_PATH
"""
import os
import random
import sys
import unicodedata

from PIL import Image, ImageDraw, ImageFont

OUT, COUNT, SEED, FONT = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), sys.argv[4]
rng = random.Random(SEED)
font = ImageFont.truetype(FONT, 12)
MAX_W = 680

COLORS = {
    "red": (225, 45, 45), "yellow": (235, 205, 50), "green": (70, 230, 50), "cyan": (70, 220, 225),
    "white": (225, 225, 225), "magenta": (215, 50, 215), "blue": (95, 155, 255), "purple": (185, 110, 255),
    "orange": (240, 150, 40), "grey": (190, 190, 190),
}
CHANNEL_COLOR = {"^": "red", "!": "green", "*": "blue", "@": "purple", "#": "orange", "$": "yellow", "%": "orange", "": "white"}

PHRASES = """tối nay đi săn boss không
ai bán đá cường hóa không
chờ mình một chút nhé
sáng giờ bị đấm cho mấy mạng
kiên trì thật vẫn bay lên bắn cho bằng được
con gà này
chết ngược rồi
giờ phải đi rình mò bắn lại
cay để bụng là có thật
bay lên tấn công trên không luôn
em cho thuê giáp tím
ae còn cú giáp dư trade với mình nhé
sự kiện đại chiến mặt trăng lúc 20h00
anh em ra trụ hết đi
ai chém MG cùng không
để tối đi moon
mua gì vậy
full bố 2 ông AG rồi
tý vay 100k wp
còn không đủ wp bán
xin giáp moon MG thịt
tối cho nhé
tối mở ra không xài cho
chầy cối vừa thôi a tuấn
ui cảm ơn nhé
vào party đi
đợt e cũng tầm đó
hơn 300 ấy chứ
chỉ cho lên vàng cái là ủn xanh dương
làm qua 3u xanh hoặc tím 90 đi
xài 75 z sao không xài cây 125 chời
3u trắng cũng được chứ xài 75 làm gì
125 miss làm bậc
300 cây 3u chưa lên vàng nữa
sai 3u 75 chém MG không nổi luôn
yếu nhất sv mẹ rồi
xin ae chỉ sơ BG chủ yếu cắm map và đi BB
đau đáng wz hết nl nên về tp mà
trụ này đi sao nhỉ
ai chỉ giúp với
map hẻm núi philip
về tp vào cổng dịch chuyển lên map
thua ngược
mua đồ ai bán cho xin cái giá
mg xin giáp như vậy đau đúi
anh em IG về hết trụ
tìm mạng trụ
km cầm đầu em anh quay sụp chú
bán đồ đổi e3 chip nhiên liệu
nháy nháy chú hùa chỉ biết cắn chạy
có vài mạng thế này đánh đấm gì
một với một dám không
cắn đã rủ cả bầy lên
lên bắn cho bằng được
đi đâu đấy
ngủ chưa
ra map chính đi
cần người đi cùng
ai có súng không
bán acc giá rẻ
nhận đổi vàng
hôm qua đánh ghê thật
đội hình mạnh quá
ai rảnh không
mình mới chơi
chỉ mình cách lên level
giáp này bao nhiêu
cho xin cái link
ai pk không
thôi đi ăn cơm
ăn cơm xong vào tiếp
mai gặp lại nhé
hẹn tối nay
đánh trận này thắng chắc
bên kia đông quá
rút về trụ đi
giữ trụ cẩn thận
nhớ mua máu
hết đạn rồi
sửa đồ đi
ai buff giùm
nhường đi mà
được rồi để đó
không biết nữa
thật không vậy
chuẩn luôn
quá đỉnh
ngon lành
đừng có mà
làm sao bây giờ
xong rồi đó
tập trung vào
ưu tiên bắn MG trước
trừ máu nhanh quá
bị lag ghê
mạng yếu quá
ae ơi giúp với
quay lại đây
""".strip().split("\n")

WORDS_EN = ["boss", "party", "pm", "map", "moon", "slot", "camp", "call", "bank", "level", "token", "chip", "trade", "full",
            "ok", "acc", "farm", "buff", "lag", "skill", "item", "event", "online", "team", "go", "afk", "gg", "noob", "pro"]
TEEN = ["vl", "vcl", "cmm", "cmndt", "ko", "k", "đc", "dc", "ae", "mn", "bn", "mk", "ib", "nt", "z", "j", "r", "ms", "kg"]
EMOTES = ["=))", "=)))", ":v", ":V", ":d", ":D", "^^", ":))", "=)", "-_-", "=.=", ":(", "haha", "hahah", "hihi", "kkk"]
TAGS = ["BC", "AC", "EV", "CoP", "Audi", "AB", "Do", "AG", "MG", "BG", "VN", "GM", "TT", "IG"]
RANKS = ["Tổng tư lệnh", "Phú tổng tư lãnh", "Tham mưu trưởng", "Tổng tư lãnh", "Đội trưởng", "Thành viên"]
NAME_PARTS = ["Sky", "Luxury", "Daddy", "Shopee", "Lava", "Titanic", "Castle", "Coronada", "Hulk", "Long", "Time", "No", "See",
              "Arale", "Galbrena", "That", "Sat", "Thien", "Kim", "Hoa", "Hong", "Do", "Ngoc", "Song", "Ngan", "Nhat", "Anh",
              "Val", "Hellsing", "Rayleigh", "God", "Hien", "Xinh", "Diana", "Momota", "Chu", "Tom", "Vang", "Xin", "Cam", "On",
              "Toxic", "Galarius", "Salarius", "Cheng", "Audi", "Jun", "Man", "Dark", "Andy", "Quang", "Ga", "Kiem", "Si",
              "Phap", "Su", "Rong", "Bao", "Lua", "Bang", "Gio", "Sam", "Set", "Ninja", "Pro", "King", "Lady", "Angel", "Devil"]
INITIALS = ["", "b", "c", "ch", "d", "đ", "g", "gh", "gi", "h", "k", "kh", "l", "m", "n", "ng", "ngh", "nh", "p", "ph", "qu",
            "r", "s", "t", "th", "tr", "v", "x"]
RIMES = ["a", "ai", "am", "an", "ang", "anh", "ao", "au", "ay", "ăm", "ăn", "ăng", "âm", "ân", "âng", "âu", "ây", "e", "em",
         "en", "eo", "ê", "êm", "ên", "ênh", "êu", "i", "ia", "im", "in", "inh", "iu", "iêm", "iên", "iêu", "o", "oa", "oai",
         "oan", "oang", "oanh", "oi", "om", "on", "ong", "ô", "ôi", "ôm", "ôn", "ông", "ơ", "ơi", "ơm", "ơn", "u", "ua", "ui",
         "um", "un", "ung", "uôi", "uôn", "uông", "uy", "uyên", "ư", "ưa", "ưi", "ưng", "ươi", "ươn", "ương", "ưu", "y", "yêu"]
STOP_RIMES = ["ac", "ach", "at", "ăc", "ăt", "âc", "ât", "ep", "et", "êch", "êt", "ich", "it", "iêt", "oc", "ot", "ôc", "ôt",
              "ơt", "uc", "ut", "uôc", "uôt", "ưc", "ưt", "ươc", "ươt", "ap", "ăp", "âp", "ip", "op", "ôp", "ơp", "up"]
TONES = ["", "̀", "́", "̉", "̃", "̣"]
STOP_TONES = ["́", "̣"]
VOWELS = "aăâeêioôơuưy"


def with_tone(rime, tone):
    if not tone:
        return rime
    letters = list(unicodedata.normalize("NFD", rime))
    base = [i for i, c in enumerate(unicodedata.normalize("NFC", rime)) if c in VOWELS]
    nfc = list(unicodedata.normalize("NFC", rime))
    special = [i for i, c in enumerate(nfc) if c in "ăâêôơư"]
    if special:
        pos = special[-1]
    elif len(base) >= 2 and not rime.endswith(tuple(VOWELS)) or len(base) == 3:
        pos = base[1]
    else:
        pos = base[0]
    nfc[pos] = unicodedata.normalize("NFC", nfc[pos] + tone)
    return "".join(nfc)


def syllable():
    if rng.random() < 0.25:
        rime, tone = rng.choice(STOP_RIMES), rng.choice(STOP_TONES)
    else:
        rime, tone = rng.choice(RIMES), rng.choice(TONES)
    word = rng.choice(INITIALS) + with_tone(rime, tone)
    return word


def strip_accents(s):
    s = s.replace("đ", "d").replace("Đ", "D")
    return "".join(c for c in unicodedata.normalize("NFD", s) if unicodedata.category(c) != "Mn")


def number():
    return rng.choice([
        str(rng.randint(1, 999)), f"{rng.randint(1, 999)}k", f"{rng.randint(1, 50)}m", f"{rng.randint(1, 20)}x",
        f"{rng.randint(1, 9)}u", f"{rng.randint(10, 23)}h{rng.choice(['', '00', '30'])}", f"{rng.randint(1, 100)}%",
        f"{rng.randint(1, 9)}0{rng.randint(0, 9)}0",
    ])


def body():
    r = rng.random()
    if r < 0.35:
        words = rng.choice(PHRASES).split()
    elif r < 0.55:
        words = strip_accents(rng.choice(PHRASES)).split()
    elif r < 0.75:
        words = [syllable() for _ in range(rng.randint(2, 9))]
    else:
        pool = [rng.choice(WORDS_EN), rng.choice(TEEN), number(), syllable(), rng.choice(PHRASES).split()[0], rng.choice(TAGS)]
        words = [rng.choice(pool) for _ in range(rng.randint(1, 7))]
    if rng.random() < 0.25:
        words.insert(rng.randint(0, len(words)), number())
    if rng.random() < 0.2:
        words.insert(rng.randint(0, len(words)), rng.choice(TEEN + WORDS_EN))
    if rng.random() < 0.15:
        words = [w.upper() if rng.random() < 0.5 else w for w in words]
    if rng.random() < 0.25:
        words.append(rng.choice(EMOTES))
    if rng.random() < 0.05:
        words = [rng.choice(EMOTES)]
    return " ".join(words)


def player():
    r = rng.random()
    if r < 0.1:
        return rng.choice(RANKS)
    parts = rng.sample(NAME_PARTS, rng.randint(1, 3))
    name = rng.choice(["", "_", "x", "X"]).join(parts) if rng.random() < 0.6 else "".join(parts)
    if rng.random() < 0.2:
        name = name.lower()
    if rng.random() < 0.2:
        name += str(rng.randint(1, 99))
    if rng.random() < 0.15:
        name = "_" + name + "_"
    elif rng.random() < 0.1:
        name = name + rng.choice(["_", "."])
    return name[:16]


def segments():
    """Returns [(text, colour)] for one chat line, mimicking in-game colouring."""
    if rng.random() < 0.08:  # wrapped continuation line
        return [(body(), rng.choice(list(COLORS)))]
    ch = rng.choices(list(CHANNEL_COLOR), weights=[6, 2, 2, 1, 1, 2, 1, 1])[0]
    head_col = CHANNEL_COLOR[ch]
    segs = [(f"{ch}[{rng.randint(0, 23):02d}:{rng.randint(0, 59):02d}] ", head_col)]
    names = [player()]
    if rng.random() < 0.6:
        names.append(rng.choice(TAGS) if rng.random() < 0.7 else player())
    if rng.random() < 0.05:
        names.append(player())
    for i, n in enumerate(names):
        col = head_col
        if i > 0 and rng.random() < 0.3:
            col = "magenta"
        segs.append((f"[{n}] ", col))
    body_col = head_col if rng.random() < 0.6 else rng.choice(list(COLORS))
    segs.append((body(), body_col))
    return segs


def background(w, h):
    base = (rng.randint(12, 40), rng.randint(35, 75), rng.randint(60, 115))
    im = Image.new("RGB", (w, h), base)
    px = im.load()
    gx, gy = rng.uniform(-0.04, 0.04), rng.uniform(-0.6, 0.6)
    for y in range(h):
        for x in range(w):
            d = gx * x + gy * y + rng.uniform(-4, 4)
            px[x, y] = tuple(max(0, min(255, int(c + d))) for c in base)
    if rng.random() < 0.2:  # brighter scenery behind the semi-transparent chat box
        d = ImageDraw.Draw(im)
        x0 = rng.randint(0, w)
        d.rectangle([x0, 0, x0 + rng.randint(20, 120), h], fill=tuple(min(255, c + rng.randint(20, 50)) for c in base))
    return im


def otsu(hist, total):
    s = sum(i * c for i, c in enumerate(hist))
    sb = wb = 0.0
    best = thr = 0
    for t in range(256):
        wb += hist[t]
        if wb == 0:
            continue
        wf = total - wb
        if wf == 0:
            break
        sb += t * hist[t]
        mb, mf = sb / wb, (s - sb) / wf
        v = wb * wf * (mb - mf) ** 2
        if v > best:
            best, thr = v, t
    return thr


def preprocess(im):
    r, g, _ = im.split()
    rp, gp = r.load(), g.load()
    w, h = im.size
    out = Image.new("L", (w, h))
    op = out.load()
    for y in range(h):
        for x in range(w):
            op[x, y] = max(rp[x, y], gp[x, y])
    out = out.resize((int(w * 1.5), int(h * 1.5)), Image.BILINEAR)
    hist = out.histogram()
    thr = otsu(hist, out.size[0] * out.size[1])
    out = out.point(lambda v: 255 if v > thr else 0)
    if sum(i * c for i, c in enumerate(out.histogram())) / (out.size[0] * out.size[1]) < 128:
        out = out.point(lambda v: 255 - v)
    return out


def render():
    segs = segments()
    text = "".join(t for t, _ in segs).strip()
    while font.getlength(text) > MAX_W - 14 and len(segs[-1][0].split()) > 1:
        words = segs[-1][0].split()[:-1]
        segs[-1] = (" ".join(words), segs[-1][1])
        text = "".join(t for t, _ in segs).strip()
    w = int(font.getlength(text)) + 14
    h = 16
    im = background(w, h)
    d = ImageDraw.Draw(im)
    d.fontmode = "1"
    x = 7.0
    for t, col in segs:
        d.text((x, 1), t, font=font, fill=COLORS[col])
        x += font.getlength(t)
    im = im.resize((w * 2, h * 2), Image.BILINEAR)  # client capture scale x2
    return preprocess(im), " ".join(text.split())


os.makedirs(OUT, exist_ok=True)
for i in range(COUNT):
    img, gt = render()
    base = os.path.join(OUT, f"ace_{SEED}_{i:05d}")
    img.save(base + ".png")
    with open(base + ".gt.txt", "w", encoding="utf-8") as f:
        f.write(gt + "\n")
