param(
    [string]$Root,
    [string]$Config,
    [string]$Python,
    [string]$FFmpeg,
    [string]$FFprobe,
    [string]$MetadataFile,
    [int]$Limit = 0,
    [int]$MinimumAgeSeconds = 300,
    [switch]$Plan
)

$ErrorActionPreference = 'Stop'
$project = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
if (-not $Config) { $Config = Join-Path $project '.local/pc/covers.json' }
$saved = @{}
if (Test-Path -LiteralPath $Config -PathType Leaf) {
    $saved = Get-Content -LiteralPath $Config -Raw | ConvertFrom-Json
}
if (-not $Root) { $Root = $saved.Root }
if (-not $Python) { $Python = $saved.Python }
if (-not $FFmpeg) { $FFmpeg = $saved.FFmpeg }
if (-not $FFprobe) { $FFprobe = $saved.FFprobe }
if (-not $Python) { $Python = (Get-Command python -ErrorAction Stop).Source }
if (-not $FFmpeg) { $FFmpeg = (Get-Command ffmpeg -ErrorAction Stop).Source }
if (-not $FFprobe) { $FFprobe = Join-Path (Split-Path $FFmpeg) 'ffprobe.exe' }
if (-not $Root) { throw 'Specify -Root, or copy examples/covers.example.json to .local/pc/covers.json and edit it.' }
if ($Limit -lt 0 -or $MinimumAgeSeconds -lt 0) { throw 'Limit and MinimumAgeSeconds must be nonnegative.' }
foreach ($tool in @($Python, $FFmpeg, $FFprobe)) {
    if (-not (Test-Path -LiteralPath $tool -PathType Leaf)) { throw "Tool not found: $tool" }
}
if (-not (Test-Path -LiteralPath $Root -PathType Container)) {
    throw 'The media folder is inaccessible. Open it in Explorer and sign in to your NAS first.'
}

$previousPackages = $env:NMF_PYTHON_PACKAGES
$previousEncoding = $env:PYTHONIOENCODING
try {
    if ($saved.PythonPackages) { $env:NMF_PYTHON_PACKAGES = $saved.PythonPackages }
    $env:PYTHONIOENCODING = 'utf-8'
    # Windows PowerShell 5.1 strips embedded double quotes in native arguments.
    & $Python -c "import os,sys; p=os.environ.get('NMF_PYTHON_PACKAGES'); sys.path[:0]=[p] if p else []; from PIL import Image, ImageStat"
    if ($LASTEXITCODE -ne 0) { throw 'Install Pillow in the selected Python: python -m pip install -r scripts/pc/requirements.txt' }
    $arguments = @(
        (Join-Path $PSScriptRoot 'generate_covers.py'),
        '--root', $Root,
        '--ffmpeg', $FFmpeg,
        '--ffprobe', $FFprobe,
        '--state-dir', (Join-Path $project '.local/cover-jobs'),
        '--limit', $Limit,
        '--min-age', $MinimumAgeSeconds
    )
    if (-not $MetadataFile) { $MetadataFile = $saved.MetadataFile }
    if ($MetadataFile) { $arguments += @('--metadata-file', $MetadataFile) }
    if ($Plan) { $arguments += '--plan' }
    & $Python @arguments
    if ($LASTEXITCODE -ne 0) { throw "Cover generation reported errors: exit $LASTEXITCODE. See .local/cover-jobs/preview-results.jsonl." }
} finally {
    $env:NMF_PYTHON_PACKAGES = $previousPackages
    $env:PYTHONIOENCODING = $previousEncoding
}
