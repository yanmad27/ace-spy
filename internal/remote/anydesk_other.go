//go:build !windows

package remote

func anydeskStatus() string { return "anydesk: unsupported on this OS" }

func anydeskStart(bool, bool) string { return "anydesk: unsupported on this OS" }

func parsecStart() string { return "parsec: unsupported on this OS" }

func anydeskDiag() string { return "anydesk: unsupported on this OS" }

func sysDiag() string { return "sys: unsupported on this OS" }
