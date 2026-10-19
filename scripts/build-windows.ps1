param([string]$OutputDirectory = "bin")
$ErrorActionPreference = 'Stop'
$previousGOOS = $env:GOOS; $previousGOARCH = $env:GOARCH
Push-Location (Join-Path $PSScriptRoot '..')
try {
    $versionMatch = [regex]::Match((Get-Content -Raw 'version.go'), 'const appVersion = "([^"]+)"')
    if (-not $versionMatch.Success) { throw 'Could not read appVersion from version.go' }
    $appVersion = $versionMatch.Groups[1].Value
    New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null
    $outputFile = Join-Path $OutputDirectory "soundstreamer-$appVersion.exe"
    go run scripts/build-resources.go
    if ($LASTEXITCODE -ne 0) { throw 'Windows resource generation failed' }
    $env:GOOS = 'windows'; $env:GOARCH = 'amd64'
    go build -trimpath -buildvcs=false -ldflags '-H=windowsgui' -o $outputFile .
    if ($LASTEXITCODE -ne 0) { throw 'Windows build failed' }
    Write-Host "Built $outputFile"
} finally { $env:GOOS = $previousGOOS; $env:GOARCH = $previousGOARCH; Pop-Location }
