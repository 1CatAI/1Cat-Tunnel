$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$dist = Join-Path $root "dist"
$toolcache = Join-Path $root ".toolcache"
$packageRoot = Join-Path $dist "packages"
$linuxDeliveryDir = Join-Path $dist "linux-client-delivery"
$serverDockerDeliveryDir = Join-Path $dist "server-docker-deployment"
$windowsDeliveryDir = Join-Path $dist "windows-client-delivery"

function Reset-Directory {
    param([string]$Path)

    if (Test-Path $Path) {
        Remove-Item $Path -Recurse -Force
    }
    New-Item -ItemType Directory -Force $Path | Out-Null
}

function Get-GoCommand {
    $existing = Get-Command go -ErrorAction SilentlyContinue
    if ($existing) {
        return $existing.Source
    }

    Write-Host "Local Go toolchain not found. Downloading a portable Go toolchain from go.dev..."

    New-Item -ItemType Directory -Force $toolcache | Out-Null

    $releases = Invoke-RestMethod -Uri "https://go.dev/dl/?mode=json"
    $release = $releases | Where-Object { $_.stable } | Select-Object -First 1
    if (-not $release) {
        throw "Unable to resolve the latest stable Go release from go.dev."
    }

    $asset = $release.files | Where-Object {
        $_.os -eq "windows" -and $_.arch -eq "amd64" -and $_.kind -eq "archive" -and $_.filename -like "*.zip"
    } | Select-Object -First 1
    if (-not $asset) {
        throw "Unable to find a Windows amd64 Go archive for $($release.version)."
    }

    $archivePath = Join-Path $toolcache $asset.filename
    $extractRoot = Join-Path $toolcache "$($release.version)-windows-amd64"
    $goExe = Join-Path $extractRoot "go\bin\go.exe"

    if (-not (Test-Path $archivePath)) {
        Invoke-WebRequest -Uri ("https://go.dev/dl/" + $asset.filename) -OutFile $archivePath
    }

    if (-not (Test-Path $goExe)) {
        if (Test-Path $extractRoot) {
            Remove-Item $extractRoot -Recurse -Force
        }
        New-Item -ItemType Directory -Force $extractRoot | Out-Null
        & tar.exe -xf $archivePath -C $extractRoot
        if ($LASTEXITCODE -ne 0) {
            throw "Failed to extract $archivePath"
        }
    }

    return $goExe
}

function Invoke-GoBuild {
    param(
        [string]$GoCommand,
        [string]$GOOS,
        [string]$GOARCH,
        [string]$Output,
        [string]$Package
    )

    $env:CGO_ENABLED = "0"
    $env:GOOS = $GOOS
    $env:GOARCH = $GOARCH

    & $GoCommand build -trimpath "-ldflags=-s -w" -o $Output $Package
    if ($LASTEXITCODE -ne 0) {
        throw "go build failed for $Package ($GOOS/$GOARCH)"
    }
}

function Copy-PackageFile {
    param(
        [string]$Source,
        [string]$DestinationDirectory,
        [string]$DestinationName
    )

    if (-not $DestinationName) {
        $DestinationName = Split-Path $Source -Leaf
    }
    Copy-Item $Source (Join-Path $DestinationDirectory $DestinationName) -Force
}

function New-LinuxPackageArchive {
    param([string]$PackageDirectory)

    $archivePath = $PackageDirectory + ".tar.gz"
    if (Test-Path $archivePath) {
        Remove-Item $archivePath -Force
    }

    $pythonScript = @'
import os
import sys
import tarfile

src = sys.argv[1]
dst = sys.argv[2]
parent = os.path.dirname(src)

with tarfile.open(dst, "w:gz") as tar:
    for root, dirs, files in os.walk(src):
        dirs.sort()
        files.sort()

        rel_root = os.path.relpath(root, parent)
        dir_info = tar.gettarinfo(root, arcname=rel_root)
        dir_info.mode = 0o755
        tar.addfile(dir_info)

        for name in files:
            path = os.path.join(root, name)
            arcname = os.path.join(rel_root, name)
            info = tar.gettarinfo(path, arcname=arcname)
            lowered = name.lower()
            if lowered.endswith(".sh") or lowered in {"tunnel-server", "tunnel-client", "1cat-tunnel-client"}:
                info.mode = 0o755
            else:
                info.mode = 0o644
            with open(path, "rb") as handle:
                tar.addfile(info, handle)
'@

    $pythonScript | python - $PackageDirectory $archivePath
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to archive $PackageDirectory"
    }
}

function New-WindowsPackageArchive {
    param([string]$PackageDirectory)

    $archivePath = $PackageDirectory + ".zip"
    if (Test-Path $archivePath) {
        Remove-Item $archivePath -Force
    }

    Compress-Archive -Path $PackageDirectory -DestinationPath $archivePath -Force
}

function New-ZipArchiveFromDirectory {
    param(
        [string]$DirectoryPath,
        [string]$ArchivePath
    )

    if (Test-Path $ArchivePath) {
        Remove-Item $ArchivePath -Force
    }

    Compress-Archive -Path $DirectoryPath -DestinationPath $ArchivePath -Force
}

function New-ServerPackage {
    param([string]$PackagesRoot, [string]$BinaryPath, [string]$ConfigPath)

    $packageDir = Join-Path $PackagesRoot "1cat-tunnel-server-linux-amd64"
    Reset-Directory $packageDir

    Copy-PackageFile $BinaryPath $packageDir "tunnel-server"
    Copy-PackageFile $ConfigPath $packageDir "server.json"
    Copy-PackageFile (Join-Path $root "packaging\server\README.md") $packageDir "README.md"
    Copy-PackageFile (Join-Path $root "packaging\server\start-server.sh") $packageDir "start-server.sh"
    Copy-PackageFile (Join-Path $root "packaging\server\install-server-systemd.sh") $packageDir "install-server-systemd.sh"
    Copy-PackageFile (Join-Path $root "README.md") $packageDir "PROJECT-README.md"

    New-LinuxPackageArchive $packageDir
}

function New-LinuxClientPackage {
    param([string]$PackagesRoot, [string]$BinaryPath, [string]$ConfigPath)

    $packageDir = Join-Path $PackagesRoot "1cat-tunnel-client-linux-amd64"
    Reset-Directory $packageDir

    Copy-PackageFile $BinaryPath $packageDir "tunnel-client"
    Copy-PackageFile $ConfigPath $packageDir "client-linux.json"
    Copy-PackageFile (Join-Path $root "packaging\client-linux\README.md") $packageDir "README.md"
    Copy-PackageFile (Join-Path $root "packaging\client-linux\start-client.sh") $packageDir "start-client.sh"
    Copy-PackageFile (Join-Path $root "packaging\client-linux\install-client-systemd.sh") $packageDir "install-client-systemd.sh"
    Copy-PackageFile (Join-Path $root "README.md") $packageDir "PROJECT-README.md"

    New-LinuxPackageArchive $packageDir
}

function New-WindowsClientPackage {
    param([string]$PackagesRoot, [string]$BinaryPath, [string]$ConfigPath)

    $packageDir = Join-Path $PackagesRoot "1cat-tunnel-client-windows-amd64"
    Reset-Directory $packageDir

    Copy-PackageFile $BinaryPath $packageDir "tunnel-client.exe"
    Copy-PackageFile $ConfigPath $packageDir "client-windows.json"
    Copy-PackageFile (Join-Path $root "packaging\client-windows\README.md") $packageDir "README.md"
    Copy-PackageFile (Join-Path $root "packaging\client-windows\start-client.bat") $packageDir "start-client.bat"
    Copy-PackageFile (Join-Path $root "packaging\client-windows\start-client.ps1") $packageDir "start-client.ps1"
    Copy-PackageFile (Join-Path $root "README.md") $packageDir "PROJECT-README.md"

    New-WindowsPackageArchive $packageDir
}

function New-WindowsClientDeliveryFolder {
    param([string]$DeliveryDirectory, [string]$BinaryPath, [string]$ConfigPath)

    Reset-Directory $DeliveryDirectory

    Copy-PackageFile $BinaryPath $DeliveryDirectory "1cat Tunnel Client.exe"
    Copy-PackageFile $ConfigPath $DeliveryDirectory "client-windows.json"
    Copy-PackageFile (Join-Path $root "packaging\client-windows-delivery\README.txt") $DeliveryDirectory "README.txt"
    Copy-PackageFile (Join-Path $root "packaging\client-windows-delivery\Start 1cat Tunnel Client.bat") $DeliveryDirectory "Start 1cat Tunnel Client.bat"
    Copy-PackageFile (Join-Path $root "packaging\client-windows-delivery\Start 1cat Tunnel Client.ps1") $DeliveryDirectory "Start 1cat Tunnel Client.ps1"

    New-ZipArchiveFromDirectory -DirectoryPath $DeliveryDirectory -ArchivePath (Join-Path $dist "windows-client-delivery.zip")
}

function New-LinuxClientDeliveryFolder {
    param([string]$DeliveryDirectory, [string]$BinaryPath, [string]$ConfigPath)

    Reset-Directory $DeliveryDirectory

    Copy-PackageFile $BinaryPath $DeliveryDirectory "1cat-tunnel-client"
    Copy-PackageFile $ConfigPath $DeliveryDirectory "client-linux.json"
    Copy-PackageFile (Join-Path $root "packaging\client-linux-delivery\README.txt") $DeliveryDirectory "README.txt"
    Copy-PackageFile (Join-Path $root "packaging\client-linux-delivery\start-client.sh") $DeliveryDirectory "start-client.sh"
    Copy-PackageFile (Join-Path $root "packaging\client-linux-delivery\install-client-systemd.sh") $DeliveryDirectory "install-client-systemd.sh"

    New-LinuxPackageArchive $DeliveryDirectory
}

function New-ServerDockerDeliveryFolder {
    param([string]$DeliveryDirectory, [string]$BinaryPath)

    Reset-Directory $DeliveryDirectory
    New-Item -ItemType Directory -Force (Join-Path $DeliveryDirectory "data") | Out-Null

    Copy-PackageFile $BinaryPath $DeliveryDirectory "tunnel-server"
    Copy-PackageFile (Join-Path $root "packaging\server-docker\Dockerfile") $DeliveryDirectory "Dockerfile"
    Copy-PackageFile (Join-Path $root "packaging\server-docker\docker-compose.yml") $DeliveryDirectory "docker-compose.yml"
    Copy-PackageFile (Join-Path $root "packaging\server-docker\.dockerignore") $DeliveryDirectory ".dockerignore"
    Copy-PackageFile (Join-Path $root "packaging\server-docker\server.json") $DeliveryDirectory "server.json"
    Copy-PackageFile (Join-Path $root "packaging\server-docker\README.md") $DeliveryDirectory "README.md"

    New-LinuxPackageArchive $DeliveryDirectory
}

Reset-Directory $dist
New-Item -ItemType Directory -Force $packageRoot | Out-Null

Push-Location $root
try {
    $goCommand = Get-GoCommand
    Write-Host "Using Go toolchain: $goCommand"
    & $goCommand version

    $serverBinary = Join-Path $dist "tunnel-server-linux-amd64"
    $linuxClientBinary = Join-Path $dist "tunnel-client-linux-amd64"
    $windowsClientBinary = Join-Path $dist "tunnel-client-windows-amd64.exe"

    Invoke-GoBuild -GoCommand $goCommand -GOOS "linux" -GOARCH "amd64" -Output $serverBinary -Package ".\cmd\tunnel-server"
    Invoke-GoBuild -GoCommand $goCommand -GOOS "linux" -GOARCH "amd64" -Output $linuxClientBinary -Package ".\cmd\tunnel-client"
    Invoke-GoBuild -GoCommand $goCommand -GOOS "windows" -GOARCH "amd64" -Output $windowsClientBinary -Package ".\cmd\tunnel-client"

    $serverConfig = Join-Path $dist "server.json"
    $linuxClientConfig = Join-Path $dist "client-linux.json"
    $windowsClientConfig = Join-Path $dist "client-windows.json"

    Copy-Item .\examples\server.json $serverConfig -Force
    Copy-Item .\examples\client-linux.json $linuxClientConfig -Force
    Copy-Item .\examples\client-windows.json $windowsClientConfig -Force

    New-ServerPackage -PackagesRoot $packageRoot -BinaryPath $serverBinary -ConfigPath $serverConfig
    New-LinuxClientPackage -PackagesRoot $packageRoot -BinaryPath $linuxClientBinary -ConfigPath $linuxClientConfig
    New-WindowsClientPackage -PackagesRoot $packageRoot -BinaryPath $windowsClientBinary -ConfigPath $windowsClientConfig
    New-LinuxClientDeliveryFolder -DeliveryDirectory $linuxDeliveryDir -BinaryPath $linuxClientBinary -ConfigPath $linuxClientConfig
    New-ServerDockerDeliveryFolder -DeliveryDirectory $serverDockerDeliveryDir -BinaryPath $serverBinary
    New-WindowsClientDeliveryFolder -DeliveryDirectory $windowsDeliveryDir -BinaryPath $windowsClientBinary -ConfigPath $windowsClientConfig
}
finally {
    Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue
    Remove-Item Env:GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
    Pop-Location
}
