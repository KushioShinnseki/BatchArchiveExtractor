# 批量解压助手（Go / Windows）

双击 `BatchArchiveExtractor.exe`，点击“选择目录”，然后点击“开始解压”。无需安装 Go、Python 或 7-Zip。可选填写统一解压密码。

## 处理规则

- 递归扫描所选目录及其子目录，每个文件夹中的多个压缩包都会处理。
- 解压到压缩包所在目录：`A/1.zip → A`，`A/B/2.rar → A/B`。
- 支持 ZIP、ZIPX、RAR、7z、TAR、GZ、BZ2、XZ、ZST 以及 tar.gz / tgz 等常见组合。
- 目录允许合并；文件重名、文件与目录冲突、包内重名（包括大小写差异）会跳过整个压缩包，不覆盖已有文件，保留原包并报告具体路径。
- 临时解压、检查结果、写入目标、逐文件 SHA-256 校验，全部成功后才删除原包。7-Zip 的警告也视为失败。
- 密码错误、损坏、读写失败、停止操作、原包变化等情况保留源包。写入中失败会尝试撤回本包新建的文件和目录；清理失败会在报告中说明。
- 如果只有删除源包失败，保留解压文件与源包并报告。
- 分卷压缩包暂不处理，保留全部分卷并报告。空压缩包保留并报告。
- 本次只扫描一次。包内嵌套的压缩包留待下一次运行处理。
- 跳过文件系统链接目录，拒绝包内链接、不安全路径及 Windows 无效文件名。

成功后的原包是直接删除，不进入回收站。请先在样本目录试用；软件不会自动对其他目录执行操作。

## 报告

报告保存到所选目录的 `.batch-extract-reports/时间-随机编号/`：

- `报告.txt`：便于阅读，包含成功、冲突、错误及扫描失败。
- `report.jsonl`：每行一个 JSON 事件，便于程序分析。

窗口显示处理进度、日志，支持停止和打开报告目录。停止当前包时先清理本包新建输出；之前已成功完成的包保持处理结果。进度按压缩包数量计算。

突然断电或进程被强制终止时，可能留下本包已写入的部分文件；原包在完成校验前不会删除。重新运行遇到这些文件会报告重名，需人工检查。

## 开发与本地打包

实现使用 Go 标准库及 Windows 原生 Win32 界面，无第三方 Go 模块，无 CGO。项目沿用仓库的 GPL-3.0 许可。

在 Windows x64 安装 Go 1.24 或更新版本后：

```powershell
go test -v ./...
go vet ./...
go build -buildvcs=false -trimpath -ldflags "-H windowsgui -s -w" -o dist/BatchArchiveExtractor.exe .
```

完整检查与打包（还需要 Node.js，用于清理流程的单元测试）：

```powershell
./scripts/build.ps1
```

输出到 `release/`：单文件 EXE、带使用说明及许可的 Windows x64 ZIP、`SHA256SUMS.txt`。

## GitHub CI/CD

- 开发分支推送与面向 `main` 的 PR：检查格式、`go vet`、Go 功能测试、清理脚本测试、Windows 编译、EXE 初始化与 ZIP 校验。
- PR 检查通过后使用 squash merge 合并到 `main`，随后 CI 再验证最终主线提交。
- `main` 的 push CI 成功才自动触发 CD。也可手动运行 CD（仅 `main`）。CD 对同一提交重新检查和打包，上传并验证全部资产后才发布 Release。
- 发布标签：`BatchArchiveExtractor_YYYYMMDD_NNNN`，按 Asia/Shanghai 日期、当天递增编号。
- **Manual release cleanup** 独立且仅能手动运行。`retain` 模式保留每天最高编号；`range` 模式按开始/结束日期删除（含首尾）。执行前打印完整计划，先删 Release 再删标签；失败不会盲目继续删除对应标签。
- 旧版纯日期/时间标签只处理清理脚本中明确支持、能验证日期的格式。未知、其他项目或有歧义的标签保留并报告。新仓库初始历史只有 LICENSE，无旧版标签。
- 手动清理是破坏性操作，仅在确实需要删除历史版本时运行；不设置定时清理。

`vendor/7z.exe`、`vendor/7z.dll`、`vendor/License.txt` 通过 `go:embed` 内置到程序中，运行时解压到临时目录，正常退出时清理。程序为未签名的 Windows x64 可执行文件。

可选无界面运行（直接执行相同解压与删除规则）：

```powershell
.\BatchArchiveExtractor.exe -dir "D:\待解压"
.\BatchArchiveExtractor.exe -dir "D:\待解压" -password "密码"
```

退出码：0 全部成功（或没有压缩包）；1 运行/报告错误；2 存在保留项。图形版本无终端窗口，详细结果请查看报告。

## 第三方组件

内置官方 [7-Zip 26.06](https://www.7-zip.org/download.html)，Copyright © 1999–2026 Igor Pavlov。完整许可见 `vendor/License.txt`，包括 LGPL、BSD 及 unRAR 限制。源代码可从 [官方 7-Zip 源码仓库](https://github.com/ip7z/7zip/tree/26.06) 获取。

RAR5 测试样本 `testdata/rar5-crc.rar` 来自 [rarfile 测试集](https://github.com/markokr/rarfile/blob/master/test/files/rar5-crc.rar)，只用于本地测试，不包含于可执行程序。
