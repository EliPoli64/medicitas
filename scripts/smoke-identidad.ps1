# Smoke identidad MediCitas (Keycloak + API + Postgres).
# Uso: con Compose arriba, desde la raíz del repo:
#   powershell -File scripts/smoke-identidad.ps1

$ErrorActionPreference = "Stop"
$api = if ($env:API_BASE) { $env:API_BASE } else { "http://localhost:8080" }
$kc = if ($env:KC_BASE) { $env:KC_BASE } else { "http://localhost:6767" }

Write-Host "Esperando $api/ready ..."
$deadline = (Get-Date).AddMinutes(2)
do {
    try {
        $r = Invoke-WebRequest -Uri "$api/ready" -UseBasicParsing -TimeoutSec 3
        if ($r.StatusCode -eq 200) { break }
    } catch { }
    Start-Sleep -Seconds 2
} while ((Get-Date) -lt $deadline)

$ready = Invoke-RestMethod -Uri "$api/ready"
if ($ready.status -ne "OK") { throw "/ready no OK: $($ready | ConvertTo-Json)" }
Write-Host "ready OK"

function Get-Token([string]$user, [string]$pass) {
    $resp = Invoke-RestMethod -Method Post -Uri "$kc/realms/medicitas/protocol/openid-connect/token" -Body @{
        client_id  = "api"
        username   = $user
        password   = $pass
        grant_type = "password"
    }
    return $resp.access_token
}

$tok = Get-Token "recep-norte" "recep123"
$me = Invoke-RestMethod -Uri "$api/me" -Headers @{ Authorization = "Bearer $tok" }
if (-not $me.usuarioId) { throw "/me sin usuarioId" }
if ($me.clinicaIdentificador -ne "clinica-norte") { throw "/me clinica=$($me.clinicaIdentificador)" }
Write-Host "me OK usuarioId=$($me.usuarioId)"

$pac = Invoke-RestMethod -Uri "$api/pacientes?clinicaId=clinica-sur" -Headers @{ Authorization = "Bearer $tok" }
if ($pac.clinicaIdentificador -ne "clinica-norte") { throw "I5 fail clinica=$($pac.clinicaIdentificador)" }
if ($pac.pacientes.Count -lt 1) { throw "sin pacientes" }
Write-Host "pacientes OK count=$($pac.pacientes.Count) (query sur ignorado)"

$plat = Get-Token "plat-admin" "plat123"
try {
    Invoke-RestMethod -Uri "$api/pacientes" -Headers @{ Authorization = "Bearer $plat" }
    throw "plat-admin debio 403"
} catch {
    $code = $_.Exception.Response.StatusCode.value__
    if ($code -ne 403) { throw "plat-admin status=$code want 403" }
}
Write-Host "plat-admin /pacientes 403 OK"
Write-Host "SMOKE IDENTIDAD OK"
