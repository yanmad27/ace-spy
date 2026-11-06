// Package winocr wraps Windows.Media.Ocr through raw WinRT vtable calls.
// All WinRT objects live on one dedicated MTA thread; callers are serialized.
package winocr

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"time"
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

	iidOcrEngineStatics          = guid("{5BFFA85A-3384-3540-9940-699120D428A8}")
	iidLanguageFactory           = guid("{9B0252AC-0C27-44F8-B792-9793FB66C63E}")
	iidSoftwareBitmapStatics     = guid("{DF0385DB-672F-4A9D-806E-C2442F343E86}")
	iidCryptographicBufferStatic = guid("{320B7E22-3CB0-4CDF-8663-1D28910065EB}")
	iidAsyncInfo                 = guid("{00000036-0000-0000-C000-000000000046}")
)

const (
	roInitMultithreaded   = 1
	bitmapFormatBgra8     = 87
	bitmapAlphaPremulti   = 0
	asyncStarted          = 0
	asyncCompleted        = 1
	asyncCanceled         = 2
	recognizeTimeout      = 10 * time.Second
	rpcEChangedMode       = 0x80010106
	installHintPowerShell = `Add-WindowsCapability -Online -Name "Language.OCR~~~%s~0.0.1.0"`
)

func guid(s string) windows.GUID {
	g, err := windows.GUIDFromString(s)
	if err != nil {
		panic(err)
	}
	return g
}

type comObj struct{ vtbl *[64]uintptr }

func (o *comObj) call(slot int, args ...uintptr) error {
	all := make([]uintptr, 0, len(args)+1)
	all = append(all, uintptr(unsafe.Pointer(o)))
	all = append(all, args...)
	hr, _, _ := syscall.SyscallN(o.vtbl[slot], all...)
	if int32(hr) < 0 {
		return fmt.Errorf("HRESULT 0x%08X", uint32(hr))
	}
	return nil
}

func (o *comObj) release() {
	if o != nil {
		syscall.SyscallN(o.vtbl[2], uintptr(unsafe.Pointer(o)))
	}
}

func (o *comObj) queryInterface(iid *windows.GUID) (*comObj, error) {
	var out *comObj
	if err := o.call(0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out))); err != nil {
		return nil, err
	}
	return out, nil
}

type hstring uintptr

func newHString(s string) (hstring, error) {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		return 0, err
	}
	var h hstring
	hr, _, _ := procWindowsCreateString.Call(uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&h)))
	if int32(hr) < 0 {
		return 0, fmt.Errorf("WindowsCreateString: HRESULT 0x%08X", uint32(hr))
	}
	return h, nil
}

func (h hstring) String() string {
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

func (h hstring) free() {
	if h != 0 {
		procWindowsDeleteString.Call(uintptr(h))
	}
}

func activationFactory(class string, iid *windows.GUID) (*comObj, error) {
	hs, err := newHString(class)
	if err != nil {
		return nil, err
	}
	defer hs.free()
	var out *comObj
	hr, _, _ := procRoGetActivationFactory.Call(uintptr(hs), uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	if int32(hr) < 0 {
		return nil, fmt.Errorf("RoGetActivationFactory(%s): HRESULT 0x%08X", class, uint32(hr))
	}
	return out, nil
}

type request struct {
	pix  []byte
	w, h int
	resp chan response
}

type response struct {
	lines []string
	err   error
}

type Engine struct {
	reqs   chan request
	closed chan struct{}
}

// New starts the OCR thread. language is a BCP-47 tag such as "vi" or "vi-VN".
func New(language string) (*Engine, error) {
	e := &Engine{reqs: make(chan request), closed: make(chan struct{})}
	ready := make(chan error, 1)
	go e.thread(language, ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Engine) Recognize(pix []byte, w, h int) ([]string, error) {
	if len(pix) != w*h*4 || w <= 0 || h <= 0 {
		return nil, errors.New("invalid image buffer")
	}
	resp := make(chan response, 1)
	select {
	case e.reqs <- request{pix: pix, w: w, h: h, resp: resp}:
	case <-e.closed:
		return nil, errors.New("ocr engine closed")
	}
	r := <-resp
	return r.lines, r.err
}

func (e *Engine) Close() {
	select {
	case <-e.closed:
	default:
		close(e.closed)
	}
}

type winrt struct {
	engine  *comObj
	bitmaps *comObj
	buffers *comObj
	maxDim  uint32
}

func (e *Engine) thread(language string, ready chan<- error) {
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

	rt, err := initWinRT(language)
	if err != nil {
		ready <- err
		return
	}
	defer rt.release()
	ready <- nil

	for {
		select {
		case <-e.closed:
			return
		case req := <-e.reqs:
			lines, err := rt.recognize(req.pix, req.w, req.h)
			req.resp <- response{lines: lines, err: err}
		}
	}
}

func initWinRT(language string) (*winrt, error) {
	statics, err := activationFactory("Windows.Media.Ocr.OcrEngine", &iidOcrEngineStatics)
	if err != nil {
		return nil, err
	}
	defer statics.release()

	langFactory, err := activationFactory("Windows.Globalization.Language", &iidLanguageFactory)
	if err != nil {
		return nil, err
	}
	defer langFactory.release()

	tag, err := newHString(language)
	if err != nil {
		return nil, err
	}
	defer tag.free()

	var lang *comObj
	if err := langFactory.call(6, uintptr(tag), uintptr(unsafe.Pointer(&lang))); err != nil {
		return nil, fmt.Errorf("invalid OCR language %q: %w", language, err)
	}
	defer lang.release()

	rt := &winrt{}
	if err := statics.call(6, uintptr(unsafe.Pointer(&rt.maxDim))); err != nil {
		return nil, fmt.Errorf("OcrEngine.MaxImageDimension: %w", err)
	}
	if err := statics.call(9, uintptr(unsafe.Pointer(lang)), uintptr(unsafe.Pointer(&rt.engine))); err != nil {
		return nil, fmt.Errorf("OcrEngine.TryCreateFromLanguage: %w", err)
	}
	if rt.engine == nil {
		return nil, fmt.Errorf("Windows OCR for %q is not installed. In an admin PowerShell run:\n"+installHintPowerShell, language, ocrCapabilityTag(language))
	}
	if rt.bitmaps, err = activationFactory("Windows.Graphics.Imaging.SoftwareBitmap", &iidSoftwareBitmapStatics); err != nil {
		rt.release()
		return nil, err
	}
	if rt.buffers, err = activationFactory("Windows.Security.Cryptography.CryptographicBuffer", &iidCryptographicBufferStatic); err != nil {
		rt.release()
		return nil, err
	}
	return rt, nil
}

func ocrCapabilityTag(language string) string {
	if language == "vi" {
		return "vi-VN"
	}
	return language
}

func (rt *winrt) release() {
	rt.engine.release()
	rt.bitmaps.release()
	rt.buffers.release()
}

func (rt *winrt) recognize(pix []byte, w, h int) ([]string, error) {
	if uint32(w) > rt.maxDim || uint32(h) > rt.maxDim {
		return nil, fmt.Errorf("image %dx%d exceeds OCR max dimension %d; reduce area or scale", w, h, rt.maxDim)
	}

	var buf *comObj
	if err := rt.buffers.call(9, uintptr(len(pix)), uintptr(unsafe.Pointer(&pix[0])), uintptr(unsafe.Pointer(&buf))); err != nil {
		return nil, fmt.Errorf("CreateFromByteArray: %w", err)
	}
	defer buf.release()

	var bmp *comObj
	if err := rt.bitmaps.call(10, uintptr(unsafe.Pointer(buf)), bitmapFormatBgra8, uintptr(w), uintptr(h), bitmapAlphaPremulti, uintptr(unsafe.Pointer(&bmp))); err != nil {
		return nil, fmt.Errorf("CreateCopyWithAlphaFromBuffer: %w", err)
	}
	defer bmp.release()

	var op *comObj
	if err := rt.engine.call(6, uintptr(unsafe.Pointer(bmp)), uintptr(unsafe.Pointer(&op))); err != nil {
		return nil, fmt.Errorf("RecognizeAsync: %w", err)
	}
	defer op.release()

	if err := await(op); err != nil {
		return nil, err
	}

	var result *comObj
	if err := op.call(8, uintptr(unsafe.Pointer(&result))); err != nil {
		return nil, fmt.Errorf("GetResults: %w", err)
	}
	defer result.release()

	var vec *comObj
	if err := result.call(6, uintptr(unsafe.Pointer(&vec))); err != nil {
		return nil, fmt.Errorf("OcrResult.Lines: %w", err)
	}
	defer vec.release()

	var size uint32
	if err := vec.call(7, uintptr(unsafe.Pointer(&size))); err != nil {
		return nil, fmt.Errorf("Lines.Size: %w", err)
	}
	lines := make([]string, 0, size)
	for i := uint32(0); i < size; i++ {
		var line *comObj
		if err := vec.call(6, uintptr(i), uintptr(unsafe.Pointer(&line))); err != nil {
			return nil, fmt.Errorf("Lines.GetAt(%d): %w", i, err)
		}
		var hs hstring
		err := line.call(7, uintptr(unsafe.Pointer(&hs)))
		line.release()
		if err != nil {
			return nil, fmt.Errorf("OcrLine.Text: %w", err)
		}
		lines = append(lines, hs.String())
		hs.free()
	}
	return lines, nil
}

func await(op *comObj) error {
	info, err := op.queryInterface(&iidAsyncInfo)
	if err != nil {
		return fmt.Errorf("IAsyncInfo: %w", err)
	}
	defer info.release()

	deadline := time.Now().Add(recognizeTimeout)
	for {
		var status int32
		if err := info.call(7, uintptr(unsafe.Pointer(&status))); err != nil {
			return fmt.Errorf("IAsyncInfo.Status: %w", err)
		}
		switch status {
		case asyncCompleted:
			return nil
		case asyncStarted:
		case asyncCanceled:
			return errors.New("OCR canceled")
		default:
			var code int32
			_ = info.call(8, uintptr(unsafe.Pointer(&code)))
			return fmt.Errorf("OCR failed: HRESULT 0x%08X", uint32(code))
		}
		if time.Now().After(deadline) {
			_ = info.call(9)
			return errors.New("OCR timed out")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
