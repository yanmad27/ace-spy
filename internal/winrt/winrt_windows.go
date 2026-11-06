// Package winrt holds minimal helpers for calling COM / WinRT interfaces through
// raw vtables, plus a worker that runs calls on one OS thread in the MTA.
package winrt

import (
	"fmt"
	"runtime"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	combase                       = windows.NewLazySystemDLL("combase.dll")
	procRoInitialize              = combase.NewProc("RoInitialize")
	procRoUninitialize            = combase.NewProc("RoUninitialize")
	procRoGetActivationFactory    = combase.NewProc("RoGetActivationFactory")
	procWindowsCreateString       = combase.NewProc("WindowsCreateString")
	procWindowsDeleteString       = combase.NewProc("WindowsDeleteString")
	procWindowsGetStringRawBuffer = combase.NewProc("WindowsGetStringRawBuffer")
)

const (
	roInitMultithreaded = 1
	rpcEChangedMode     = 0x80010106
)

func GUID(s string) windows.GUID {
	g, err := windows.GUIDFromString(s)
	if err != nil {
		panic(err)
	}
	return g
}

// Obj is any COM interface pointer.
type Obj struct{ vtbl *[128]uintptr }

func (o *Obj) Ptr() uintptr { return uintptr(unsafe.Pointer(o)) }

// Call invokes vtable slot with this as the first argument and checks the HRESULT.
func (o *Obj) Call(slot int, args ...uintptr) error {
	all := make([]uintptr, 0, len(args)+1)
	all = append(all, o.Ptr())
	all = append(all, args...)
	hr, _, _ := syscall.SyscallN(o.vtbl[slot], all...)
	if int32(hr) < 0 {
		return fmt.Errorf("HRESULT 0x%08X", uint32(hr))
	}
	return nil
}

func (o *Obj) Release() {
	if o != nil {
		syscall.SyscallN(o.vtbl[2], o.Ptr())
	}
}

func (o *Obj) QueryInterface(iid *windows.GUID) (*Obj, error) {
	var out *Obj
	if err := o.Call(0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out))); err != nil {
		return nil, err
	}
	return out, nil
}

// Out returns the address to pass for an interface out-parameter.
func Out(p **Obj) uintptr { return uintptr(unsafe.Pointer(p)) }

type HString uintptr

func NewHString(s string) (HString, error) {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		return 0, err
	}
	var h HString
	hr, _, _ := procWindowsCreateString.Call(uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&h)))
	if int32(hr) < 0 {
		return 0, fmt.Errorf("WindowsCreateString: HRESULT 0x%08X", uint32(hr))
	}
	return h, nil
}

func (h HString) String() string {
	if h == 0 {
		return ""
	}
	var n uint32
	p, _, _ := procWindowsGetStringRawBuffer.Call(uintptr(h), uintptr(unsafe.Pointer(&n)))
	if p == 0 || n == 0 {
		return ""
	}
	// p points into WinRT-owned memory, valid until h is freed.
	buf := *(**uint16)(unsafe.Pointer(&p))
	return string(utf16.Decode(unsafe.Slice(buf, n)))
}

func (h HString) Free() {
	if h != 0 {
		procWindowsDeleteString.Call(uintptr(h))
	}
}

func ActivationFactory(class string, iid *windows.GUID) (*Obj, error) {
	hs, err := NewHString(class)
	if err != nil {
		return nil, err
	}
	defer hs.Free()
	var out *Obj
	hr, _, _ := procRoGetActivationFactory.Call(uintptr(hs), uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	if int32(hr) < 0 {
		return nil, fmt.Errorf("RoGetActivationFactory(%s): HRESULT 0x%08X", class, uint32(hr))
	}
	return out, nil
}

// Thread runs functions on a single OS thread initialised for the Windows Runtime (MTA).
type Thread struct {
	jobs chan func()
	done chan struct{}
}

func NewThread() (*Thread, error) {
	t := &Thread{jobs: make(chan func()), done: make(chan struct{})}
	ready := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		hr, _, _ := procRoInitialize.Call(roInitMultithreaded)
		if int32(hr) < 0 && uint32(hr) != rpcEChangedMode {
			ready <- fmt.Errorf("RoInitialize: HRESULT 0x%08X", uint32(hr))
			return
		}
		if int32(hr) >= 0 {
			defer procRoUninitialize.Call()
		}
		ready <- nil
		for {
			select {
			case <-t.done:
				return
			case f := <-t.jobs:
				f()
			}
		}
	}()
	if err := <-ready; err != nil {
		return nil, err
	}
	return t, nil
}

// Do runs f on the thread and waits for it.
func (t *Thread) Do(f func()) bool {
	finished := make(chan struct{})
	select {
	case t.jobs <- func() { defer close(finished); f() }:
	case <-t.done:
		return false
	}
	<-finished
	return true
}

func (t *Thread) Close() {
	select {
	case <-t.done:
	default:
		close(t.done)
	}
}
