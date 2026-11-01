param([switch]$Fix)

$ErrorActionPreference = 'Stop'
$project = $PSScriptRoot
$go = (Get-Command go -ErrorAction Stop).Source
$gofmt = (Get-Command gofmt -ErrorAction Stop).Source
# Respect caller caches; use ignored local paths when unspecified.
if (-not $env:GOPATH) { $env:GOPATH = [IO.Path]::GetFullPath((Join-Path $project '../.local/gopath')) }
if (-not $env:GOCACHE) { $env:GOCACHE = [IO.Path]::GetFullPath((Join-Path $project '../.local/gocache')) }
$files = @(Get-ChildItem -LiteralPath $project -Filter '*.go' -File |
    ForEach-Object { $_.FullName })
if ($Fix) {
    & $gofmt -w @files
    if ($LASTEXITCODE -ne 0) { throw 'gofmt failed' }
}
$unformatted = @(& $gofmt -l @files)
if ($LASTEXITCODE -ne 0) { throw 'gofmt failed' }
if ($unformatted.Count -gt 0) {
    throw "Unformatted files; run ./check.ps1 -Fix:`n$($unformatted -join "`n")"
}

& $go -C $project vet ./...
if ($LASTEXITCODE -ne 0) { throw 'go vet failed' }
& $go -C $project test ./...
if ($LASTEXITCODE -ne 0) { throw 'go test failed' }
Write-Output 'PASS: gofmt, go vet, go test'
