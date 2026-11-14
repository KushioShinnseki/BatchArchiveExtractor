package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

var testEngine string

func TestMain(m *testing.M) {
	engine, cleanup, err := unpackEngine()
	if err != nil {
		panic(err)
	}
	testEngine = engine
	code := m.Run()
	cleanup()
	os.Exit(code)
}

type pair struct{ name, content string }

func makeZip(t *testing.T, root, name string, entries ...pair) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for _, e := range entries {
		w, err := z.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(e.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
func runBatch(t *testing.T, root, password string, ctx context.Context) *Extractor {
	t.Helper()
	x := &Extractor{Root: root, Password: password, Engine: testEngine, Ctx: ctx}
	if err := x.Run(); err != nil {
		t.Fatal(err)
	}
	return x
}
func statuses(t *testing.T, x *Extractor, want ...string) {
	t.Helper()
	got := []string{}
	for _, r := range x.Results {
		got = append(got, r.Status)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("want %v, got %+v", want, x.Results)
	}
}
func exists(path string) bool { _, err := os.Stat(path); return err == nil }
func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRecursiveMultipleChinese(t *testing.T) {
	r := t.TempDir()
	a := makeZip(t, r, "A/1.zip", pair{"中文/你好.txt", "内容"}, pair{"empty/", ""})
	b := makeZip(t, r, "A/B/2.zip", pair{"two.txt", "2"})
	c := makeZip(t, r, "A/B/3.zip", pair{"three.txt", "3"})
	x := runBatch(t, r, "", nil)
	statuses(t, x, "成功", "成功", "成功")
	if read(t, filepath.Join(r, "A/中文/你好.txt")) != "内容" || read(t, filepath.Join(r, "A/B/two.txt")) != "2" {
		t.Fatal("wrong output")
	}
	if exists(a) || exists(b) || exists(c) || !exists(filepath.Join(r, "A/empty")) {
		t.Fatal("source deletion or directory failed")
	}
	lines := strings.Split(strings.TrimSpace(read(t, filepath.Join(x.Report, "report.jsonl"))), "\n")
	for _, line := range lines {
		var v map[string]any
		if json.Unmarshal([]byte(line), &v) != nil {
			t.Fatal("invalid report")
		}
	}
}
func TestExistingCollisionSkipsEntireArchive(t *testing.T) {
	r := t.TempDir()
	a := makeZip(t, r, "one.zip", pair{"new.txt", "new"}, pair{"same.txt", "replacement"})
	os.WriteFile(filepath.Join(r, "same.txt"), []byte("original"), 0600)
	x := runBatch(t, r, "", nil)
	statuses(t, x, "保留")
	if !strings.Contains(x.Results[0].Message, "same.txt") || exists(filepath.Join(r, "new.txt")) || !exists(a) || read(t, filepath.Join(r, "same.txt")) != "original" {
		t.Fatal(x.Results)
	}
}
func TestBetweenArchiveCollision(t *testing.T) {
	r := t.TempDir()
	makeZip(t, r, "1.zip", pair{"same.txt", "one"})
	makeZip(t, r, "2.zip", pair{"same.txt", "two"})
	statuses(t, runBatch(t, r, "", nil), "成功", "保留")
	if read(t, filepath.Join(r, "same.txt")) != "one" {
		t.Fatal("overwritten")
	}
}
func TestInternalDuplicateAndCaseConflict(t *testing.T) {
	r := t.TempDir()
	makeZip(t, r, "1.zip", pair{"same.txt", "one"}, pair{"same.txt", "two"})
	makeZip(t, r, "2.zip", pair{"A.txt", "one"}, pair{"a.TXT", "two"})
	statuses(t, runBatch(t, r, "", nil), "保留", "保留")
	if exists(filepath.Join(r, "same.txt")) {
		t.Fatal("unexpected output")
	}
}
func TestCorruptTraversalAndContinue(t *testing.T) {
	r := t.TempDir()
	os.WriteFile(filepath.Join(r, "1.zip"), []byte("broken"), 0600)
	makeZip(t, r, "2.zip", pair{"../escape.txt", "bad"})
	makeZip(t, r, "3.zip", pair{"good.txt", "good"})
	statuses(t, runBatch(t, r, "", nil), "保留", "保留", "成功")
	if exists(filepath.Join(filepath.Dir(r), "escape.txt")) {
		t.Fatal("path traversal")
	}
}
func TestCRCFailureRetained(t *testing.T) {
	r := t.TempDir()
	var data bytes.Buffer
	z := zip.NewWriter(&data)
	w, err := z.CreateHeader(&zip.FileHeader{Name: "data.txt", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("unique_PAYLOAD_12345"))
	z.Close()
	broken := bytes.Replace(data.Bytes(), []byte("unique_PAYLOAD_12345"), []byte("unique_PAYLOAD_12346"), 1)
	os.WriteFile(filepath.Join(r, "broken.zip"), broken, 0600)
	statuses(t, runBatch(t, r, "", nil), "保留")
	if exists(filepath.Join(r, "data.txt")) {
		t.Fatal("corrupt file published")
	}
}
func TestParentFileConflict(t *testing.T) {
	r := t.TempDir()
	os.WriteFile(filepath.Join(r, "folder"), []byte("original"), 0600)
	makeZip(t, r, "1.zip", pair{"folder/child.txt", "data"})
	statuses(t, runBatch(t, r, "", nil), "保留")
	if read(t, filepath.Join(r, "folder")) != "original" {
		t.Fatal("overwritten")
	}
}
func TestEncrypted7z(t *testing.T) {
	r := t.TempDir()
	source := filepath.Join(r, "secret.txt")
	os.WriteFile(source, []byte("secret"), 0600)
	cmd := exec.Command(testEngine, "a", filepath.Join(r, "secret.7z"), source, "-pabc", "-mhe=on")
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, data)
	}
	os.Remove(source)
	statuses(t, runBatch(t, r, "wrong", nil), "保留")
	if exists(source) {
		t.Fatal("wrong password produced output")
	}
	statuses(t, runBatch(t, r, "abc", nil), "成功")
	if read(t, source) != "secret" {
		t.Fatal("wrong contents")
	}
}
func TestTarGzip(t *testing.T) {
	r := t.TempDir()
	f, _ := os.Create(filepath.Join(r, "data.tar.gz"))
	gz := gzip.NewWriter(f)
	tarw := tar.NewWriter(gz)
	data := []byte("你好 tar")
	tarw.WriteHeader(&tar.Header{Name: "nested/data.txt", Mode: 0644, Size: int64(len(data))})
	tarw.Write(data)
	tarw.Close()
	gz.Close()
	f.Close()
	statuses(t, runBatch(t, r, "", nil), "成功")
	if read(t, filepath.Join(r, "nested/data.txt")) != string(data) {
		t.Fatal("wrong tar contents")
	}
}
func TestRAR5(t *testing.T) {
	r := t.TempDir()
	data, err := os.ReadFile("testdata/rar5-crc.rar")
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(r, "sample.rar")
	os.WriteFile(archive, data, 0600)
	statuses(t, runBatch(t, r, "", nil), "成功")
	a, b := read(t, filepath.Join(r, "stest1.txt")), read(t, filepath.Join(r, "stest2.txt"))
	if len(a) != 2048 || a != b || exists(archive) {
		t.Fatal("RAR5 output or source deletion failed")
	}
}

type fileContext struct {
	context.Context
	target string
	action func()
	fired  bool
	cancel bool
}

func (c *fileContext) Err() error {
	if !c.fired && exists(c.target) {
		c.fired = true
		if c.action != nil {
			c.action()
		}
	}
	if c.fired && c.cancel {
		return context.Canceled
	}
	return nil
}
func TestCancelRollsBackCurrentArchive(t *testing.T) {
	r := t.TempDir()
	archive := makeZip(t, r, "1.zip", pair{"data.txt", "payload"})
	target := filepath.Join(r, "data.txt")
	ctx := &fileContext{Context: context.Background(), target: target, cancel: true}
	statuses(t, runBatch(t, r, "", ctx), "保留")
	if !exists(archive) || exists(target) {
		t.Fatal("cancellation did not rollback")
	}
}
func TestSourceMutationRollsBack(t *testing.T) {
	r := t.TempDir()
	archive := makeZip(t, r, "1.zip", pair{"data.txt", "payload"})
	target := filepath.Join(r, "data.txt")
	ctx := &fileContext{Context: context.Background(), target: target, action: func() {
		f, _ := os.OpenFile(archive, os.O_WRONLY|os.O_APPEND, 0600)
		f.Write([]byte("changed"))
		f.Close()
	}}
	x := runBatch(t, r, "", ctx)
	statuses(t, x, "保留")
	if !strings.Contains(x.Results[0].Message, "发生变化") || !exists(archive) || exists(target) {
		t.Fatal(x.Results)
	}
}
func TestDeleteFailureRetainsArchiveAndOutput(t *testing.T) {
	r := t.TempDir()
	archive := makeZip(t, r, "1.zip", pair{"data.txt", "payload"})
	path, _ := syscall.UTF16PtrFromString(archive)
	h, err := syscall.CreateFile(path, syscall.GENERIC_READ, syscall.FILE_SHARE_READ, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(h)
	x := runBatch(t, r, "", nil)
	statuses(t, x, "保留")
	if !strings.Contains(x.Results[0].Message, "删除原包失败") || !exists(archive) || read(t, filepath.Join(r, "data.txt")) != "payload" {
		t.Fatal(x.Results)
	}
}
func TestNestedArchivesWaitForNextRun(t *testing.T) {
	r := t.TempDir()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, _ := z.Create("inner.txt")
	w.Write([]byte("inner"))
	z.Close()
	makeZip(t, r, "outer.zip", pair{"inner.zip", b.String()})
	x := runBatch(t, r, "", nil)
	statuses(t, x, "成功")
	if x.Total != 1 || !exists(filepath.Join(r, "inner.zip")) || exists(filepath.Join(r, "inner.txt")) {
		t.Fatal("nested archive processed unexpectedly")
	}
}
func TestMultipartRetained(t *testing.T) {
	r := t.TempDir()
	makeZip(t, r, "name.part1.rar", pair{"data.txt", "data"})
	makeZip(t, r, "name.zip", pair{"data.txt", "data"})
	os.WriteFile(filepath.Join(r, "name.z01"), []byte("volume"), 0600)
	statuses(t, runBatch(t, r, "", nil), "保留", "保留", "保留")
}
func TestTarSymlinkRejected(t *testing.T) {
	r := t.TempDir()
	f, _ := os.Create(filepath.Join(r, "link.tar"))
	w := tar.NewWriter(f)
	w.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../outside", Mode: 0777})
	w.Close()
	f.Close()
	statuses(t, runBatch(t, r, "", nil), "保留")
	if exists(filepath.Join(r, "link")) {
		t.Fatal("symlink published")
	}
}
func TestUnsafeNames(t *testing.T) {
	for _, name := range []string{"../escape", "C:/escape", "/absolute", "foo:stream", "NUL.txt", "foo./bar", ".batch-extract-reports/evil"} {
		if _, err := safeName(name); err == nil {
			t.Errorf("allowed %q", name)
		}
	}
}
func TestEmptyArchiveRetained(t *testing.T) {
	r := t.TempDir()
	makeZip(t, r, "empty.zip")
	statuses(t, runBatch(t, r, "", nil), "保留")
}
func TestWindowsGUIInitialization(t *testing.T) {
	if err := runUI(testEngine, true); err != nil {
		t.Fatal(err)
	}
}
