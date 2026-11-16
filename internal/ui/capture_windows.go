package ui

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lxn/walk"
	"golang.org/x/sys/windows"

	"github.com/yanmad27/ace-spy/internal/pusher"
)

const (
	captureSeconds  = 60
	maxCaptureBytes = 4 << 20 // server limit, gzipped
)

// captureScript records the game's traffic with pktmon (built into Windows, needs
// admin): only connections owned by the game processes, for a fixed time, then
// converts it to pcapng. It writes a done file with the endpoints, or the error.
const captureScript = `param([string]$Pids, [int]$Seconds, [string]$Dir)
$ErrorActionPreference = 'Stop'
$done = Join-Path $Dir 'done.txt'
try {
  $ids = $Pids -split ',' | ForEach-Object { [int]$_ }
  $conns = Get-NetTCPConnection -State Established | Where-Object { $ids -contains $_.OwningProcess }
  $ips = $conns | ForEach-Object { $_.RemoteAddress } | Sort-Object -Unique
  if (-not $ips) { throw 'the game has no open server connection' }
  $etl = Join-Path $Dir 'ace.etl'
  $pcap = Join-Path $Dir 'ace.pcapng'
  pktmon filter remove | Out-Null
  foreach ($ip in $ips) { pktmon filter add -i $ip | Out-Null }
  pktmon start --capture --pkt-size 0 -f $etl | Out-Null
  Start-Sleep -Seconds $Seconds
  pktmon stop | Out-Null
  pktmon etl2pcap $etl -o $pcap | Out-Null
  pktmon filter remove | Out-Null
  $info = $conns | ForEach-Object { "$($_.OwningProcess) $($_.LocalPort)->$($_.RemoteAddress):$($_.RemotePort)" }
  ('OK ' + ($info -join '; ')) | Out-File -Encoding utf8 $done
} catch {
  pktmon stop 2>$null | Out-Null
  pktmon filter remove 2>$null | Out-Null
  ('ERR ' + $_.Exception.Message) | Out-File -Encoding utf8 $done
}
`

// capturePackets records a minute of the game clients' network traffic and uploads
// it (gzipped, token-protected) so the protocol can be studied for exact chat text.
func (a *app) capturePackets() {
	a.readFields()
	res := newResolver(a.cfg.Regions)
	var pids []string
	seen := map[uint32]bool{}
	for _, r := range a.cfg.Regions {
		if !r.Enabled || r.Window == nil {
			continue
		}
		info, err := res.Resolve(*r.Window)
		if err != nil {
			a.logf("[%s] %v", r.Label, err)
			continue
		}
		if !seen[info.PID] {
			seen[info.PID] = true
			pids = append(pids, strconv.FormatUint(uint64(info.PID), 10))
		}
	}
	if len(pids) == 0 {
		a.errorBox("Ghi gói tin", fmt.Errorf("không tìm thấy cửa sổ game nào đã gắn với vùng chụp"))
		return
	}
	msg := fmt.Sprintf("ace-spy sẽ ghi lưu lượng mạng giữa game (PID %s) và server game trong %d giây, rồi gửi lên server ace-spy để phân tích.\n\n"+
		"Windows sẽ hỏi quyền Administrator (pktmon). Hãy đăng nhập game trước, và trong lúc ghi gõ 1–2 câu chat.\n\nTiếp tục?",
		strings.Join(pids, ", "), captureSeconds)
	if walk.MsgBox(a.mw, "Ghi gói tin (thăm dò)", msg, walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
		return
	}
	dir, err := os.MkdirTemp("", "ace-spy-capture-")
	if err != nil {
		a.errorBox("Ghi gói tin", err)
		return
	}
	script := filepath.Join(dir, "capture.ps1")
	if err := os.WriteFile(script, []byte(captureScript), 0o600); err != nil {
		a.errorBox("Ghi gói tin", err)
		return
	}
	args := fmt.Sprintf(`-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File "%s" -Pids %s -Seconds %d -Dir "%s"`,
		script, strings.Join(pids, ","), captureSeconds, dir)
	verb, _ := windows.UTF16PtrFromString("runas")
	exe, _ := windows.UTF16PtrFromString("powershell.exe")
	argp, _ := windows.UTF16PtrFromString(args)
	if err := windows.ShellExecute(0, verb, exe, argp, nil, windows.SW_HIDE); err != nil {
		a.errorBox("Ghi gói tin", fmt.Errorf("không chạy được pktmon với quyền Administrator: %w", err))
		return
	}
	a.logf("Đang ghi gói tin %d giây (PID %s)… gõ vài câu chat trong game.", captureSeconds, strings.Join(pids, ", "))
	note := "pids=" + strings.Join(pids, ",")
	p := pusher.New(a.cfg.ServerURL, a.cfg.Token, a.cfg.ClientName, nil)
	go a.finishCapture(dir, note, p)
}

func (a *app) finishCapture(dir, note string, p *pusher.Pusher) {
	done := filepath.Join(dir, "done.txt")
	deadline := time.Now().Add(time.Duration(captureSeconds+90) * time.Second)
	var status string
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(done); err == nil && len(b) > 0 {
			status = strings.TrimSpace(strings.TrimPrefix(string(b), "\uFEFF"))
			break
		}
		time.Sleep(time.Second)
	}
	switch {
	case status == "":
		a.asyncLogf("Ghi gói tin: hết thời gian chờ (đã từ chối quyền Administrator?)")
		return
	case strings.HasPrefix(status, "ERR"):
		a.asyncLogf("Ghi gói tin lỗi: %s", strings.TrimSpace(status[3:]))
		return
	}
	raw, err := os.ReadFile(filepath.Join(dir, "ace.pcapng"))
	if err != nil {
		a.asyncLogf("Ghi gói tin: %v", err)
		return
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(raw)
	zw.Close()
	if buf.Len() > maxCaptureBytes {
		a.asyncLogf("Ghi gói tin: file quá lớn (%d KB nén) để gửi; bản gốc ở %s", buf.Len()>>10, dir)
		return
	}
	note += " " + strings.TrimSpace(status[2:])
	if len(note) > 480 {
		note = note[:480]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := p.UploadCapture(ctx, note, buf.Bytes()); err != nil {
		a.asyncLogf("Gửi gói tin lỗi: %v (file ở %s)", err, dir)
		return
	}
	a.asyncLogf("Đã gửi gói tin: %d KB (nén %d KB). Cảm ơn!", len(raw)>>10, buf.Len()>>10)
	os.RemoveAll(dir)
}
