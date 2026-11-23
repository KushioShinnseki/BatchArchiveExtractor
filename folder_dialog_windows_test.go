package main

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

func TestModernFolderDialog(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := ole32.NewProc("CoInitializeEx").Call(0, 2)
	if err := hresultError("初始化 COM", hr); err != nil {
		t.Fatal(err)
	}
	defer ole32.NewProc("CoUninitialize").Call()
	dialog, err := newFolderDialog()
	if err != nil {
		t.Fatal(err)
	}
	defer dialog.release()
	var options uint32
	hr, _, _ = syscall.SyscallN(dialog.vtable[10], uintptr(unsafe.Pointer(dialog)), uintptr(unsafe.Pointer(&options)))
	if err := hresultError("读取目录选项", hr); err != nil {
		t.Fatal(err)
	}
	if options&folderDialogOptions != folderDialogOptions {
		t.Fatalf("folder picker options missing: 0x%x", options)
	}
	// Exercise the real COM interfaces and Unicode path round-trip without
	// opening an interactive dialog on CI runners.
	directory := filepath.Join(t.TempDir(), "中文 文件夹")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := setDialogFolder(dialog, directory); err != nil {
		t.Fatal(err)
	}
	var item *shellObject
	hr, _, _ = syscall.SyscallN(dialog.vtable[13], uintptr(unsafe.Pointer(dialog)), uintptr(unsafe.Pointer(&item))) // GetFolder
	if err := hresultError("读取初始目录", hr); err != nil {
		t.Fatal(err)
	}
	defer item.release()
	got, err := shellItemPath(item)
	if err != nil {
		t.Fatal(err)
	}
	// The Shell canonicalizes 8.3 names (e.g. RUNNER~1) to their long names.
	// Check directory identity rather than comparing equivalent path spellings.
	selectedInfo, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	expectedInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if !selectedInfo.IsDir() || !os.SameFile(selectedInfo, expectedInfo) {
		t.Fatalf("initial folder round-trip selected a different directory: got %q, want %q", got, directory)
	}
}
