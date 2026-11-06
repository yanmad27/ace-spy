# ace-spy

Reads Ace Online chat from configurable screen regions with Windows OCR (Vietnamese), and shows new lines on a live web viewer (Vercel).

```
[Windows PC: ace-spy.exe]                              [Vercel: cmd/server (Go) + Neon Postgres]
 region "Bang hội"  ─┐  GDI capture → upscale            POST /api/ingest (Bearer token)
 region "Toàn sv"   ─┼─ → Tesseract (vie)        ───▶   Postgres + dedupe
 region "Đội"       ─┘  → diff vs previous frame          web viewer polls /api/messages every 2 s
```

## Server / web viewer (Vercel)

Vercel's Go preset builds `cmd/server`. Neon Postgres comes from the Vercel Marketplace and sets `DATABASE_URL`.

| Env | Default | Notes |
|-----|---------|-------|
| `DATABASE_URL` | — (required) | Set by the Neon integration (`POSTGRES_URL` also accepted) |
| `INGEST_TOKEN` | — (required, ≥16 chars) | Same value goes in the client |
| `VIEW_PASSWORD` | empty = public | HTTP Basic auth for viewer + read APIs |
| `VIEW_USER` | `admin` | |
| `CRON_SECRET` | empty = no pruning on Vercel | Vercel Cron calls `/api/cron/prune` daily with it |
| `RETENTION_DAYS` | `30` | `0` keeps everything |
| `DEDUPE_SECONDS` | `600` | Window for fuzzy duplicate detection per region. `0` disables |

```bash
vercel link
vercel integration add neon --yes      # provisions Postgres, sets DATABASE_URL
vercel env add INGEST_TOKEN production
vercel env add VIEW_PASSWORD production
vercel env add CRON_SECRET production
vercel deploy --prod
```

Self-hosting works too: `docker build -t ace-spy-server .`, then run it with the same env vars and `-p 8080:8080`.

API: `GET /api/messages?region=&before=<id>|after=<id>&limit=`, `GET /api/regions`.

## Releases and auto-update

Everything deploys automatically on `git push` to `main`:
- **Website/server:** Vercel redeploys from Git.
- **Windows client:** if the push touches client code, `.github/workflows/release.yml` bumps the patch version, tests on Windows and publishes `ace-spy-X.Y.Z-windows-amd64.zip`. Put `[skip release]` in the commit message to skip it. `./release.sh x.y.z` still works for a specific version.
- **Installed clients** check GitHub Releases at startup and every 30 minutes. A newer version is downloaded, verified against `SHA256SUMS.txt`, swapped in next to the exe, and restarted; capture resumes if it was running. Config (`ace-spy.json`) is kept.

## Client (Windows 10/11)

1. Download the zip from [GitHub Releases](https://github.com/yanmad27/ace-spy/releases/latest) and extract **all** of it. It bundles a portable Tesseract (`tesseract/`) and the Vietnamese model (`tessdata/vie.traineddata`), so nothing needs installing.
2. Leave **OCR engine** on `tesseract`. Windows' built-in OCR has no Vietnamese; the `windows` option only suits other languages.
3. Run `ace-spy.exe` and enter the server URL (your `*.vercel.app` domain), the ingest token and a client name.
4. Click **Add region…**, then **Pick area…**. The screen freezes; drag a box around the chat box. Give each region a label (for example `Client 1`, `Client 2`). The label becomes the tab name on the website.
   - The area is **bound to the game window (process) under it**. Coordinates are stored relative to that window, and capture uses `PrintWindow`, so the client can be moved or covered. It must not be minimized.
   - **Two game clients:** add one region per client, picking each over its own window. If a client restarts (new PID), the region re-attaches to a running client that no other region is using, preferring the same window title.
5. Use **Test OCR** to check what is recognized. If small text is misread, raise **Upscale** (×2–×3).
6. Click **Start**.

Config is stored in `ace-spy.json` next to the exe, and the log in `ace-spy.log`.

### What gets pushed
Only chat messages with the in-game structure `<channel>[hh:mm] [Name] text` are pushed. Wrapped lines are joined onto their message. Lines without a timestamp, and lines that don't read as Vietnamese (checked by syllable structure plus common chat abbreviations), are dropped as OCR noise. The server applies the same check, then drops fuzzy duplicates (diacritics ignored, ≥80% similar) received in the same region within `DEDUPE_SECONDS` (default 600).

Channel symbols are normalised (`I`/`|` → `!`, `©` → `@`, `ˆ` → `^` …), and the website colours each channel: `!` green, `@` purple, `#` `$` `%` orange, `^` red.

### Notes and limits
- Run the game in **windowed or borderless** mode. Exclusive fullscreen DirectX can capture as black.
- If the game window is resized, re-pick the area. Areas not over an app window fall back to fixed screen coordinates.
- New-line detection aligns each OCR frame with the previous one, with fuzzy matching to tolerate OCR noise. If you scroll the chat box up manually, older lines can be re-sent; the server dedupe window absorbs most of them.
- The viewer polls every 2 s (15 s when the tab is hidden), so lines appear within about 2–3 s.
- Unchanged frames (same pixel hash) skip OCR, so idle chat costs almost no CPU.
