param(
 [Parameter(Mandatory)][string]$Web,
 [string]$Output,
 [string]$Version='2.0.0-0021',
 [string]$Go='go',
 [string]$Python='python'
)
$ErrorActionPreference='Stop'
$root=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
if (-not $Output) { $Output=Join-Path $root 'dist' }
$Output=[IO.Path]::GetFullPath($Output)
New-Item -ItemType Directory -Force -Path $Output | Out-Null
$source=Join-Path $root 'server'
& $Go -C $source test ./...
if ($LASTEXITCODE -ne 0) {throw 'Go tests failed'}
& $Go -C $source vet ./...
if ($LASTEXITCODE -ne 0) {throw 'Go vet failed'}
$previous=@{GOOS=$env:GOOS;GOARCH=$env:GOARCH;CGO_ENABLED=$env:CGO_ENABLED}
try {
 $env:GOOS='linux';$env:GOARCH='arm64';$env:CGO_ENABLED='0'
 $binary=Join-Path $Output 'nas-media-folders-arm64'
 & $Go -C $source build -trimpath -buildvcs=false -ldflags '-s -w' -o $binary .
 if ($LASTEXITCODE -ne 0) {throw 'ARM64 build failed'}
 & $Python (Join-Path $root 'packaging/synology/build.py') --binary $binary --web $Web --output $Output --version $Version
 if ($LASTEXITCODE -ne 0) {throw 'DSM packaging failed'}
} finally {
 $env:GOOS=$previous.GOOS;$env:GOARCH=$previous.GOARCH;$env:CGO_ENABLED=$previous.CGO_ENABLED
}
