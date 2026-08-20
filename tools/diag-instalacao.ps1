# Diagnóstico da instalação do RustDesk Plus.
#
# Rode na máquina onde a senha não funciona, em PowerShell COMO ADMINISTRADOR
# (sem elevação, as pastas do systemprofile dão falso negativo em Test-Path), e
# envie o arquivo gerado.
#
# O que ele responde, que é o que separa as hipóteses:
#  - o log do instalador (cada tentativa de --password e se a chave apareceu);
#  - se o cliente com marca usa mesmo as pastas/serviço "RustDesk" ou o nome do
#    app — se usar o nome do app, o instalador está escrevendo no lugar errado;
#  - se o valor da senha no config do SERVIÇO é o mesmo do config do USUÁRIO. O
#    instalador só confere que a chave existe, não que o valor confere: se o
#    serviço regravou o arquivo com a senha antiga, a instalação termina "ok" e
#    a conexão recusa a senha. Os valores saem como hash curto, não em claro.
#
# password/salt nunca são impressos: só presença e hash de 12 caracteres.

$out = "$env:USERPROFILE\Desktop\rustdesk-diag.txt"

function Short-Hash {
    param([string]$Value)
    if ([string]::IsNullOrEmpty($Value)) { return '<vazio>' }
    $sha = [System.Security.Cryptography.SHA256]::Create()
    $h = $sha.ComputeHash([System.Text.Encoding]::UTF8.GetBytes($Value))
    ([System.BitConverter]::ToString($h) -replace '-', '').Substring(0, 12).ToLower()
}

# Extrai chave = 'valor' de um .toml, devolvendo hash em vez do valor para os
# campos sensíveis.
function Toml-Value {
    param([string]$Path, [string]$Key, [switch]$Hashed)
    if (-not (Test-Path $Path)) { return '<arquivo ausente>' }
    $line = Select-String -Path $Path -Pattern "^\s*$([regex]::Escape($Key))\s*=" -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if (-not $line) { return '<ausente>' }
    $v = ($line.Line -split '=', 2)[1].Trim().Trim("'").Trim('"')
    if ($Hashed) { return "hash:$(Short-Hash $v)" }
    return $v
}

function Dump-ConfigDir {
    param([string]$Label, [string]$Dir)
    "", "=== $Label ===", $Dir | Add-Content $out
    if (-not (Test-Path $Dir)) { "  (pasta não existe / sem acesso)" | Add-Content $out; return }
    Get-ChildItem $Dir -File -ErrorAction SilentlyContinue | ForEach-Object {
        "--- $($_.Name)  ($($_.Length) bytes, $($_.LastWriteTime))" | Add-Content $out
        if ($_.Name -like 'RustDesk*hwcodec*' -or $_.Name -like '*_local.toml') { "  (omitido)"; return }
        (Get-Content $_.FullName) -replace '^\s*(password|salt|permanent-password|enc_id)\s*=.*', '$1 = <presente>' |
            Add-Content $out
    }
}

$svcCfg  = 'C:\Windows\System32\config\systemprofile\AppData\Roaming\RustDesk\config'
$svcCfg2 = 'C:\Windows\SysWOW64\config\systemprofile\AppData\Roaming\RustDesk\config'
$usrCfg  = "$env:APPDATA\RustDesk\config"

"=== gerado em $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss') ===" | Set-Content $out
$admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
    [Security.Principal.WindowsBuiltInRole]::Administrator)
"elevado (administrador): $admin" | Add-Content $out
if (-not $admin) { "ATENÇÃO: sem elevação as pastas do serviço aparecem como inexistentes." | Add-Content $out }

# ── O ponto central: a senha do serviço é a mesma do usuário? ─────────────────
"", "=== comparação da senha (usuário x serviço) ===" | Add-Content $out
foreach ($pair in @(@{L = 'usuário'; P = $usrCfg }, @{L = 'serviço System32'; P = $svcCfg }, @{L = 'serviço SysWOW64'; P = $svcCfg2 })) {
    $toml = Join-Path $pair.P 'RustDesk.toml'
    $t2   = Join-Path $pair.P 'RustDesk2.toml'
    "--- $($pair.L)" | Add-Content $out
    "  RustDesk.toml            : $(if (Test-Path $toml) { 'existe' } else { 'AUSENTE' })" | Add-Content $out
    "  password                 : $(Toml-Value $toml 'password' -Hashed)"                  | Add-Content $out
    "  salt                     : $(Toml-Value $toml 'salt' -Hashed)"                      | Add-Content $out
    "  enc_id                   : $(Toml-Value $toml 'enc_id' -Hashed)"                    | Add-Content $out
    "  permanent-password       : $(Toml-Value $t2 'permanent-password' -Hashed)"          | Add-Content $out
    "  verification-method      : $(Toml-Value $t2 'verification-method')"                 | Add-Content $out
    "  approve-mode             : $(Toml-Value $t2 'approve-mode')"                        | Add-Content $out
    "  custom-rendezvous-server : $(Toml-Value $t2 'custom-rendezvous-server')"            | Add-Content $out
}
"(hashes iguais entre usuário e serviço = a senha foi propagada; diferentes = o serviço regravou por cima)" |
    Add-Content $out

# ── O cliente com marca usa as pastas/serviço "RustDesk" ou o nome do app? ────
"", "=== serviços que apontam para Program Files ===" | Add-Content $out
Get-CimInstance Win32_Service -ErrorAction SilentlyContinue |
    Where-Object { $_.PathName -match 'Program Files' -and $_.PathName -match 'service|rustdesk|remoto|remote' } |
    ForEach-Object { "  [$($_.State)] $($_.Name)  ->  $($_.PathName)" } | Add-Content $out

"", "=== pastas de config candidatas (qualquer nome de app) ===" | Add-Content $out
foreach ($root in @($env:APPDATA,
                    'C:\Windows\System32\config\systemprofile\AppData\Roaming',
                    'C:\Windows\SysWOW64\config\systemprofile\AppData\Roaming',
                    'C:\ProgramData')) {
    Get-ChildItem $root -Directory -ErrorAction SilentlyContinue | ForEach-Object {
        $cfg = Join-Path $_.FullName 'config'
        $hit = Get-ChildItem $cfg -Filter '*.toml' -ErrorAction SilentlyContinue
        if ($hit) { "  $cfg  ->  $($hit.Name -join ', ')" | Add-Content $out }
    }
}

"", "=== executáveis instalados ===" | Add-Content $out
Get-ChildItem 'C:\Program Files' -Directory -ErrorAction SilentlyContinue |
    Where-Object { $_.Name -match 'RustDesk|Remoto|Remote|Trivio' } | ForEach-Object {
        "--- $($_.FullName)" | Add-Content $out
        Get-ChildItem $_.FullName -Filter *.exe -ErrorAction SilentlyContinue |
            ForEach-Object { "  $($_.Name)  $($_.Length) bytes  v$($_.VersionInfo.FileVersion)" } | Add-Content $out
    }

"", "=== log do instalador ($env:TEMP\rustdesk-install.log) ===" | Add-Content $out
if (Test-Path "$env:TEMP\rustdesk-install.log") {
    Get-Content "$env:TEMP\rustdesk-install.log" | Add-Content $out
} else {
    "  (não encontrado nesta conta — veja o %TEMP% do usuário que rodou o instalador)" | Add-Content $out
}

"", "=== serviço RustDesk ===" | Add-Content $out
sc.exe query RustDesk | Add-Content $out
sc.exe qc RustDesk    | Add-Content $out

Dump-ConfigDir 'config do serviço (System32)' $svcCfg
Dump-ConfigDir 'config do serviço (SysWOW64)' $svcCfg2
Dump-ConfigDir 'config do usuário'            $usrCfg

"", "=== Defender: exclusões ativas ===" | Add-Content $out
try { (Get-MpPreference -ErrorAction Stop).ExclusionPath | Add-Content $out }
catch { "  (Get-MpPreference indisponível)" | Add-Content $out }

"", "=== Defender: detecções recentes (arquivos) ===" | Add-Content $out
try {
    Get-MpThreatDetection -ErrorAction Stop | Sort-Object InitialDetectionTime -Descending |
        Select-Object -First 10 |
        ForEach-Object { "  $($_.InitialDetectionTime)  $($_.ThreatID)  $(($_.Resources | Where-Object { $_ -like 'file:*' }) -join '; ')" } |
        Add-Content $out
} catch { "  (Get-MpThreatDetection indisponível)" | Add-Content $out }

Write-Host "Diagnóstico salvo em $out" -ForegroundColor Green
notepad $out
