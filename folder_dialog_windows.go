package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// IFileOpenDialog is the Explorer-style, resizable Windows dialog, with an
// address bar, search, navigation history and the standard places sidebar.
// COM is initialized as STA on the UI's locked OS thread in runUI.
type shellObject struct {
	vtable *[29]uintptr
}

var (
	clsidFileOpenDialog = syscall.GUID{Data1: 0xdc1c5a9c, Data2: 0xe88a, Data3: 0x4dde, Data4: [8]byte{0xa5, 0xa1, 0x60, 0xf8, 0x2a, 0x20, 0xae, 0xf7}}
	iidFileOpenDialog   = syscall.GUID{Data1: 0xd57c7288, Data2: 0xd4ad, Data3: 0x4768, Data4: [8]byte{0xbe, 0x02, 0x9d, 0x96, 0x95, 0x32, 0xd9, 0x60}}
	iidShellItem        = syscall.GUID{Data1: 0x43826d1e, Data2: 0xe718, Data3: 0x42ee, Data4: [8]byte{0xbc, 0x55, 0xa1, 0xe2, 0x61, 0xc3, 0x7b, 0xfe}}
)

const folderDialogOptions = 0x20 | 0x40 | 0x800 | 0x8 // PICKFOLDERS, FORCEFILESYSTEM, PATHMUSTEXIST, NOCHANGEDIR

func hresultError(operation string, hr uintptr) error {
	if int32(hr) < 0 {
		return fmt.Errorf("%s (HRESULT 0x%08X)", operation, uint32(hr))
	}
	return nil
}

func (object *shellObject) release() {
	syscall.SyscallN(object.vtable[2], uintptr(unsafe.Pointer(object))) // IUnknown::Release
}

func newFolderDialog() (*shellObject, error) {
	var dialog *shellObject
	hr, _, _ := ole32.NewProc("CoCreateInstance").Call(
		uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, 1, // CLSCTX_INPROC_SERVER
		uintptr(unsafe.Pointer(&iidFileOpenDialog)), uintptr(unsafe.Pointer(&dialog)))
	if err := hresultError("创建目录选择器", hr); err != nil {
		return nil, err
	}
	var options uint32
	hr, _, _ = syscall.SyscallN(dialog.vtable[10], uintptr(unsafe.Pointer(dialog)), uintptr(unsafe.Pointer(&options)))
	if err := hresultError("读取目录选项", hr); err != nil {
		dialog.release()
		return nil, err
	}
	hr, _, _ = syscall.SyscallN(dialog.vtable[9], uintptr(unsafe.Pointer(dialog)), uintptr(options|folderDialogOptions))
	if err := hresultError("设置目录选项", hr); err != nil {
		dialog.release()
		return nil, err
	}
	return dialog, nil
}

func setDialogFolder(dialog *shellObject, path string) error {
	var item *shellObject
	hr, _, _ := shell32.NewProc("SHCreateItemFromParsingName").Call(
		uintptr(unsafe.Pointer(wide(path))), 0,
		uintptr(unsafe.Pointer(&iidShellItem)), uintptr(unsafe.Pointer(&item)))
	if err := hresultError("读取初始目录", hr); err != nil {
		return err
	}
	defer item.release()
	hr, _, _ = syscall.SyscallN(dialog.vtable[12], uintptr(unsafe.Pointer(dialog)), uintptr(unsafe.Pointer(item))) // SetFolder
	return hresultError("设置初始目录", hr)
}

func shellItemPath(item *shellObject) (string, error) {
	var name *uint16
	hr, _, _ := syscall.SyscallN(item.vtable[5], uintptr(unsafe.Pointer(item)), 0x80058000, uintptr(unsafe.Pointer(&name))) // SIGDN_FILESYSPATH
	if err := hresultError("读取所选目录", hr); err != nil {
		return "", err
	}
	if name == nil {
		return "", fmt.Errorf("目录选择器返回空路径")
	}
	defer ole32.NewProc("CoTaskMemFree").Call(uintptr(unsafe.Pointer(name)))
	for length := 0; length < 32768; length++ {
		if *(*uint16)(unsafe.Add(unsafe.Pointer(name), length*2)) == 0 {
			return syscall.UTF16ToString(unsafe.Slice(name, length)), nil
		}
	}
	return "", fmt.Errorf("所选目录路径过长")
}

// A cancelled dialog returns an empty path without changing the current choice.
func chooseFolder(owner uintptr, initial string) (string, error) {
	dialog, err := newFolderDialog()
	if err != nil {
		return "", err
	}
	defer dialog.release()
	hr, _, _ := syscall.SyscallN(dialog.vtable[17], uintptr(unsafe.Pointer(dialog)), uintptr(unsafe.Pointer(wide("选择需要递归解压的目录"))))
	if err := hresultError("设置窗口标题", hr); err != nil {
		return "", err
	}
	hr, _, _ = syscall.SyscallN(dialog.vtable[18], uintptr(unsafe.Pointer(dialog)), uintptr(unsafe.Pointer(wide("选择此文件夹"))))
	if err := hresultError("设置选择按钮", hr); err != nil {
		return "", err
	}
	initial = strings.TrimSpace(initial)
	if info, err := os.Stat(initial); err == nil && info.IsDir() {
		// An inaccessible/removed initial folder must not prevent opening the picker.
		_ = setDialogFolder(dialog, initial)
	}
	hr, _, _ = syscall.SyscallN(dialog.vtable[3], uintptr(unsafe.Pointer(dialog)), owner) // Show
	if uint32(hr) == 0x800704c7 {                                                         // HRESULT_FROM_WIN32(ERROR_CANCELLED)
		return "", nil
	}
	if err := hresultError("打开目录选择窗口", hr); err != nil {
		return "", err
	}
	var item *shellObject
	hr, _, _ = syscall.SyscallN(dialog.vtable[20], uintptr(unsafe.Pointer(dialog)), uintptr(unsafe.Pointer(&item))) // GetResult
	if err := hresultError("获取所选目录", hr); err != nil {
		return "", err
	}
	defer item.release()
	return shellItemPath(item)
}
