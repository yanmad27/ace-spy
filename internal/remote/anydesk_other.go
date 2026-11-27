//go:build !windows

package remote

func anydeskStatus() string { return "anydesk: unsupported on this OS" }

func anydeskStart(bool) string { return "anydesk: unsupported on this OS" }
