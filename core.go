package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const reportDir = ".batch-extract-reports"

var volumeName = regexp.MustCompile(`(?i)(\.part\d+\.rar|\.(r\d\d|z\d\d|\d{3}))$`)
var reservedName = regexp.MustCompile(`(?i)^(CON|PRN|AUX|NUL|COM[1-9¹²³]|LPT[1-9¹²³])(\.|$)`)

type Entry struct {
	Name string
	Dir  bool
	Size int64
}
type Result struct {
	Archive string `json:"archive"`
	Status  string `json:"status"`
	Message string `json:"message"`
}
type Event struct {
	Kind, Message, Report string
	Current, Total        int
	Result                Result
}
type Extractor struct {
	Root, Engine, Password, Report string
	Ctx                            context.Context
	Notify                         func(Event)
	Results                        []Result
	Total                          int
}

func isLink(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	if data, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return data.FileAttributes&0x400 != 0
	}
	return false
}

func safeName(name string) (string, error) {
	name = strings.ReplaceAll(name, `\`, "/")
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.TrimRight(part, ". ") != part || reservedName.MatchString(part) || strings.EqualFold(part, reportDir) {
			return "", fmt.Errorf("不安全的包内路径：%q", name)
		}
		for _, ch := range part {
			if ch < 32 || strings.ContainsRune(`<>:"|?*`, ch) {
				return "", fmt.Errorf("不支持的 Windows 文件名：%q", name)
			}
		}
	}
	return filepath.FromSlash(name), nil
}

func hashFile(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1024*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (x *Extractor) command(args ...string) (string, error) {
	password := x.Password
	if password == "" {
		password = "__NO_PASSWORD_PROVIDED_8F76__"
	}
	args = append(args, "-sccUTF-8", "-bd", "-y", "-p"+password)
	cmd := exec.CommandContext(x.Ctx, x.Engine, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	output, err := cmd.CombinedOutput()
	text := strings.ReplaceAll(string(output), "\r\n", "\n")
	if err != nil {
		if x.Ctx.Err() != nil {
			return "", x.Ctx.Err()
		}
		if len(text) > 8000 {
			text = text[len(text)-8000:]
		}
		return "", fmt.Errorf("7-Zip 解压引擎失败（损坏、密码错误或空间不足等）：%v\n%s", err, text)
	}
	return text, nil
}

func (x *Extractor) manifest(archive string) (map[string]Entry, error) {
	text, err := x.command("l", "-slt", archive)
	if err != nil {
		return nil, err
	}
	header, body, ok := strings.Cut(text, "\n----------\n")
	if !ok {
		return nil, errors.New("无法读取压缩包清单")
	}
	if strings.Contains(header, "Multivolume = +") || strings.Contains(header, "Volumes = 2") {
		return nil, errors.New("分卷压缩包暂不处理，保留所有分卷")
	}
	entries := map[string]Entry{}
	for _, block := range strings.Split(strings.Trim(body, "\n"), "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		data := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			key, value, ok := strings.Cut(line, " =")
			if !ok {
				return nil, errors.New("文件名含换行或清单格式不支持")
			}
			if _, exists := data[key]; exists {
				return nil, errors.New("文件名或清单字段存在歧义")
			}
			data[key] = strings.TrimPrefix(value, " ")
		}
		name, err := safeName(data["Path"])
		if err != nil {
			return nil, err
		}
		if data["Symbolic Link"] != "" || data["Hard Link"] != "" || data["Copy Link"] != "" || data["Alternate Stream"] == "+" || strings.HasPrefix(strings.TrimSpace(data["Mode"]), "l") || strings.HasPrefix(strings.TrimSpace(data["Attributes"]), "l") {
			return nil, fmt.Errorf("不支持链接或备用数据流：%s", name)
		}
		key := strings.ToLower(name)
		if _, exists := entries[key]; exists {
			return nil, fmt.Errorf("包内重名（含大小写差异）：%s", name)
		}
		isDir := data["Folder"] == "+" || strings.HasPrefix(data["Attributes"], "D")
		size, err := strconv.ParseInt(data["Size"], 10, 64)
		if err != nil || size < 0 {
			return nil, fmt.Errorf("无法读取文件大小：%s", name)
		}
		entries[key] = Entry{name, isDir, size}
	}
	if len(entries) == 0 {
		return nil, errors.New("空压缩包，保留源包")
	}
	for _, e := range entries {
		for parent := filepath.Dir(e.Name); parent != "."; parent = filepath.Dir(parent) {
			if p, ok := entries[strings.ToLower(parent)]; ok && !p.Dir {
				return nil, fmt.Errorf("包内文件与目录重名：%s", parent)
			}
		}
	}
	return entries, nil
}

func checkDest(base string, entries map[string]Entry) error {
	if isLink(base) {
		return errors.New("目标目录是链接，已跳过")
	}
	conflicts := map[string]bool{}
	for _, e := range entries {
		current := base
		parts := strings.Split(e.Name, string(os.PathSeparator))
		for i, part := range parts {
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if os.IsNotExist(err) {
				break
			}
			if err != nil {
				return err
			}
			if isLink(current) || !info.IsDir() || (i == len(parts)-1 && !e.Dir) {
				conflicts[current] = true
				break
			}
		}
	}
	if len(conflicts) > 0 {
		paths := []string{}
		for p := range conflicts {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		return fmt.Errorf("重名冲突，整包跳过，不覆盖：\n%s", strings.Join(paths, "\n"))
	}
	return nil
}

func (x *Extractor) record(result Result) error {
	data, _ := json.Marshal(struct {
		Time string `json:"time"`
		Result
	}{time.Now().Format("2006-01-02 15:04:05"), result})
	for name, content := range map[string][]byte{
		"report.jsonl": append(data, '\n'),
		"报告.txt":       []byte(fmt.Sprintf("[%s] %s\r\n%s\r\n%s\r\n\r\n", time.Now().Format("2006-01-02 15:04:05"), result.Status, result.Archive, result.Message)),
	} {
		f, err := os.OpenFile(filepath.Join(x.Report, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		_, err = f.Write(content)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (x *Extractor) extract(archive string) (count int, err error) {
	if isLink(archive) {
		return 0, errors.New("源压缩包是链接，已保留")
	}
	if volumeName.MatchString(archive) {
		return 0, errors.New("分卷压缩包暂不处理，请手动解压；所有分卷均保留")
	}
	stem := strings.TrimSuffix(archive, filepath.Ext(archive))
	for _, ext := range []string{".z01", ".r00"} {
		if _, e := os.Stat(stem + ext); e == nil {
			return 0, errors.New("检测到分卷压缩包，保留所有分卷")
		}
	}
	initial, err := os.Stat(archive)
	if err != nil {
		return 0, err
	}
	fingerprint, err := hashFile(x.Ctx, archive)
	if err != nil {
		return 0, err
	}
	entries, err := x.manifest(archive)
	if err != nil {
		return 0, err
	}
	wrapped := false
	lower := strings.ToLower(archive)
	for _, suffix := range []string{".tar.gz", ".tar.bz2", ".tar.xz", ".tar.zst", ".tgz", ".tbz2", ".txz", ".tzst"} {
		if strings.HasSuffix(lower, suffix) {
			wrapped = true
		}
	}
	base := filepath.Dir(archive)
	if !wrapped {
		if err := checkDest(base, entries); err != nil {
			return 0, err
		}
	}
	temp, err := os.MkdirTemp("", "batch-extract-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(temp)
	stage := filepath.Join(temp, "content")
	if err = os.Mkdir(stage, 0700); err != nil {
		return 0, err
	}
	if _, err = x.command("x", archive, "-o"+stage, "-aos"); err != nil {
		return 0, err
	}
	if wrapped {
		files, e := os.ReadDir(stage)
		if e != nil {
			return 0, e
		}
		if len(files) != 1 || files[0].IsDir() || isLink(filepath.Join(stage, files[0].Name())) {
			return 0, errors.New("无法识别 tar 压缩层")
		}
		inner := filepath.Join(stage, files[0].Name())
		entries, err = x.manifest(inner)
		if err != nil {
			return 0, err
		}
		if err = checkDest(base, entries); err != nil {
			return 0, err
		}
		stage = filepath.Join(temp, "unwrapped")
		if err = os.Mkdir(stage, 0700); err != nil {
			return 0, err
		}
		if _, err = x.command("x", inner, "-o"+stage, "-aos"); err != nil {
			return 0, err
		}
	}
	actual := map[string]Entry{}
	err = filepath.WalkDir(stage, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == stage {
			return nil
		}
		if isLink(path) {
			return fmt.Errorf("解压结果包含链接：%s", path)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !d.IsDir() && !info.Mode().IsRegular() {
			return errors.New("不支持特殊文件")
		}
		rel, err := filepath.Rel(stage, path)
		if err != nil {
			return err
		}
		rel, err = safeName(rel)
		if err != nil {
			return err
		}
		actual[strings.ToLower(rel)] = Entry{rel, d.IsDir(), info.Size()}
		return x.Ctx.Err()
	})
	if err != nil {
		return 0, err
	}
	for key, e := range entries {
		a, ok := actual[key]
		if !ok || a.Dir != e.Dir || (!e.Dir && a.Size != e.Size) {
			return 0, fmt.Errorf("解压结果缺失或大小不符：%s", e.Name)
		}
	}
	for key, a := range actual {
		if _, ok := entries[key]; !ok && !a.Dir {
			return 0, fmt.Errorf("出现清单外文件：%s", a.Name)
		}
	}
	if err = checkDest(base, actual); err != nil {
		return 0, err
	}
	files, dirs := []string{}, []string{}
	committed := false
	defer func() {
		if !committed && err != nil {
			failures := []string{}
			for i := len(files) - 1; i >= 0; i-- {
				if e := os.Remove(files[i]); e != nil {
					failures = append(failures, e.Error())
				}
			}
			for i := len(dirs) - 1; i >= 0; i-- {
				if e := os.Remove(dirs[i]); e != nil {
					failures = append(failures, e.Error())
				}
			}
			if len(failures) > 0 {
				err = fmt.Errorf("%w\n部分临时写入未能清理，源包保留：%s", err, strings.Join(failures, "; "))
			}
		}
	}()
	ordered := []Entry{}
	for _, e := range actual {
		ordered = append(ordered, e)
	}
	sort.Slice(ordered, func(i, j int) bool {
		di, dj := strings.Count(ordered[i].Name, `\`), strings.Count(ordered[j].Name, `\`)
		if di != dj {
			return di < dj
		}
		return ordered[i].Name < ordered[j].Name
	})
	for _, e := range ordered {
		if err = x.Ctx.Err(); err != nil {
			return 0, err
		}
		if err = checkDest(base, map[string]Entry{e.Name: e}); err != nil {
			return 0, err
		}
		target := filepath.Join(base, e.Name)
		if e.Dir {
			if _, statErr := os.Stat(target); os.IsNotExist(statErr) {
				if err = os.Mkdir(target, 0755); err != nil {
					return 0, err
				}
				dirs = append(dirs, target)
			}
			continue
		}
		source := filepath.Join(stage, e.Name)
		if err = copyExclusive(x.Ctx, source, target, &files); err != nil {
			return 0, err
		}
		a, hashErr := hashFile(x.Ctx, source)
		if hashErr != nil {
			return 0, hashErr
		}
		b, hashErr := hashFile(x.Ctx, target)
		if hashErr != nil {
			return 0, hashErr
		}
		if a != b {
			return 0, fmt.Errorf("写入后 SHA-256 校验失败：%s", e.Name)
		}
	}
	now, err := os.Stat(archive)
	if err != nil {
		return 0, err
	}
	after, err := hashFile(x.Ctx, archive)
	if err != nil {
		return 0, err
	}
	if initial.Size() != now.Size() || !initial.ModTime().Equal(now.ModTime()) || !os.SameFile(initial, now) || after != fingerprint {
		return 0, errors.New("处理期间源压缩包发生变化，已保留")
	}
	if err = x.record(Result{archive, "已校验", "所有文件已写入并通过 SHA-256 校验，准备删除原包"}); err != nil {
		return 0, fmt.Errorf("无法记录报告，保留源包：%w", err)
	}
	if err = x.Ctx.Err(); err != nil {
		return 0, err
	}
	committed = true
	if err = os.Remove(archive); err != nil {
		return 0, fmt.Errorf("解压校验通过，但删除原包失败；解压文件与源包均保留：%w", err)
	}
	return len(files), nil
}

func copyExclusive(ctx context.Context, source, target string, created *[]string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	*created = append(*created, target)
	buf := make([]byte, 1024*1024)
	for {
		if err = ctx.Err(); err != nil {
			break
		}
		var n int
		n, err = in.Read(buf)
		if n > 0 {
			if _, writeErr := out.Write(buf[:n]); writeErr != nil {
				err = writeErr
				break
			}
		}
		if err == io.EOF {
			err = nil
			break
		}
		if err != nil {
			break
		}
	}
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

func isArchive(name string) bool {
	name = strings.ToLower(name)
	for _, ext := range []string{".zip", ".zipx", ".rar", ".7z", ".tar", ".gz", ".bz2", ".xz", ".zst", ".tgz", ".tbz2", ".txz", ".tzst"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return volumeName.MatchString(name)
}

func (x *Extractor) Run() error {
	if x.Ctx == nil {
		x.Ctx = context.Background()
	}
	if x.Notify == nil {
		x.Notify = func(Event) {}
	}
	root, err := filepath.Abs(x.Root)
	if err != nil {
		return err
	}
	x.Root = root
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || isLink(root) {
		return errors.New("请选择普通目录，不能选择链接目录")
	}
	if isLink(filepath.Join(root, reportDir)) {
		return errors.New("报告目录是链接，无法安全写入")
	}
	if err = os.MkdirAll(filepath.Join(root, reportDir), 0700); err != nil {
		return err
	}
	x.Report, err = os.MkdirTemp(filepath.Join(root, reportDir), time.Now().Format("20060102-150405-"))
	if err != nil {
		return err
	}
	if err = x.record(Result{"", "开始", "递归扫描：" + root + "；重名整包跳过；仅校验成功后删除源包"}); err != nil {
		return err
	}
	archives := []string{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if err := x.Ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if err := x.record(Result{path, "扫描失败", walkErr.Error()}); err != nil {
				return err
			}
			x.Notify(Event{Kind: "log", Message: "扫描失败：" + path + " " + walkErr.Error()})
			return nil
		}
		if path != root && (isLink(path) || strings.EqualFold(d.Name(), reportDir)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() && isArchive(d.Name()) {
			archives = append(archives, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(archives)
	x.Total = len(archives)
	x.Notify(Event{Kind: "scanned", Total: x.Total, Report: x.Report})
	for i, archive := range archives {
		if x.Ctx.Err() != nil {
			break
		}
		x.Notify(Event{Kind: "processing", Current: i + 1, Total: x.Total, Message: archive})
		n, err := x.extract(archive)
		result := Result{archive, "成功", fmt.Sprintf("已解压 %d 个文件，校验通过，原压缩包已删除", n)}
		if err != nil {
			result.Status = "保留"
			result.Message = err.Error()
			if errors.Is(err, context.Canceled) {
				result.Message = "用户已停止；源包保留。"
			}
		}
		if err = x.record(result); err != nil {
			return fmt.Errorf("报告写入失败，停止后续处理：%w", err)
		}
		x.Results = append(x.Results, result)
		x.Notify(Event{Kind: "result", Current: i + 1, Total: x.Total, Result: result})
	}
	success := 0
	for _, r := range x.Results {
		if r.Status == "成功" {
			success++
		}
	}
	message := fmt.Sprintf("发现 %d 个；成功并删除 %d 个；保留 %d 个；未处理 %d 个。", x.Total, success, len(x.Results)-success, x.Total-len(x.Results))
	if x.Ctx.Err() != nil {
		message = "已停止。" + message
	}
	if err = x.record(Result{"", "完成", message}); err != nil {
		return err
	}
	x.Notify(Event{Kind: "done", Message: message, Report: x.Report})
	return nil
}
