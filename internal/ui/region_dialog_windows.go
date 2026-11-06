package ui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/yanmad27/ace-spy/internal/config"
	"github.com/yanmad27/ace-spy/internal/gamewin"
	"github.com/yanmad27/ace-spy/internal/picker"
	"github.com/yanmad27/ace-spy/internal/screen"
)

func areaText(r config.Region) string {
	if r.Width == 0 || r.Height == 0 {
		return "(not selected)"
	}
	if r.Window != nil {
		return fmt.Sprintf("%s (PID %d): x=%d y=%d %d×%d px in window", r.Window.Exe, r.Window.PID, r.X, r.Y, r.Width, r.Height)
	}
	return fmt.Sprintf("screen: x=%d y=%d %d×%d px", r.X, r.Y, r.Width, r.Height)
}

// bindToWindow converts a screen rectangle to one relative to the game window under
// its centre. It returns false when the area is not over an application window.
func bindToWindow(r *config.Region, rect screen.Rect) bool {
	info, ok := gamewin.AtPoint(rect.X+rect.W/2, rect.Y+rect.H/2)
	if !ok {
		return false
	}
	ox, oy, cw, ch, err := gamewin.ClientOrigin(info.HWND)
	if err != nil {
		return false
	}
	x0, y0 := max(rect.X-ox, 0), max(rect.Y-oy, 0)
	x1, y1 := min(rect.X+rect.W-ox, cw), min(rect.Y+rect.H-oy, ch)
	if x1-x0 < config.MinRegionSide || y1-y0 < config.MinRegionSide {
		return false
	}
	r.Window = info.Target()
	r.X, r.Y, r.Width, r.Height = x0, y0, x1-x0, y1-y0
	return true
}

// regionDialog edits r in place and reports whether the user accepted.
func (a *app) regionDialog(r *config.Region) bool {
	var (
		dlg        *walk.Dialog
		labelEd    *walk.LineEdit
		areaLbl    *walk.Label
		intervalEd *walk.NumberEdit
		scaleEd    *walk.NumberEdit
		enabledCB  *walk.CheckBox
		okBtn      *walk.PushButton
		cancelBtn  *walk.PushButton
	)
	draft := *r

	pick := func() {
		dlg.SetVisible(false)
		a.mw.SetVisible(false)
		time.Sleep(300 * time.Millisecond)
		rect, err := picker.Pick()
		picked := draft
		bound := err == nil && bindToWindow(&picked, rect)
		a.mw.SetVisible(true)
		dlg.SetVisible(true)
		dlg.Activate()
		if errors.Is(err, picker.ErrCanceled) {
			return
		}
		if err != nil {
			walk.MsgBox(dlg, "Pick area", err.Error(), walk.MsgBoxIconError)
			return
		}
		if !bound {
			picked.Window = nil
			picked.X, picked.Y, picked.Width, picked.Height = rect.X, rect.Y, rect.W, rect.H
		}
		draft.Window, draft.X, draft.Y, draft.Width, draft.Height = picked.Window, picked.X, picked.Y, picked.Width, picked.Height
		areaLbl.SetText(areaText(draft))
	}

	accept := func() {
		draft.Label = strings.TrimSpace(labelEd.Text())
		draft.IntervalMs = int(intervalEd.Value())
		draft.Scale = int(scaleEd.Value())
		draft.Enabled = enabledCB.Checked()
		if err := draft.Validate(); err != nil {
			walk.MsgBox(dlg, "Invalid region", err.Error(), walk.MsgBoxIconWarning)
			return
		}
		if a.labelTaken(draft.Label, draft.ID) {
			walk.MsgBox(dlg, "Invalid region", fmt.Sprintf("Label %q is already used.", draft.Label), walk.MsgBoxIconWarning)
			return
		}
		dlg.Accept()
	}

	_, err := Dialog{
		AssignTo:      &dlg,
		Title:         "Region",
		DefaultButton: &okBtn,
		CancelButton:  &cancelBtn,
		MinSize:       Size{Width: 420, Height: 240},
		Layout:        VBox{},
		Children: []Widget{
			Composite{
				Layout: Grid{Columns: 3},
				Children: []Widget{
					Label{Text: "Label"},
					LineEdit{AssignTo: &labelEd, Text: draft.Label, MaxLength: config.MaxLabelLen, ColumnSpan: 2},
					Label{Text: "Area"},
					Label{AssignTo: &areaLbl, Text: areaText(draft)},
					PushButton{Text: "Pick area…", OnClicked: pick},
					Label{Text: "Interval (ms)"},
					NumberEdit{AssignTo: &intervalEd, Decimals: 0, MinValue: config.MinIntervalMs, MaxValue: config.MaxIntervalMs, Increment: 250, Value: float64(draft.IntervalMs), ColumnSpan: 2},
					Label{Text: "Upscale", ToolTipText: "Small game fonts OCR better at ×2 or ×3"},
					NumberEdit{AssignTo: &scaleEd, Decimals: 0, MinValue: config.MinScale, MaxValue: config.MaxScale, Value: float64(draft.Scale), ColumnSpan: 2},
					Label{Text: ""},
					CheckBox{AssignTo: &enabledCB, Text: "Enabled", Checked: draft.Enabled, ColumnSpan: 2},
				},
			},
			VSpacer{},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					HSpacer{},
					PushButton{AssignTo: &okBtn, Text: "OK", OnClicked: accept},
					PushButton{AssignTo: &cancelBtn, Text: "Cancel", OnClicked: func() { dlg.Cancel() }},
				},
			},
		},
	}.Run(a.mw)
	if err != nil {
		a.errorBox("Region dialog", err)
		return false
	}
	if dlg.Result() != walk.DlgCmdOK {
		return false
	}
	*r = draft
	return true
}
