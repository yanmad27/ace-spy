package remote

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	anydeskService = "AnyDesk"
	anydeskImage   = "anydesk.exe"
	settleDelay    = 3 * time.Second
)

type anydeskProc struct {
	pid     uint32
	session uint32
}

// openService opens the AnyDesk service with only the rights asked for, which a
// standard user has for query; start usually needs an administrator.
func openService(access uint32) (*mgr.Service, error) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, err
	}
	defer windows.CloseServiceHandle(m)
	name, _ := windows.UTF16PtrFromString(anydeskService)
	h, err := windows.OpenService(m, name, access)
	if err != nil {
		return nil, err
	}
	return &mgr.Service{Name: anydeskService, Handle: h}, nil
}

func serviceInfo() (state string, exe string) {
	s, err := openService(windows.SERVICE_QUERY_STATUS | windows.SERVICE_QUERY_CONFIG)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return "none", ""
	}
	if err != nil {
		return "unknown (" + err.Error() + ")", ""
	}
	defer s.Close()
	if st, err := s.Query(); err != nil {
		state = "unknown (" + err.Error() + ")"
	} else {
		state = stateName(st.State)
	}
	if cfg, err := s.Config(); err == nil {
		exe = imagePath(cfg.BinaryPathName)
	}
	return state, exe
}

func stateName(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "starting"
	case svc.StopPending:
		return "stopping"
	case svc.Running:
		return "running"
	case svc.ContinuePending, svc.PausePending, svc.Paused:
		return "paused"
	}
	return fmt.Sprintf("state %d", s)
}

// imagePath extracts the executable from a service command line such as
// `"C:\Program Files (x86)\AnyDesk\AnyDesk.exe" --service`.
func imagePath(cmdline string) string {
	cmdline = strings.TrimSpace(cmdline)
	if rest, ok := strings.CutPrefix(cmdline, `"`); ok {
		if i := strings.IndexByte(rest, '"'); i >= 0 {
			return rest[:i]
		}
		return rest
	}
	if i := strings.Index(strings.ToLower(cmdline), ".exe"); i >= 0 {
		return cmdline[:i+4]
	}
	return cmdline
}

func anydeskProcs() ([]anydeskProc, error) { return procsByImage(anydeskImage) }

func procsByImage(image string) ([]anydeskProc, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var out []anydeskProc
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if !strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), image) {
			continue
		}
		p := anydeskProc{pid: e.ProcessID, session: ^uint32(0)}
		windows.ProcessIdToSessionId(e.ProcessID, &p.session)
		out = append(out, p)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return out, err
	}
	return out, nil
}

func currentSession() uint32 {
	var s uint32
	windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &s)
	return s
}

func processPath(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// findExe prefers the service's image, then a running copy (portable AnyDesk), then the default installs.
func findExe(serviceExe string, procs []anydeskProc) string {
	if serviceExe != "" && fileExists(serviceExe) {
		return serviceExe
	}
	for _, p := range procs {
		if path := processPath(p.pid); path != "" {
			return path
		}
	}
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles"} {
		if dir := os.Getenv(env); dir != "" {
			if path := filepath.Join(dir, "AnyDesk", "AnyDesk.exe"); fileExists(path) {
				return path
			}
		}
	}
	return ""
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

func anydeskStatus() string {
	state, serviceExe := serviceInfo()
	procs, err := anydeskProcs()
	own, other := 0, 0
	me := currentSession()
	for _, p := range procs {
		if p.session == me {
			own++
		} else {
			other++
		}
	}
	s := fmt.Sprintf("anydesk service=%s procs(this session=%d, other=%d)", state, own, other)
	if err != nil {
		s += " procerr=" + err.Error()
	}
	if exe := findExe(serviceExe, procs); exe != "" {
		s += " exe=" + exe
	} else {
		s += " exe=not found"
	}
	return s
}

// anydeskStart starts the service if it is stopped and launches AnyDesk in this
// session when none runs here. restart first ends the copies in this session.
func anydeskStart(restart, all bool) string {
	var log []string
	state, serviceExe := serviceInfo()
	procs, err := anydeskProcs()
	if err != nil {
		log = append(log, "list processes: "+err.Error())
	}
	exe := findExe(serviceExe, procs)
	me := currentSession()

	if restart {
		killed := 0
		for _, p := range procs {
			if p.session != me && !all {
				continue
			}
			if err := terminate(p.pid); err != nil {
				log = append(log, fmt.Sprintf("kill %d: %v", p.pid, err))
			} else {
				killed++
			}
		}
		scope := "this session"
		if all {
			scope = "all sessions"
		}
		log = append(log, fmt.Sprintf("ended %d of %d in %s", killed, len(procs), scope))
		time.Sleep(2 * time.Second)
		state, _ = serviceInfo()
	}

	if state == "stopped" {
		if err := startService(); err != nil {
			log = append(log, "service start: "+err.Error())
		} else {
			log = append(log, "service start requested")
		}
	}

	procs, _ = anydeskProcs()
	running := false
	for _, p := range procs {
		if p.session == me {
			running = true
			break
		}
	}
	switch {
	case running:
		log = append(log, "already running in this session")
	case exe == "":
		log = append(log, "AnyDesk.exe not found")
	default:
		cmd := exec.Command(exe)
		cmd.Dir = filepath.Dir(exe)
		if err := cmd.Start(); err != nil {
			log = append(log, "launch: "+err.Error())
		} else {
			cmd.Process.Release()
			log = append(log, "launched "+exe)
		}
	}

	time.Sleep(settleDelay)
	return strings.Join(log, "; ") + " → " + anydeskStatus()
}

func startService() error {
	s, err := openService(windows.SERVICE_START)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Start()
}

func terminate(pid uint32) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}

const parsecImage = "parsecd.exe"

// parsecStart launches Parsec in this session if it is not already running here.
func parsecStart() string {
	me := currentSession()
	procs, err := procsByImage(parsecImage)
	if err != nil {
		return "list processes: " + err.Error()
	}
	for _, p := range procs {
		if p.session == me {
			return parsecStatus("already running in this session")
		}
	}
	var candidates []string
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
		if dir := os.Getenv(env); dir != "" {
			candidates = append(candidates, filepath.Join(dir, "Parsec", parsecImage))
		}
	}
	if dir := os.Getenv("APPDATA"); dir != "" {
		candidates = append(candidates, filepath.Join(dir, "Parsec", parsecImage))
	}
	for _, exe := range candidates {
		if !fileExists(exe) {
			continue
		}
		cmd := exec.Command(exe)
		cmd.Dir = filepath.Dir(exe)
		if err := cmd.Start(); err != nil {
			return "launch " + exe + ": " + err.Error()
		}
		cmd.Process.Release()
		time.Sleep(settleDelay)
		return parsecStatus("launched " + exe)
	}
	return "parsecd.exe not found in " + strings.Join(candidates, ", ")
}

func parsecStatus(prefix string) string {
	procs, _ := procsByImage(parsecImage)
	me := currentSession()
	own := 0
	for _, p := range procs {
		if p.session == me {
			own++
		}
	}
	return fmt.Sprintf("%s → parsec procs(this session=%d, other=%d)", prefix, own, len(procs)-own)
}
