# Installs from the dry run's files, served as a stand-in GitHub release, and
# checks the installed binary runs. Used by the CI install job.
$ErrorActionPreference = 'Stop'

$server = Start-Process -FilePath python -PassThru -WindowStyle Hidden `
    -ArgumentList '-m', 'http.server', '8765', '--bind', '127.0.0.1', '--directory', 'release'
try {
    foreach ($attempt in 1..40) {
        try {
            Invoke-WebRequest -UseBasicParsing -Uri "$env:BIGA_RELEASE_URL/checksums.txt" | Out-Null
            break
        } catch {
            Start-Sleep -Milliseconds 250
        }
    }

    $version = (Get-Content -Raw release/metadata.json | ConvertFrom-Json).version
    $env:BIGA_VERSION = $version
    $env:BIGA_INSTALL_DIR = Join-Path $env:RUNNER_TEMP "bin-$($PSVersionTable.PSVersion.Major)"
    & ./install.ps1

    $output = & (Join-Path $env:BIGA_INSTALL_DIR 'biga.exe') --version
    if ($output -notmatch [regex]::Escape($version)) {
        throw "unexpected version output: $output"
    }
    Write-Host "OK: $output"
} finally {
    Stop-Process -Id $server.Id -ErrorAction SilentlyContinue
}
