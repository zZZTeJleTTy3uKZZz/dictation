# Установка «Диктовки» одной командой (контракт публичного выпуска, §2–§3):
#
#   irm https://github.com/zZZTeJleTTy3uKZZz/dictation/releases/latest/download/install.ps1 | iex
#
# Сценарий берёт публичный манифест update/1, скачивает установщик
# Dictation-Setup.exe (kind installer, Windows x64), сверяет SHA-256 и запускает
# его тихо. По умолчанию установка для всех пользователей: нужны права
# администратора, поэтому без них сценарий перезапускает себя с запросом UAC.
# С параметром -CurrentUser ставится только для текущего пользователя, прав не нужно.
# Никаких приватных адресов, токенов и путей под конкретную машину нет.
#
# Параметры работают, если сценарий запущен файлом или через
# [scriptblock]::Create (см. README). Через «irm … | iex» параметров нет,
# поэтому установка идёт в режиме по умолчанию, для всех.
param(
    [string]$ManifestUrl = 'https://github.com/zZZTeJleTTy3uKZZz/dictation/releases/latest/download/update.json',
    [switch]$CurrentUser,
    # Служебный маркер перезапуска с UAC: второго перезапуска не делаем.
    [switch]$Elevated
)

$ErrorActionPreference = 'Stop'
$ScriptUrl = 'https://github.com/zZZTeJleTTy3uKZZz/dictation/releases/latest/download/install.ps1'

# Код выхода — тот, по которому родитель после перезапуска различает причины:
# 3 — идёт запись; 1 — прочий отказ сценария.
function Fail {
    param([string]$Message, [int]$Code = 1)
    Write-Host $Message -ForegroundColor Red
    exit $Code
}

function Test-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    return ([Security.Principal.WindowsPrincipal]$identity).IsInRole(
        [Security.Principal.WindowsBuiltInRole]::Administrator)
}

# Установка для всех нужна с правами администратора. Без них сценарий запускает
# себя же с запросом UAC и ждёт результат. Аргументы собираем одной строкой:
# Start-Process не заключает в кавычки элементы массива с пробелами.
if (-not $CurrentUser -and -not (Test-Administrator)) {
    if ($Elevated) {
        Fail 'Права администратора не получены: Windows не подтвердила запуск. Диктовка не установлена. Чтобы поставить только для себя, запустите сценарий с параметром -CurrentUser.'
    }
    Write-Host 'Установка для всех пользователей требует прав администратора. Windows сейчас запросит подтверждение (UAC).'
    if ($PSCommandPath) {
        $argumentLine = "-NoProfile -ExecutionPolicy Bypass -File `"$PSCommandPath`" -ManifestUrl `"$ManifestUrl`" -Elevated"
    } else {
        $command = "& ([scriptblock]::Create((irm '$ScriptUrl'))) -Elevated"
        $argumentLine = "-NoProfile -ExecutionPolicy Bypass -Command `"$command`""
    }
    try {
        $elevated = Start-Process -FilePath 'powershell.exe' -ArgumentList $argumentLine -Verb RunAs -Wait -PassThru
    } catch {
        Fail 'Права администратора не получены: запрос UAC отклонён или отменён. Диктовка не установлена. Чтобы поставить только для себя, запустите сценарий с параметром -CurrentUser.'
    }
    switch ($elevated.ExitCode) {
        0 {
            Write-Host 'Диктовка установлена для всех пользователей.' -ForegroundColor Green
            exit 0
        }
        3 {
            Fail 'Диктовка сейчас записывает разговор — установка отменена, файлы не тронуты. Закончите диктовку и повторите.' 3
        }
        default {
            Fail "Установка для всех не выполнена (код $($elevated.ExitCode)). Повторите или поставьте только для себя: сценарий с параметром -CurrentUser." $elevated.ExitCode
        }
    }
}

try {
    $manifest = Invoke-RestMethod -Uri $ManifestUrl -UseBasicParsing -TimeoutSec 30
} catch {
    Fail 'Не удалось проверить обновления — нет связи с GitHub.'
}

if ($manifest.protocol -ne 'update/1') {
    Fail 'Манифест обновления непонятен: неизвестный протокол.'
}

$asset = @($manifest.assets | Where-Object {
    $_.kind -eq 'installer' -and $_.os -eq 'windows' -and $_.arch -eq 'amd64'
}) | Select-Object -First 1
if (-not $asset -or -not $asset.url) {
    Fail 'В манифесте нет установщика для Windows x64.'
}
if ($asset.url -notmatch '^https://') {
    Fail 'Адрес установщика в манифесте небезопасен.'
}

$temporary = Join-Path ([System.IO.Path]::GetTempPath()) `
    ('dictation-' + [guid]::NewGuid().ToString('N'))
$setup = Join-Path $temporary 'Dictation-Setup.exe'
try {
    New-Item -ItemType Directory -Path $temporary -Force | Out-Null
    Write-Host 'Скачиваю Диктовку…'
    Invoke-WebRequest -Uri $asset.url -OutFile $setup -UseBasicParsing -TimeoutSec 300

    $actual = (Get-FileHash -LiteralPath $setup -Algorithm SHA256).Hash.ToLowerInvariant()
    if (-not $asset.sha256 -or $actual -ne $asset.sha256.ToLowerInvariant()) {
        Fail 'Сумма установщика не совпала с манифестом — установка прервана.'
    }

    # Тихие параметры установщика Inno Setup. Режим выбирает сценарий: по умолчанию
    # для всех (/ALLUSERS), с -CurrentUser — только для текущего пользователя.
    $arguments = @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART')
    if ($CurrentUser) {
        $arguments += '/CURRENTUSER'
        Write-Host 'Устанавливаю Диктовку только для текущего пользователя…'
    } else {
        $arguments += '/ALLUSERS'
        Write-Host 'Устанавливаю Диктовку для всех пользователей…'
    }
    $installed = Start-Process -FilePath $setup -ArgumentList $arguments -Wait -PassThru
    if ($installed.ExitCode -eq 3) {
        Fail 'Диктовка сейчас записывает разговор — установка отменена, файлы не тронуты. Закончите диктовку и повторите.' 3
    }
    if ($installed.ExitCode -ne 0) {
        Fail "Установка завершилась с кодом $($installed.ExitCode)." $installed.ExitCode
    }

    if ($CurrentUser) {
        $program = Join-Path $env:LOCALAPPDATA 'Programs\Dictation\dictation.exe'
        if (Test-Path -LiteralPath $program) {
            Start-Process -FilePath $program | Out-Null
        } else {
            Write-Host 'Готово. Запустите «Диктовку» из меню Пуск.'
        }
    } else {
        # Программа для всех поднимается службой Windows; из окна администратора
        # её не запускаем, чтобы значок не появился в сеансе администратора.
        Write-Host 'Готово. Диктовку поднимает служба Windows; если значка в трее нет, войдите в систему заново.'
    }
    Write-Host "Диктовка $($manifest.version) установлена." -ForegroundColor Green
} finally {
    if (Test-Path -LiteralPath $temporary) {
        Remove-Item -LiteralPath $temporary -Recurse -Force -ErrorAction SilentlyContinue
    }
}
