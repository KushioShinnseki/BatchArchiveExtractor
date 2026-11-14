package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

var (
	user32        = syscall.NewLazyDLL("user32.dll")
	kernel32      = syscall.NewLazyDLL("kernel32.dll")
	gdi32         = syscall.NewLazyDLL("gdi32.dll")
	shell32       = syscall.NewLazyDLL("shell32.dll")
	ole32         = syscall.NewLazyDLL("ole32.dll")
	createWindow  = user32.NewProc("CreateWindowExW")
	defWindowProc = user32.NewProc("DefWindowProcW")
	sendMessage   = user32.NewProc("SendMessageW")
	setText       = user32.NewProc("SetWindowTextW")
	enableWindow  = user32.NewProc("EnableWindow")
	moveWindow    = user32.NewProc("MoveWindow")
	getClientRect = user32.NewProc("GetClientRect")
	activeUI      *UI
)

func wide(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(strings.ReplaceAll(s, "\x00", ""))
	return p
}
func callText(handle uintptr, text string) { setText.Call(handle, uintptr(unsafe.Pointer(wide(text)))) }
func getText(handle uintptr) string {
	n, _, _ := user32.NewProc("GetWindowTextLengthW").Call(handle)
	buf := make([]uint16, n+1)
	user32.NewProc("GetWindowTextW").Call(handle, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}
func message(handle uintptr, text string) {
	user32.NewProc("MessageBoxW").Call(handle, uintptr(unsafe.Pointer(wide(text))), uintptr(unsafe.Pointer(wide("批量解压助手"))), 0x10)
}

type point struct{ X, Y int32 }
type rect struct{ Left, Top, Right, Bottom int32 }
type winMessage struct {
	Hwnd           uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Pt             point
	Private        uint32
}
type windowClass struct {
	Style                              uint32
	WndProc                            uintptr
	ClsExtra, WndExtra                 int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
}
type browseInfo struct {
	Owner, Root, DisplayName, Title uintptr
	Flags                           uint32
	Callback, Param                 uintptr
	Image                           int32
}
type UI struct {
	Window, Font, Instance uintptr
	Controls               map[string]uintptr
	Events                 chan Event
	Cancel                 context.CancelFunc
	Running                bool
	Report, Engine         string
	Scale                  float64
	Log                    string
}

const (
	idBrowse    = 101
	idStart     = 102
	idStop      = 103
	idReport    = 104
	wmCommand   = 0x111
	wmDestroy   = 2
	wmClose     = 0x10
	wmSize      = 5
	wmTimer     = 0x113
	wmGetMinMax = 0x24
)

func (u *UI) px(n int) int { return int(float64(n) * u.Scale) }
func (u *UI) add(name, class, text string, style uint32, id int) {
	h, _, _ := createWindow.Call(0, uintptr(unsafe.Pointer(wide(class))), uintptr(unsafe.Pointer(wide(text))), uintptr(style|0x50000000), 0, 0, 0, 0, u.Window, uintptr(id), u.Instance, 0)
	if h == 0 {
		panic("无法创建界面控件：" + name)
	}
	u.Controls[name] = h
	sendMessage.Call(h, 0x30, u.Font, 1)
}
func (u *UI) place(name string, x, y, w, h int) {
	moveWindow.Call(u.Controls[name], uintptr(u.px(x)), uintptr(u.px(y)), uintptr(u.px(w)), uintptr(u.px(h)), 1)
}
func (u *UI) layout() {
	if len(u.Controls) == 0 {
		return
	}
	var r rect
	getClientRect.Call(u.Window, uintptr(unsafe.Pointer(&r)))
	w, h := int(float64(r.Right)/u.Scale), int(float64(r.Bottom)/u.Scale)
	if w < 740 || h < 540 {
		var outer rect
		user32.NewProc("GetWindowRect").Call(u.Window, uintptr(unsafe.Pointer(&outer)))
		width := int(outer.Right-outer.Left) + max(0, u.px(740)-int(r.Right))
		height := int(outer.Bottom-outer.Top) + max(0, u.px(540)-int(r.Bottom))
		user32.NewProc("SetWindowPos").Call(u.Window, 0, 0, 0, uintptr(width), uintptr(height), 0x16)
		return
	}
	u.place("title", 24, 20, w-48, 32)
	u.place("subtitle", 24, 57, w-48, 24)
	u.place("path", 24, 99, w-182, 32)
	u.place("browse", w-146, 98, 122, 34)
	u.place("passwordLabel", 24, 149, 152, 24)
	u.place("password", 178, 143, 240, 30)
	u.place("hint", 24, 188, w-48, 48)
	u.place("start", 24, 244, 124, 36)
	u.place("stop", 160, 244, 100, 36)
	u.place("report", w-174, 244, 150, 36)
	u.place("progress", 24, 301, w-48, 16)
	u.place("status", 24, 329, w-48, 44)
	u.place("log", 24, 381, w-48, h-405)
}
func (u *UI) write(text string) {
	u.Log += text + "\r\n"
	if len(u.Log) > 150000 {
		u.Log = u.Log[len(u.Log)-120000:]
	}
	callText(u.Controls["log"], u.Log)
	sendMessage.Call(u.Controls["log"], 0xB1, ^uintptr(0), ^uintptr(0))
	sendMessage.Call(u.Controls["log"], 0xB7, 0, 0)
}
func (u *UI) setRunning(running bool) {
	u.Running = running
	value := uintptr(1)
	if running {
		value = 0
	}
	for _, name := range []string{"path", "browse", "password", "start"} {
		enableWindow.Call(u.Controls[name], value)
	}
	stop := uintptr(0)
	if running {
		stop = 1
	}
	enableWindow.Call(u.Controls["stop"], stop)
	report := uintptr(0)
	if u.Report != "" && !running {
		report = 1
	}
	enableWindow.Call(u.Controls["report"], report)
}
func (u *UI) choose() {
	buf := make([]uint16, 32768)
	title := wide("选择需要递归解压的目录")
	bi := browseInfo{Owner: u.Window, Title: uintptr(unsafe.Pointer(title)), DisplayName: uintptr(unsafe.Pointer(&buf[0])), Flags: 0x41}
	item, _, _ := shell32.NewProc("SHBrowseForFolderW").Call(uintptr(unsafe.Pointer(&bi)))
	runtime.KeepAlive(title)
	if item == 0 {
		return
	}
	defer ole32.NewProc("CoTaskMemFree").Call(item)
	ok, _, _ := shell32.NewProc("SHGetPathFromIDListW").Call(item, uintptr(unsafe.Pointer(&buf[0])))
	if ok != 0 {
		callText(u.Controls["path"], syscall.UTF16ToString(buf))
	}
}
func (u *UI) start() {
	root := strings.TrimSpace(getText(u.Controls["path"]))
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		message(u.Window, "请先选择一个存在的目录。")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	u.Cancel = cancel
	u.Report = ""
	u.setRunning(true)
	sendMessage.Call(u.Controls["progress"], 0x402, 0, 0)
	callText(u.Controls["status"], "正在扫描子文件夹…")
	u.write("\r\n开始：" + root)
	password := getText(u.Controls["password"])
	go func() {
		x := &Extractor{Root: root, Engine: u.Engine, Password: password, Ctx: ctx, Notify: func(e Event) { u.Events <- e }}
		if err := x.Run(); err != nil {
			u.Events <- Event{Kind: "error", Message: err.Error()}
		}
		u.Events <- Event{Kind: "idle", Report: x.Report}
	}()
}
func (u *UI) stop() {
	if u.Cancel != nil {
		u.Cancel()
	}
	enableWindow.Call(u.Controls["stop"], 0)
	callText(u.Controls["status"], "正在停止，保留当前源包；请等待清理完成…")
}
func (u *UI) poll() {
	for {
		select {
		case e := <-u.Events:
			switch e.Kind {
			case "scanned":
				u.Report = e.Report
				sendMessage.Call(u.Controls["progress"], 0x406, 0, uintptr(max(1, e.Total)))
				u.write(fmt.Sprintf("发现 %d 个压缩包。", e.Total))
			case "processing":
				callText(u.Controls["status"], fmt.Sprintf("[%d/%d] %s", e.Current, e.Total, e.Message))
			case "result":
				sendMessage.Call(u.Controls["progress"], 0x402, uintptr(e.Current), 0)
				u.write(fmt.Sprintf("[%s] %s\r\n%s\r\n", e.Result.Status, e.Result.Archive, strings.ReplaceAll(e.Result.Message, "\n", "\r\n")))
			case "log":
				u.write(e.Message)
			case "done":
				callText(u.Controls["status"], e.Message)
				u.write(e.Message)
			case "error":
				callText(u.Controls["status"], "已停止："+e.Message)
				u.write("错误：" + e.Message)
			case "idle":
				u.Report = e.Report
				u.setRunning(false)
			}
		default:
			return
		}
	}
}

func windowProc(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	u := activeUI
	if u != nil {
		switch msg {
		case wmCommand:
			switch int(wparam & 0xffff) {
			case idBrowse:
				u.choose()
			case idStart:
				if !u.Running {
					u.start()
				}
			case idStop:
				u.stop()
			case idReport:
				if u.Report != "" {
					shell32.NewProc("ShellExecuteW").Call(u.Window, uintptr(unsafe.Pointer(wide("open"))), uintptr(unsafe.Pointer(wide(u.Report))), 0, 0, 1)
				}
			}
			return 0
		case wmSize:
			u.layout()
			return 0
		case wmTimer:
			u.poll()
			return 0
		case wmClose:
			if u.Running {
				u.stop()
				u.write("请等待停止完成后再关闭窗口。")
				return 0
			}
		case wmDestroy:
			user32.NewProc("PostQuitMessage").Call(0)
			return 0
		}
	}
	result, _, _ := defWindowProc.Call(hwnd, uintptr(msg), wparam, lparam)
	return result
}

func runUI(engine string, smoke bool) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ole32.NewProc("CoInitializeEx").Call(0, 2)
	defer ole32.NewProc("CoUninitialize").Call()
	// DPI aware layout keeps controls readable at Windows display scaling settings.
	user32.NewProc("SetProcessDPIAware").Call()
	dpi := uintptr(96)
	proc := user32.NewProc("GetDpiForSystem")
	if proc.Find() == nil {
		dpi, _, _ = proc.Call()
	}
	u := &UI{Engine: engine, Scale: float64(dpi) / 96, Controls: map[string]uintptr{}, Events: make(chan Event, 128)}
	activeUI = u
	u.Instance, _, _ = kernel32.NewProc("GetModuleHandleW").Call(0)
	fontHeight := int32(-u.px(16))
	u.Font, _, _ = gdi32.NewProc("CreateFontW").Call(uintptr(fontHeight), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(wide("Microsoft YaHei UI"))))
	defer gdi32.NewProc("DeleteObject").Call(u.Font)
	common := struct{ Size, Classes uint32 }{8, 0x20}
	syscall.NewLazyDLL("comctl32.dll").NewProc("InitCommonControlsEx").Call(uintptr(unsafe.Pointer(&common)))
	cursor, _, _ := user32.NewProc("LoadCursorW").Call(0, 32512)
	icon, _, _ := user32.NewProc("LoadIconW").Call(0, 32512)
	class := windowClass{WndProc: syscall.NewCallback(windowProc), Instance: u.Instance, Cursor: cursor, Icon: icon, Background: 6, ClassName: wide("BatchArchiveExtractorGo")}
	atom, _, err := user32.NewProc("RegisterClassW").Call(uintptr(unsafe.Pointer(&class)))
	if atom == 0 {
		return err
	}
	u.Window, _, err = createWindow.Call(0, uintptr(unsafe.Pointer(class.ClassName)), uintptr(unsafe.Pointer(wide("批量解压助手 · Go"))), 0x00CF0000|0x02000000, 0x80000000, 0x80000000, uintptr(u.px(960)), uintptr(u.px(740)), 0, 0, u.Instance, 0)
	if u.Window == 0 {
		return err
	}
	u.add("title", "STATIC", "批量解压助手", 0, 0)
	u.add("subtitle", "STATIC", "递归扫描子文件夹 · 解压到原目录 · 重名不覆盖", 0, 0)
	u.add("path", "EDIT", "", 0x00800000|0x80|0x10000, 0)
	u.add("browse", "BUTTON", "选择目录…", 0x10000, idBrowse)
	u.add("passwordLabel", "STATIC", "解压密码（可选）", 0, 0)
	u.add("password", "EDIT", "", 0x00800000|0x80|0x20|0x10000, 0)
	u.add("hint", "STATIC", "成功后自动删除原压缩包。重名则整包跳过并记录报告；损坏、密码错误或其他问题均保留源包。", 0, 0)
	u.add("start", "BUTTON", "开始解压", 0x10000|1, idStart)
	u.add("stop", "BUTTON", "停止", 0x10000, idStop)
	u.add("report", "BUTTON", "打开报告目录", 0x10000, idReport)
	u.add("progress", "msctls_progress32", "", 0, 0)
	u.add("status", "STATIC", "选择目录后，点击“开始解压”。", 0, 0)
	u.add("log", "EDIT", "报告保存在所选目录的 .batch-extract-reports 文件夹中。\r\n", 0x00800000|0x00200000|0x4|0x40|0x800|0x10000, 0)
	sendMessage.Call(u.Controls["log"], 0xC5, 300000, 0)
	u.setRunning(false)
	u.layout()
	user32.NewProc("SetTimer").Call(u.Window, 1, 100, 0)
	if smoke {
		var r rect
		getClientRect.Call(u.Window, uintptr(unsafe.Pointer(&r)))
		if len(u.Controls) != 13 || r.Right < 500 {
			return fmt.Errorf("界面初始化检查失败：%d", len(u.Controls))
		}
		user32.NewProc("DestroyWindow").Call(u.Window)
		return nil
	}
	user32.NewProc("ShowWindow").Call(u.Window, 1)
	user32.NewProc("UpdateWindow").Call(u.Window)
	var msg winMessage
	for {
		ret, _, _ := user32.NewProc("GetMessageW").Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if ret == 0 {
			break
		}
		if int32(ret) == -1 {
			return errors.New("Windows 消息循环失败")
		}
		if handled, _, _ := user32.NewProc("IsDialogMessageW").Call(u.Window, uintptr(unsafe.Pointer(&msg))); handled == 0 {
			user32.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&msg)))
			user32.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&msg)))
		}
	}
	return nil
}

func main() {
	dir := flag.String("dir", "", "指定目录以无界面模式执行")
	password := flag.String("password", "", "解压密码")
	smoke := flag.Bool("smoke-test", false, "仅验证内置引擎与界面初始化")
	flag.Parse()
	engine, cleanup, err := unpackEngine()
	if err != nil {
		message(0, err.Error())
		os.Exit(1)
	}
	defer cleanup()
	if *dir != "" {
		x := &Extractor{Root: *dir, Engine: engine, Password: *password, Ctx: context.Background(), Notify: func(e Event) {
			if e.Kind == "done" {
				fmt.Println(e.Message)
			}
		}}
		if err = x.Run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			cleanup()
			os.Exit(1)
		}
		for _, r := range x.Results {
			if r.Status != "成功" {
				cleanup()
				os.Exit(2)
			}
		}
		return
	}
	if *smoke {
		if _, err := os.Stat(filepath.Join(filepath.Dir(engine), "7z.dll")); err != nil {
			cleanup()
			os.Exit(1)
		}
	}
	if err = runUI(engine, *smoke); err != nil {
		if !*smoke {
			message(0, err.Error())
		}
		cleanup()
		os.Exit(1)
	}
}
