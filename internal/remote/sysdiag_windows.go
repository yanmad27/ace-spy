package remote

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// sysDiag reports what remote-access paths exist without changing anything.
func sysDiag() string {
	var out []string
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE); err == nil {
		name, _, _ := k.GetStringValue("ProductName")
		edition, _, _ := k.GetStringValue("EditionID")
		build, _, _ := k.GetStringValue("CurrentBuild")
		k.Close()
		out = append(out, fmt.Sprintf("windows=%s edition=%s build=%s", name, edition, build))
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Terminal Server`, registry.QUERY_VALUE); err == nil {
		deny, _, err := k.GetIntegerValue("fDenyTSConnections")
		k.Close()
		out = append(out, fmt.Sprintf("rdp enabled=%v (err=%v)", err == nil && deny == 0, err))
	}
	out = append(out, "user admin="+elevationType())

	var found []string
	for _, rel := range []string{
		`Tailscale\tailscale.exe`, `WireGuard\wireguard.exe`, `RustDesk\rustdesk.exe`,
		`Parsec\parsecd.exe`, `TeamViewer\TeamViewer.exe`, `Google\Chrome Remote Desktop`,
		`OpenSSH\sshd.exe`, `TightVNC\tvnserver.exe`, `uvnc bvba\UltraVNC\winvnc.exe`,
	} {
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "APPDATA", "LOCALAPPDATA"} {
			if dir := os.Getenv(env); dir != "" {
				if _, err := os.Stat(filepath.Join(dir, rel)); err == nil {
					found = append(found, filepath.Join(env, rel))
				}
			}
		}
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("SystemRoot"), `System32\OpenSSH\sshd.exe`)); err == nil {
		found = append(found, `System32\OpenSSH\sshd.exe`)
	}
	out = append(out, "installed: "+strings.Join(found, ", "))
	for _, svc := range []string{"TermService", "sshd", "Tailscale", "Parsec", "WireGuardManager"} {
		out = append(out, svc+"="+serviceState(svc))
	}
	return strings.Join(out, "\n")
}

func serviceState(name string) string {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return err.Error()
	}
	defer windows.CloseServiceHandle(m)
	n, _ := windows.UTF16PtrFromString(name)
	h, err := windows.OpenService(m, n, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		if err == windows.ERROR_SERVICE_DOES_NOT_EXIST {
			return "none"
		}
		return err.Error()
	}
	defer windows.CloseServiceHandle(h)
	var st windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(h, &st); err != nil {
		return err.Error()
	}
	if st.CurrentState == windows.SERVICE_RUNNING {
		return "running"
	}
	return fmt.Sprintf("state%d", st.CurrentState)
}

// elevationType: "limited" means an administrator running under UAC, "default" a standard user
// (or UAC off), "full" already elevated.
func elevationType() string {
	t := windows.GetCurrentProcessToken()
	var v uint32
	var n uint32
	if err := windows.GetTokenInformation(t, windows.TokenElevationType, (*byte)(unsafe.Pointer(&v)), 4, &n); err != nil {
		return err.Error()
	}
	switch v {
	case 1:
		return "default (standard user or UAC off)"
	case 2:
		return "full (elevated)"
	case 3:
		return "limited (administrator under UAC)"
	}
	return fmt.Sprint(v)
}
