# ace-spy

Reads Ace Online chat from configurable screen regions with Windows OCR (Vietnamese), and shows new lines on a live web viewer (Vercel).

```
[Windows PC: ace-spy.exe]                              [Vercel: cmd/server (Go) + Neon Postgres]
 region "Bang hội"  ─┐  GDI capture → upscale            POST /api/ingest (Bearer token)
 region "Toàn sv"   ─┼─ → Windows.Media.Ocr (vi)   ───▶   Postgres + dedupe
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
| `DEDUPE_SECONDS` | `60` | Drops an identical `(region, text)` received within this window. `0` disables |

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

## Releases

Run `./release.sh [x.y.z]` from a clean, pushed `main`. It tags `vX.Y.Z`, and GitHub Actions (`.github/workflows/release.yml`) then tests, builds and publishes `ace-spy-X.Y.Z-windows-amd64.zip` to GitHub Releases.

## Client (Windows 10/11)

1. Install the Vietnamese OCR pack once, from an admin PowerShell:
   ```powershell
   Add-WindowsCapability -Online -Name "Language.OCR~~~vi-VN~0.0.1.0"
   ```
2. Download the zip from GitHub Releases (or run `./build-client.sh`). Unzip it so `ace-spy.exe` and `ace-spy.exe.manifest` sit in the same folder.
3. Run it and enter the server URL (your `*.vercel.app` domain), the ingest token and a client name.
4. Click **Add region…**, then **Pick area…**. The screen freezes; drag a box around the chat box. Give each region a label (for example `Bang hội`, `Toàn server`). The label becomes the channel name on the website.
5. Use **Test OCR** to check what is recognized. If small text is misread, raise **Upscale** (×2–×3).
6. Click **Start**.

Config is stored in `ace-spy.json` next to the exe, and the log in `ace-spy.log`.

### Notes and limits
- Run the game in **windowed or borderless** mode. Exclusive fullscreen DirectX can capture as black.
- Coordinates are physical pixels on the virtual desktop. Moving the game window or changing the resolution means re-picking the regions.
- New-line detection aligns each OCR frame with the previous one, with fuzzy matching to tolerate OCR noise. If you scroll the chat box up manually, older lines can be re-sent; the server dedupe window absorbs most of them.
- The viewer polls every 2 s (15 s when the tab is hidden), so lines appear within about 2–3 s.
- Unchanged frames (same pixel hash) skip OCR, so idle chat costs almost no CPU.
