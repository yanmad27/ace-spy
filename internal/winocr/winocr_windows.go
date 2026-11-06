// Package winocr wraps Windows.Media.Ocr through raw WinRT vtable calls.
// Windows OCR has no Vietnamese model; it is kept for other languages.
package winocr

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"github.com/yanmad27/ace-spy/internal/winrt"
)

var (
	iidOcrEngineStatics          = winrt.GUID("{5BFFA85A-3384-3540-9940-699120D428A8}")
	iidLanguageFactory           = winrt.GUID("{9B0252AC-0C27-44F8-B792-9793FB66C63E}")
	iidSoftwareBitmapStatics     = winrt.GUID("{DF0385DB-672F-4A9D-806E-C2442F343E86}")
	iidCryptographicBufferStatic = winrt.GUID("{320B7E22-3CB0-4CDF-8663-1D28910065EB}")
	iidAsyncInfo                 = winrt.GUID("{00000036-0000-0000-C000-000000000046}")
)

const (
	bitmapFormatBgra8   = 87
	bitmapAlphaPremulti = 0
	asyncStarted        = 0
	asyncCompleted      = 1
	asyncCanceled       = 2
	recognizeTimeout    = 10 * time.Second
)

type Engine struct {
	thread  *winrt.Thread
	engine  *winrt.Obj
	bitmaps *winrt.Obj
	buffers *winrt.Obj
	maxDim  uint32
}

// New creates an OCR engine for a BCP-47 language tag such as "en-US".
func New(language string) (*Engine, error) {
	t, err := winrt.NewThread()
	if err != nil {
		return nil, err
	}
	e := &Engine{thread: t}
	t.Do(func() { err = e.init(language) })
	if err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

func (e *Engine) init(language string) error {
	statics, err := winrt.ActivationFactory("Windows.Media.Ocr.OcrEngine", &iidOcrEngineStatics)
	if err != nil {
		return err
	}
	defer statics.Release()
	langFactory, err := winrt.ActivationFactory("Windows.Globalization.Language", &iidLanguageFactory)
	if err != nil {
		return err
	}
	defer langFactory.Release()
	tag, err := winrt.NewHString(language)
	if err != nil {
		return err
	}
	defer tag.Free()

	var lang *winrt.Obj
	if err := langFactory.Call(6, uintptr(tag), winrt.Out(&lang)); err != nil {
		return fmt.Errorf("invalid OCR language %q: %w", language, err)
	}
	defer lang.Release()
	if err := statics.Call(6, uintptr(unsafe.Pointer(&e.maxDim))); err != nil {
		return fmt.Errorf("OcrEngine.MaxImageDimension: %w", err)
	}
	if err := statics.Call(9, lang.Ptr(), winrt.Out(&e.engine)); err != nil {
		return fmt.Errorf("OcrEngine.TryCreateFromLanguage: %w", err)
	}
	if e.engine == nil {
		return fmt.Errorf("Windows OCR for %q is not installed", language)
	}
	if e.bitmaps, err = winrt.ActivationFactory("Windows.Graphics.Imaging.SoftwareBitmap", &iidSoftwareBitmapStatics); err != nil {
		return err
	}
	e.buffers, err = winrt.ActivationFactory("Windows.Security.Cryptography.CryptographicBuffer", &iidCryptographicBufferStatic)
	return err
}

func (e *Engine) Recognize(pix []byte, w, h int) (lines []string, err error) {
	if len(pix) != w*h*4 || w <= 0 || h <= 0 {
		return nil, errors.New("invalid image buffer")
	}
	if !e.thread.Do(func() { lines, err = e.recognize(pix, w, h) }) {
		return nil, errors.New("ocr engine closed")
	}
	return lines, err
}

func (e *Engine) Close() {
	e.thread.Do(func() {
		e.engine.Release()
		e.bitmaps.Release()
		e.buffers.Release()
		e.engine, e.bitmaps, e.buffers = nil, nil, nil
	})
	e.thread.Close()
}

func (e *Engine) recognize(pix []byte, w, h int) ([]string, error) {
	if uint32(w) > e.maxDim || uint32(h) > e.maxDim {
		return nil, fmt.Errorf("image %dx%d exceeds OCR max dimension %d; reduce area or scale", w, h, e.maxDim)
	}
	var buf *winrt.Obj
	if err := e.buffers.Call(9, uintptr(len(pix)), uintptr(unsafe.Pointer(&pix[0])), winrt.Out(&buf)); err != nil {
		return nil, fmt.Errorf("CreateFromByteArray: %w", err)
	}
	defer buf.Release()

	var bmp *winrt.Obj
	if err := e.bitmaps.Call(10, buf.Ptr(), bitmapFormatBgra8, uintptr(w), uintptr(h), bitmapAlphaPremulti, winrt.Out(&bmp)); err != nil {
		return nil, fmt.Errorf("CreateCopyWithAlphaFromBuffer: %w", err)
	}
	defer bmp.Release()

	var op *winrt.Obj
	if err := e.engine.Call(6, bmp.Ptr(), winrt.Out(&op)); err != nil {
		return nil, fmt.Errorf("RecognizeAsync: %w", err)
	}
	defer op.Release()
	if err := await(op); err != nil {
		return nil, err
	}

	var result *winrt.Obj
	if err := op.Call(8, winrt.Out(&result)); err != nil {
		return nil, fmt.Errorf("GetResults: %w", err)
	}
	defer result.Release()
	var vec *winrt.Obj
	if err := result.Call(6, winrt.Out(&vec)); err != nil {
		return nil, fmt.Errorf("OcrResult.Lines: %w", err)
	}
	defer vec.Release()
	var size uint32
	if err := vec.Call(7, uintptr(unsafe.Pointer(&size))); err != nil {
		return nil, fmt.Errorf("Lines.Size: %w", err)
	}
	lines := make([]string, 0, size)
	for i := uint32(0); i < size; i++ {
		var line *winrt.Obj
		if err := vec.Call(6, uintptr(i), winrt.Out(&line)); err != nil {
			return nil, fmt.Errorf("Lines.GetAt(%d): %w", i, err)
		}
		var hs winrt.HString
		err := line.Call(7, uintptr(unsafe.Pointer(&hs)))
		line.Release()
		if err != nil {
			return nil, fmt.Errorf("OcrLine.Text: %w", err)
		}
		lines = append(lines, hs.String())
		hs.Free()
	}
	return lines, nil
}

func await(op *winrt.Obj) error {
	info, err := op.QueryInterface(&iidAsyncInfo)
	if err != nil {
		return fmt.Errorf("IAsyncInfo: %w", err)
	}
	defer info.Release()
	deadline := time.Now().Add(recognizeTimeout)
	for {
		var status int32
		if err := info.Call(7, uintptr(unsafe.Pointer(&status))); err != nil {
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
			_ = info.Call(8, uintptr(unsafe.Pointer(&code)))
			return fmt.Errorf("OCR failed: HRESULT 0x%08X", uint32(code))
		}
		if time.Now().After(deadline) {
			_ = info.Call(9)
			return errors.New("OCR timed out")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
