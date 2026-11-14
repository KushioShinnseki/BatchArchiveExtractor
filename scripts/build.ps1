param([string]$OutputDirectory = 'release')
$ErrorActionPreference = 'Stop'
Set-Location (Split-Path $PSScriptRoot -Parent)

$unformatted = @(gofmt -l (Get-ChildItem -Filter *.go).FullName)
if ($LASTEXITCODE -ne 0 -or $unformatted.Count -gt 0) { throw "gofmt failed: $unformatted" }
go vet ./...
if ($LASTEXITCODE -ne 0) { throw 'go vet failed' }
go test -v ./...
if ($LASTEXITCODE -ne 0) { throw 'go test failed' }
node --test .github/scripts/cleanup.test.cjs
if ($LASTEXITCODE -ne 0) { throw 'cleanup workflow tests failed' }

New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
$executable = Join-Path $OutputDirectory 'BatchArchiveExtractor.exe'
$env:CGO_ENABLED = '0'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
go build -buildvcs=false -trimpath -ldflags '-H windowsgui -s -w' -o $executable .
if ($LASTEXITCODE -ne 0) { throw 'production build failed' }
$smoke = Start-Process -FilePath (Resolve-Path $executable) -ArgumentList '-smoke-test' -WindowStyle Hidden -Wait -PassThru
if ($smoke.ExitCode -ne 0) { throw "executable smoke test failed: $($smoke.ExitCode)" }

$package = Join-Path $OutputDirectory 'package/BatchArchiveExtractor'
New-Item -ItemType Directory -Path $package -Force | Out-Null
Copy-Item -LiteralPath $executable -Destination $package -Force
Copy-Item -LiteralPath README.md,LICENSE -Destination $package -Force
Copy-Item -LiteralPath vendor/License.txt -Destination (Join-Path $package '7-Zip-License.txt') -Force
$archive = Join-Path $OutputDirectory 'BatchArchiveExtractor-windows-amd64.zip'
Compress-Archive -Path (Join-Path $OutputDirectory 'package/*') -DestinationPath $archive -Force

# Verify the actual archive, including launching its extracted executable.
$verification = Join-Path ([IO.Path]::GetTempPath()) ('batch-package-check-' + [guid]::NewGuid().ToString('N'))
try {
    Expand-Archive -LiteralPath $archive -DestinationPath $verification
    $unpacked = Join-Path $verification 'BatchArchiveExtractor/BatchArchiveExtractor.exe'
    if ((Get-FileHash $unpacked).Hash -ne (Get-FileHash $executable).Hash) { throw 'archive executable checksum mismatch' }
    foreach ($name in @('README.md','LICENSE','7-Zip-License.txt')) {
        if (-not (Test-Path -LiteralPath (Join-Path $verification "BatchArchiveExtractor/$name"))) { throw "missing package file: $name" }
    }
    $smoke = Start-Process -FilePath $unpacked -ArgumentList '-smoke-test' -WindowStyle Hidden -Wait -PassThru
    if ($smoke.ExitCode -ne 0) { throw 'packaged executable smoke test failed' }

    # Black-box check of the shipped EXE: recursion, multiple archives, conflict,
    # successful source removal, failed source retention, and readable reports.
    $fixtures = Join-Path $verification 'fixtures'
    $selected = Join-Path $verification 'integration-input'
    $nested = Join-Path $selected 'nested'
    New-Item -ItemType Directory -Path $fixtures,$nested -Force | Out-Null
    'one' | Set-Content -LiteralPath (Join-Path $fixtures 'one.txt') -Encoding utf8NoBOM
    'replacement' | Set-Content -LiteralPath (Join-Path $fixtures 'same.txt') -Encoding utf8NoBOM
    '中文内容' | Set-Content -LiteralPath (Join-Path $fixtures '中文.txt') -Encoding utf8NoBOM
    'original' | Set-Content -LiteralPath (Join-Path $nested 'same.txt') -Encoding utf8NoBOM
    Compress-Archive -LiteralPath (Join-Path $fixtures 'one.txt') -DestinationPath (Join-Path $selected '1.zip')
    Compress-Archive -LiteralPath (Join-Path $fixtures 'same.txt') -DestinationPath (Join-Path $nested '2.zip')
    Compress-Archive -LiteralPath (Join-Path $fixtures '中文.txt') -DestinationPath (Join-Path $nested '3.zip')
    $integration = Start-Process -FilePath $unpacked -ArgumentList @('-dir', ('"' + $selected + '"')) -WindowStyle Hidden -Wait -PassThru
    if ($integration.ExitCode -ne 2) { throw "packaged EXE conflict exit code failed: $($integration.ExitCode)" }
    if ((Test-Path -LiteralPath (Join-Path $selected '1.zip')) -or (Test-Path -LiteralPath (Join-Path $nested '3.zip'))) { throw 'successful source archive was retained' }
    if (-not (Test-Path -LiteralPath (Join-Path $nested '2.zip'))) { throw 'conflicting source archive was deleted' }
    if ((Get-Content -LiteralPath (Join-Path $nested 'same.txt') -Raw).Trim() -ne 'original') { throw 'conflicting destination was overwritten' }
    if ((Get-Content -LiteralPath (Join-Path $nested '中文.txt') -Raw).Trim() -ne '中文内容') { throw 'recursive Unicode extraction failed' }
    $reports = @(Get-ChildItem -LiteralPath (Join-Path $selected '.batch-extract-reports') -Filter report.jsonl -Recurse)
    if ($reports.Count -ne 1) { throw 'missing integration report' }
    $events = @(Get-Content -LiteralPath $reports[0].FullName | ForEach-Object { $_ | ConvertFrom-Json })
    if (@($events | Where-Object status -eq '成功').Count -ne 2 -or @($events | Where-Object status -eq '保留').Count -ne 1) { throw 'wrong integration report outcomes' }
} finally {
    $resolved = [IO.Path]::GetFullPath($verification)
    $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
    if (-not $resolved.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -or -not ([IO.Path]::GetFileName($resolved)).StartsWith('batch-package-check-')) { throw 'unexpected verification cleanup path' }
    if (Test-Path -LiteralPath $resolved) { Remove-Item -LiteralPath $resolved -Recurse -Force }
}
$checksums = foreach ($path in @($executable,$archive)) {
    '{0}  {1}' -f (Get-FileHash $path -Algorithm SHA256).Hash.ToLowerInvariant(), [IO.Path]::GetFileName($path)
}
$checksums | Set-Content -LiteralPath (Join-Path $OutputDirectory 'SHA256SUMS.txt') -Encoding utf8NoBOM
Write-Output "Validated Windows artifacts in $OutputDirectory"
