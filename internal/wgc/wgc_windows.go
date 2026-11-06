// Package wgc captures the contents of one window with Windows.Graphics.Capture
// (Windows 10 1903+). Unlike a screen copy it sees the window even when other
// windows cover it, and unlike PrintWindow it works for DirectX games.
package wgc

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"

	"github.com/yanmad27/ace-spy/internal/winrt"
)

var (
	d3d11                                    = windows.NewLazySystemDLL("d3d11.dll")
	procD3D11CreateDevice                    = d3d11.NewProc("D3D11CreateDevice")
	procCreateDirect3D11DeviceFromDXGIDevice = d3d11.NewProc("CreateDirect3D11DeviceFromDXGIDevice")

	iidDXGIDevice                 = winrt.GUID("{54EC77FA-1377-44E6-8C32-88FD5F44C84C}")
	iidDirect3DDevice             = winrt.GUID("{A37624AB-8D5F-4650-9D3E-9EAE3D9BC670}")
	iidGraphicsCaptureItemInterop = winrt.GUID("{3628E81B-3CAC-4C60-B7F4-23CE0E0C3356}")
	iidGraphicsCaptureItem        = winrt.GUID("{79C3F95B-31F7-4EC2-A464-632EF5D30760}")
	iidFramePoolStatics2          = winrt.GUID("{589B103F-6BBC-5DF5-A991-02E28B3B66D5}")
	iidCaptureSession2            = winrt.GUID("{2C39AE40-7D2E-5044-804E-8B6799D4CF9E}")
	iidCaptureSession3            = winrt.GUID("{F2CDD966-22AE-5EA1-9596-3A289344C3BE}")
	iidDxgiInterfaceAccess        = winrt.GUID("{A9B3D012-3DF2-4EE3-B8D1-8695F457D3C1}")
	iidTexture2D                  = winrt.GUID("{6F15AAF2-D208-4E89-9AB4-489535D34F9C}")
	iidClosable                   = winrt.GUID("{30D5A829-7FA4-4026-83BB-D75BAE4EA99E}")
)

const (
	d3dDriverHardware    = 1
	d3dDriverWarp        = 5
	d3dCreateBGRASupport = 0x20
	d3dSDKVersion        = 7
	pixelFormatBGRA8     = 87
	usageStaging         = 3
	cpuAccessRead        = 0x20000
	mapRead              = 1
	framePoolBuffers     = 2
	firstFrameTimeout    = 2 * time.Second
)

// Vtable slots.
const (
	slotDeviceCreateTexture2D = 5
	slotDeviceGetContext      = 40
	slotCtxMap                = 14
	slotCtxUnmap              = 15
	slotCtxCopyResource       = 47
	slotTexGetDesc            = 10
	slotInteropCreateForWnd   = 3
	slotItemSize              = 7
	slotStaticsCreateFree     = 6
	slotPoolRecreate          = 6
	slotPoolTryGetNextFrame   = 7
	slotPoolCreateSession     = 10
	slotSessionStart          = 6
	slotSetCursorCapture      = 7
	slotSetBorderRequired     = 7
	slotFrameSurface          = 6
	slotFrameContentSize      = 8
	slotDxgiGetInterface      = 3
	slotClose                 = 6
)

type sizeInt32 struct{ W, H int32 }

func (s sizeInt32) packed() uintptr { return uintptr(uint32(s.W)) | uintptr(uint32(s.H))<<32 }

type texture2DDesc struct {
	Width, Height, MipLevels, ArraySize uint32
	Format                              uint32
	SampleCount, SampleQuality          uint32
	Usage, BindFlags, CPUAccessFlags    uint32
	MiscFlags                           uint32
}

type mappedSubresource struct {
	Data       uintptr
	RowPitch   uint32
	DepthPitch uint32
}

// Capturer streams frames of one window. All WinRT/D3D calls run on its own thread.
type Capturer struct {
	thread   *winrt.Thread
	device   *winrt.Obj
	ctx      *winrt.Obj
	rtDevice *winrt.Obj
	item     *winrt.Obj
	pool     *winrt.Obj
	session  *winrt.Obj
	poolSize sizeInt32
	staging  *winrt.Obj
	stDesc   texture2DDesc
	frame    []byte
	fw, fh   int
}

func New(hwnd win.HWND) (*Capturer, error) {
	t, err := winrt.NewThread()
	if err != nil {
		return nil, err
	}
	c := &Capturer{thread: t}
	t.Do(func() { err = c.init(hwnd) })
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("window capture unavailable: %w", err)
	}
	return c, nil
}

func (c *Capturer) init(hwnd win.HWND) error {
	if err := c.createDevice(); err != nil {
		return err
	}
	interop, err := winrt.ActivationFactory("Windows.Graphics.Capture.GraphicsCaptureItem", &iidGraphicsCaptureItemInterop)
	if err != nil {
		return err
	}
	defer interop.Release()
	if err := interop.Call(slotInteropCreateForWnd, uintptr(hwnd), uintptr(unsafe.Pointer(&iidGraphicsCaptureItem)), winrt.Out(&c.item)); err != nil {
		return fmt.Errorf("CreateForWindow: %w", err)
	}
	if err := c.item.Call(slotItemSize, uintptr(unsafe.Pointer(&c.poolSize))); err != nil {
		return fmt.Errorf("GraphicsCaptureItem.Size: %w", err)
	}
	statics, err := winrt.ActivationFactory("Windows.Graphics.Capture.Direct3D11CaptureFramePool", &iidFramePoolStatics2)
	if err != nil {
		return err
	}
	defer statics.Release()
	if err := statics.Call(slotStaticsCreateFree, c.rtDevice.Ptr(), pixelFormatBGRA8, framePoolBuffers, c.poolSize.packed(), winrt.Out(&c.pool)); err != nil {
		return fmt.Errorf("CreateFreeThreaded: %w", err)
	}
	if err := c.pool.Call(slotPoolCreateSession, c.item.Ptr(), winrt.Out(&c.session)); err != nil {
		return fmt.Errorf("CreateCaptureSession: %w", err)
	}
	if s2, err := c.session.QueryInterface(&iidCaptureSession2); err == nil {
		_ = s2.Call(slotSetCursorCapture, 0)
		s2.Release()
	}
	if s3, err := c.session.QueryInterface(&iidCaptureSession3); err == nil {
		_ = s3.Call(slotSetBorderRequired, 0)
		s3.Release()
	}
	if err := c.session.Call(slotSessionStart); err != nil {
		return fmt.Errorf("StartCapture: %w", err)
	}
	return nil
}

func (c *Capturer) createDevice() error {
	var lastErr error
	for _, driver := range []uintptr{d3dDriverHardware, d3dDriverWarp} {
		var dev, ctx *winrt.Obj
		hr, _, _ := procD3D11CreateDevice.Call(0, driver, 0, d3dCreateBGRASupport, 0, 0, d3dSDKVersion,
			winrt.Out(&dev), 0, winrt.Out(&ctx))
		if int32(hr) < 0 {
			lastErr = fmt.Errorf("D3D11CreateDevice: HRESULT 0x%08X", uint32(hr))
			continue
		}
		c.device, c.ctx = dev, ctx
		break
	}
	if c.device == nil {
		return lastErr
	}
	dxgi, err := c.device.QueryInterface(&iidDXGIDevice)
	if err != nil {
		return fmt.Errorf("IDXGIDevice: %w", err)
	}
	defer dxgi.Release()
	var insp *winrt.Obj
	hr, _, _ := procCreateDirect3D11DeviceFromDXGIDevice.Call(dxgi.Ptr(), winrt.Out(&insp))
	if int32(hr) < 0 {
		return fmt.Errorf("CreateDirect3D11DeviceFromDXGIDevice: HRESULT 0x%08X", uint32(hr))
	}
	defer insp.Release()
	c.rtDevice, err = insp.QueryInterface(&iidDirect3DDevice)
	return err
}

// Frame returns the latest full-window frame (BGRA, top-down). The frame covers the
// window's DWM extended frame bounds. When the window has not changed since the last
// call, the previous frame is returned.
func (c *Capturer) Frame() (pix []byte, w, h int, err error) {
	if !c.thread.Do(func() { err = c.poll() }) {
		return nil, 0, 0, errors.New("capture closed")
	}
	if err != nil {
		return nil, 0, 0, err
	}
	return c.frame, c.fw, c.fh, nil
}

func (c *Capturer) poll() error {
	deadline := time.Now().Add(firstFrameTimeout)
	for {
		var latest *winrt.Obj
		for {
			var f *winrt.Obj
			if err := c.pool.Call(slotPoolTryGetNextFrame, winrt.Out(&f)); err != nil {
				latest.Release()
				return fmt.Errorf("TryGetNextFrame: %w", err)
			}
			if f == nil {
				break
			}
			latest.Release()
			latest = f
		}
		if latest != nil {
			err := c.read(latest)
			latest.Release()
			return err
		}
		if c.frame != nil {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("no frame from game window (is it minimized?)")
		}
		time.Sleep(15 * time.Millisecond)
	}
}

func (c *Capturer) read(frame *winrt.Obj) error {
	var content sizeInt32
	if err := frame.Call(slotFrameContentSize, uintptr(unsafe.Pointer(&content))); err != nil {
		return fmt.Errorf("ContentSize: %w", err)
	}
	var surface *winrt.Obj
	if err := frame.Call(slotFrameSurface, winrt.Out(&surface)); err != nil {
		return fmt.Errorf("Surface: %w", err)
	}
	defer surface.Release()
	access, err := surface.QueryInterface(&iidDxgiInterfaceAccess)
	if err != nil {
		return fmt.Errorf("IDirect3DDxgiInterfaceAccess: %w", err)
	}
	defer access.Release()
	var tex *winrt.Obj
	if err := access.Call(slotDxgiGetInterface, uintptr(unsafe.Pointer(&iidTexture2D)), winrt.Out(&tex)); err != nil {
		return fmt.Errorf("GetInterface(ID3D11Texture2D): %w", err)
	}
	defer tex.Release()

	var desc texture2DDesc
	_ = tex.Call(slotTexGetDesc, uintptr(unsafe.Pointer(&desc))) // returns void
	if err := c.ensureStaging(desc); err != nil {
		return err
	}
	_ = c.ctx.Call(slotCtxCopyResource, c.staging.Ptr(), tex.Ptr()) // returns void

	var m mappedSubresource
	if err := c.ctx.Call(slotCtxMap, c.staging.Ptr(), 0, mapRead, 0, uintptr(unsafe.Pointer(&m))); err != nil {
		return fmt.Errorf("Map: %w", err)
	}
	w := int(min(uint32(max(content.W, 0)), desc.Width))
	h := int(min(uint32(max(content.H, 0)), desc.Height))
	if len(c.frame) != w*h*4 {
		c.frame = make([]byte, w*h*4)
	}
	src := unsafe.Slice(*(**byte)(unsafe.Pointer(&m.Data)), int(m.RowPitch)*h)
	for y := 0; y < h; y++ {
		copy(c.frame[y*w*4:(y+1)*w*4], src[y*int(m.RowPitch):])
	}
	_ = c.ctx.Call(slotCtxUnmap, c.staging.Ptr(), 0) // returns void
	c.fw, c.fh = w, h

	if content != c.poolSize && content.W > 0 && content.H > 0 {
		if err := c.pool.Call(slotPoolRecreate, c.rtDevice.Ptr(), pixelFormatBGRA8, framePoolBuffers, content.packed()); err == nil {
			c.poolSize = content
		}
	}
	return nil
}

func (c *Capturer) ensureStaging(desc texture2DDesc) error {
	if c.staging != nil && c.stDesc.Width == desc.Width && c.stDesc.Height == desc.Height && c.stDesc.Format == desc.Format {
		return nil
	}
	c.staging.Release()
	c.staging = nil
	sd := desc
	sd.MipLevels, sd.ArraySize = 1, 1
	sd.SampleCount, sd.SampleQuality = 1, 0
	sd.Usage, sd.BindFlags, sd.CPUAccessFlags, sd.MiscFlags = usageStaging, 0, cpuAccessRead, 0
	if err := c.device.Call(slotDeviceCreateTexture2D, uintptr(unsafe.Pointer(&sd)), 0, winrt.Out(&c.staging)); err != nil {
		return fmt.Errorf("CreateTexture2D(staging): %w", err)
	}
	c.stDesc = sd
	return nil
}

func (c *Capturer) Close() {
	c.thread.Do(func() {
		for _, o := range []*winrt.Obj{c.session, c.pool} {
			if o == nil {
				continue
			}
			if cl, err := o.QueryInterface(&iidClosable); err == nil {
				_ = cl.Call(slotClose)
				cl.Release()
			}
		}
		for _, o := range []*winrt.Obj{c.staging, c.session, c.pool, c.item, c.rtDevice, c.ctx, c.device} {
			o.Release()
		}
		c.staging, c.session, c.pool, c.item, c.rtDevice, c.ctx, c.device = nil, nil, nil, nil, nil, nil, nil
	})
	c.thread.Close()
}
