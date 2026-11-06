package ui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/yanmad27/ace-spy/internal/config"
	"github.com/yanmad27/ace-spy/internal/pusher"
	"github.com/yanmad27/ace-spy/internal/runner"
	"github.com/yanmad27/ace-spy/internal/screen"
	"github.com/yanmad27/ace-spy/internal/winocr"
)

const maxLogLines = 400

type app struct {
	version string
	cfgPath string
	cfg     config.Config

	mw        *walk.MainWindow
	serverEd  *walk.LineEdit
	tokenEd   *walk.LineEdit
	clientEd  *walk.LineEdit
	langEd    *walk.LineEdit
	table     *walk.TableView
	model     *regionModel
	logEdit   *walk.TextEdit
	startBtn  *walk.PushButton
	editables []walk.Widget

	logLines []string

	engineMu   sync.Mutex
	engine     *winocr.Engine
	engineLang string

	cancel  context.CancelFunc
	stopped chan struct{}
}

func Run(cfgPath, version string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	a := &app{version: version, cfgPath: cfgPath, cfg: cfg, model: &regionModel{rows: cfg.Regions, last: map[string]string{}}}
	return a.run()
}

func (a *app) run() error {
	var addBtn, editBtn, delBtn, testBtn *walk.PushButton
	err := MainWindow{
		AssignTo: &a.mw,
		Title:    "Ace Spy " + a.version,
		MinSize:  Size{Width: 720, Height: 480},
		Size:     Size{Width: 900, Height: 620},
		Layout:   VBox{},
		Children: []Widget{
			GroupBox{
				Title:  "Server",
				Layout: Grid{Columns: 4},
				Children: []Widget{
					Label{Text: "Server URL"},
					LineEdit{AssignTo: &a.serverEd, Text: a.cfg.ServerURL},
					Label{Text: "Ingest token"},
					LineEdit{AssignTo: &a.tokenEd, Text: a.cfg.Token, PasswordMode: true},
					Label{Text: "Client name"},
					LineEdit{AssignTo: &a.clientEd, Text: a.cfg.ClientName},
					Label{Text: "OCR language"},
					LineEdit{AssignTo: &a.langEd, Text: a.cfg.Language, ToolTipText: "BCP-47 tag, e.g. vi"},
				},
			},
			TableView{
				AssignTo:         &a.table,
				AlternatingRowBG: true,
				Columns: []TableViewColumn{
					{Title: "Label", Width: 130},
					{Title: "Area (x, y, w×h)", Width: 170},
					{Title: "Interval", Width: 70},
					{Title: "Scale", Width: 50},
					{Title: "On", Width: 40},
					{Title: "Last OCR", Width: 380},
				},
				Model:           a.model,
				OnItemActivated: func() { a.editSelected() },
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					PushButton{AssignTo: &addBtn, Text: "Add region…", OnClicked: a.addRegion},
					PushButton{AssignTo: &editBtn, Text: "Edit…", OnClicked: a.editSelected},
					PushButton{AssignTo: &delBtn, Text: "Remove", OnClicked: a.removeSelected},
					PushButton{AssignTo: &testBtn, Text: "Test OCR", OnClicked: a.testSelected},
					HSpacer{},
					PushButton{AssignTo: &a.startBtn, Text: "Start", MinSize: Size{Width: 110}, OnClicked: a.toggle},
				},
			},
			TextEdit{AssignTo: &a.logEdit, ReadOnly: true, VScroll: true, MinSize: Size{Height: 140}},
		},
	}.Create()
	if err != nil {
		return err
	}
	a.editables = []walk.Widget{a.serverEd, a.tokenEd, a.clientEd, a.langEd, addBtn, editBtn, delBtn}

	a.mw.Closing().Attach(func(canceled *bool, _ walk.CloseReason) {
		a.stop()
		if a.stopped != nil {
			select {
			case <-a.stopped:
			case <-time.After(3 * time.Second):
			}
		}
		a.readFields()
		if err := config.Save(a.cfgPath, a.cfg); err != nil {
			log.Printf("save config: %v", err)
		}
		a.engineMu.Lock()
		if a.engine != nil {
			a.engine.Close()
		}
		a.engineMu.Unlock()
	})

	a.logf("Config: %s", a.cfgPath)
	a.mw.Run()
	return nil
}

func (a *app) readFields() {
	a.cfg.ServerURL = strings.TrimSpace(a.serverEd.Text())
	a.cfg.Token = strings.TrimSpace(a.tokenEd.Text())
	a.cfg.ClientName = strings.TrimSpace(a.clientEd.Text())
	a.cfg.Language = strings.TrimSpace(a.langEd.Text())
	a.cfg.Regions = a.model.rows
}

func (a *app) save() {
	a.readFields()
	if err := config.Save(a.cfgPath, a.cfg); err != nil {
		a.logf("Save config failed: %v", err)
	}
}

func (a *app) logf(format string, args ...any) {
	line := time.Now().Format("15:04:05 ") + fmt.Sprintf(format, args...)
	log.Print(line)
	a.logLines = append(a.logLines, line)
	if len(a.logLines) > maxLogLines {
		a.logLines = a.logLines[len(a.logLines)-maxLogLines:]
		a.logEdit.SetText(strings.Join(a.logLines, "\r\n") + "\r\n")
		return
	}
	a.logEdit.AppendText(line + "\r\n")
}

func (a *app) asyncLogf(format string, args ...any) {
	a.mw.Synchronize(func() { a.logf(format, args...) })
}

func (a *app) errorBox(title string, err error) {
	walk.MsgBox(a.mw, title, err.Error(), walk.MsgBoxIconError)
}

func (a *app) ocrEngine() (*winocr.Engine, error) {
	lang := strings.TrimSpace(a.langEd.Text())
	a.engineMu.Lock()
	defer a.engineMu.Unlock()
	if a.engine != nil && a.engineLang == lang {
		return a.engine, nil
	}
	if a.engine != nil {
		a.engine.Close()
		a.engine = nil
	}
	e, err := winocr.New(lang)
	if err != nil {
		return nil, err
	}
	a.engine, a.engineLang = e, lang
	return e, nil
}

func (a *app) selected() int {
	i := a.table.CurrentIndex()
	if i < 0 || i >= len(a.model.rows) {
		return -1
	}
	return i
}

func (a *app) labelTaken(label, exceptID string) bool {
	key := strings.ToLower(strings.TrimSpace(label))
	for _, r := range a.model.rows {
		if r.ID != exceptID && strings.ToLower(strings.TrimSpace(r.Label)) == key {
			return true
		}
	}
	return false
}

func (a *app) addRegion() {
	r := config.NewRegion()
	r.Label = fmt.Sprintf("Region %d", len(a.model.rows)+1)
	if ok := a.regionDialog(&r); !ok {
		return
	}
	a.model.rows = append(a.model.rows, r)
	a.model.PublishRowsReset()
	a.table.SetCurrentIndex(len(a.model.rows) - 1)
	a.save()
}

func (a *app) editSelected() {
	if a.cancel != nil {
		return
	}
	i := a.selected()
	if i < 0 {
		return
	}
	r := a.model.rows[i]
	if ok := a.regionDialog(&r); !ok {
		return
	}
	a.model.rows[i] = r
	a.model.PublishRowChanged(i)
	a.save()
}

func (a *app) removeSelected() {
	i := a.selected()
	if i < 0 {
		return
	}
	label := a.model.rows[i].Label
	if walk.MsgBox(a.mw, "Remove region", fmt.Sprintf("Remove region %q?", label), walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
		return
	}
	a.model.rows = append(a.model.rows[:i:i], a.model.rows[i+1:]...)
	a.model.PublishRowsReset()
	a.save()
}

func (a *app) testSelected() {
	i := a.selected()
	if i < 0 {
		walk.MsgBox(a.mw, "Test OCR", "Select a region first.", walk.MsgBoxIconInformation)
		return
	}
	r := a.model.rows[i]
	e, err := a.ocrEngine()
	if err != nil {
		a.errorBox("OCR unavailable", err)
		return
	}
	g, err := screen.NewGrabber(r.X, r.Y, r.Width, r.Height, r.Scale)
	if err != nil {
		a.errorBox("Capture failed", err)
		return
	}
	defer g.Close()
	pix, w, h, err := g.Grab()
	if err != nil {
		a.errorBox("Capture failed", err)
		return
	}
	start := time.Now()
	lines, err := e.Recognize(pix, w, h)
	if err != nil {
		a.errorBox("OCR failed", err)
		return
	}
	text := strings.Join(lines, "\r\n")
	if text == "" {
		text = "(no text recognized — try a larger scale or a tighter area)"
	}
	walk.MsgBox(a.mw, fmt.Sprintf("OCR: %s (%d lines, %s)", r.Label, len(lines), time.Since(start).Round(time.Millisecond)), text, walk.MsgBoxOK)
}

func (a *app) toggle() {
	if a.cancel != nil {
		a.stop()
		return
	}
	a.start()
}

func (a *app) start() {
	a.readFields()
	if err := a.cfg.Validate(); err != nil {
		a.errorBox("Invalid configuration", err)
		return
	}
	enabled := 0
	for _, r := range a.cfg.Regions {
		if r.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		a.errorBox("Nothing to capture", errors.New("add at least one enabled region"))
		return
	}
	if err := config.Save(a.cfgPath, a.cfg); err != nil {
		a.logf("Save config failed: %v", err)
	}
	e, err := a.ocrEngine()
	if err != nil {
		a.errorBox("OCR unavailable", err)
		return
	}

	labels := map[string]string{}
	for _, r := range a.cfg.Regions {
		labels[r.ID] = r.Label
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := pusher.New(a.cfg.ServerURL, a.cfg.Token, a.cfg.ClientName, func(s string) { a.asyncLogf("%s", s) })
	rn := runner.New(
		func(r config.Region) (runner.Grabber, error) {
			return screen.NewGrabber(r.X, r.Y, r.Width, r.Height, r.Scale)
		},
		e, p,
		runner.Events{
			OnLines: func(id string, visible, fresh []string) {
				a.mw.Synchronize(func() {
					a.model.setLast(id, visible)
					for _, l := range fresh {
						a.logf("[%s] %s", labels[id], l)
					}
				})
			},
			OnError: func(id string, err error) { a.asyncLogf("[%s] %v", labels[id], err) },
		},
	)

	stopped := make(chan struct{})
	regions := append([]config.Region(nil), a.cfg.Regions...)
	go p.Run(ctx)
	go func() {
		defer close(stopped)
		rn.Run(ctx, regions)
	}()
	a.cancel, a.stopped = cancel, stopped
	a.setRunning(true)
	a.logf("Started %d region(s) → %s", enabled, a.cfg.ServerURL)
}

func (a *app) stop() {
	if a.cancel == nil {
		return
	}
	a.cancel()
	a.cancel = nil
	a.setRunning(false)
	a.logf("Stopped")
}

func (a *app) setRunning(running bool) {
	for _, w := range a.editables {
		w.SetEnabled(!running)
	}
	if running {
		a.startBtn.SetText("Stop")
	} else {
		a.startBtn.SetText("Start")
	}
}

type regionModel struct {
	walk.TableModelBase
	rows []config.Region
	last map[string]string
}

func (m *regionModel) RowCount() int { return len(m.rows) }

func (m *regionModel) Value(row, col int) interface{} {
	r := m.rows[row]
	switch col {
	case 0:
		return r.Label
	case 1:
		return fmt.Sprintf("%d, %d, %d×%d", r.X, r.Y, r.Width, r.Height)
	case 2:
		return fmt.Sprintf("%d ms", r.IntervalMs)
	case 3:
		return fmt.Sprintf("×%d", r.Scale)
	case 4:
		if r.Enabled {
			return "✓"
		}
		return ""
	case 5:
		return m.last[r.ID]
	}
	return ""
}

func (m *regionModel) setLast(id string, visible []string) {
	text := ""
	if len(visible) > 0 {
		text = visible[len(visible)-1]
	}
	m.last[id] = text
	for i, r := range m.rows {
		if r.ID == id {
			m.PublishRowChanged(i)
			return
		}
	}
}
