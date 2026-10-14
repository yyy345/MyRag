param(
    [switch]$SkipBrowser,
    [switch]$Deployment,
    [switch]$KeepInfrastructure
)

# All integrations use fixed local endpoints and SDK MockTransport fixtures.
# No dotenv, dependency installation or real provider worker is started here.
$ErrorActionPreference = 'Stop'
$taskRoot = Split-Path -Parent $PSScriptRoot
$taskReportPath = Join-Path $taskRoot '.cache/migration-validation.json'
$taskSteps = New-Object 'System.Collections.Generic.List[object]'
$taskOriginalEnv = @{}
$taskInitialServices = @()
$taskInfrastructureAttempted = $false
$taskExitCode = 0
$taskReport = [ordered]@{
    schema_version = 1
    started_at = [DateTime]::UtcNow.ToString('o')
    finished_at = $null
    status = 'running'
    external_providers = 'disabled; SDK mock only'
    browser = if ($SkipBrowser) { 'skipped' } else { 'requested' }
    deployment = if ($Deployment) { 'requested' } else { 'not requested' }
    steps = @()
}

function Set-TestEnvironment([string]$Name, [string]$Value) {
    if (!$taskOriginalEnv.ContainsKey($Name)) {
        $taskOriginalEnv[$Name] = [Environment]::GetEnvironmentVariable($Name, 'Process')
    }
    [Environment]::SetEnvironmentVariable($Name, $Value, 'Process')
}

function Find-Executable([string]$Name, [string]$Preferred) {
    if ($Preferred -and (Test-Path -LiteralPath $Preferred -PathType Leaf)) { return $Preferred }
    $taskCommand = Get-Command $Name -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if (!$taskCommand) { throw ('Required executable not found: ' + $Name) }
    return $taskCommand.Source
}

function Invoke-Check([string]$Name, [string]$File, [string[]]$Arguments, [string]$Directory = $taskRoot) {
    Write-Host ('Checking ' + $Name)
    $taskTimer = [Diagnostics.Stopwatch]::StartNew()
    $taskStep = [ordered]@{name = $Name; status = 'failed'; seconds = 0}
    Push-Location -LiteralPath $Directory
    try {
        & $File @Arguments
        if ($LASTEXITCODE -ne 0) { throw ($Name + ' failed with exit code ' + $LASTEXITCODE) }
        $taskStep.status = 'passed'
    } finally {
        Pop-Location
        $taskTimer.Stop()
        $taskStep.seconds = [Math]::Round($taskTimer.Elapsed.TotalSeconds, 2)
        $taskSteps.Add($taskStep)
    }
}

Push-Location -LiteralPath $taskRoot
try {
    $taskGo = Find-Executable 'go' $(if ($env:GO_BINARY) { $env:GO_BINARY } else { 'D:/Go/bin/go.exe' })
    $taskDocker = Find-Executable 'docker' 'D:/Docker/Desktop/resources/bin/docker.exe'
    $taskNode = Find-Executable 'node' ''
    $taskNpm = Find-Executable 'npm.cmd' ''
    $taskPython = if ($env:PYTHON_BINARY) { $env:PYTHON_BINARY } else { Join-Path $taskRoot 'ai-service/.venv/Scripts/python.exe' }
    foreach ($taskPath in @($taskPython, (Join-Path $taskRoot 'node_modules/@playwright/test/cli.js'))) {
        if (!(Test-Path -LiteralPath $taskPath -PathType Leaf)) { throw 'Install the locked development dependencies before validation.' }
    }
    Set-TestEnvironment 'PATH' ((Split-Path -Parent $taskDocker) + ';' + $env:PATH)
    Set-TestEnvironment 'GO_BINARY' $taskGo
    Set-TestEnvironment 'PYTHON_BINARY' $taskPython
    foreach ($taskKey in @('RUN_DATABASE_TESTS','RUN_REDIS_TESTS','RUN_RABBITMQ_TESTS','RUN_PYTHON_TESTS')) { Set-TestEnvironment $taskKey '1' }
    Set-TestEnvironment 'DATABASE_URL' 'postgresql://postgres:migration-test-only@127.0.0.1:55432/migration_test'
    Set-TestEnvironment 'REDIS_URL' 'redis://127.0.0.1:56380/0'
    Set-TestEnvironment 'RABBITMQ_URL' 'amqp://guest:guest@127.0.0.1:55673/'
    Set-TestEnvironment 'JWT_SECRET' 'local-validation-secret-at-least-32-characters'
    Set-TestEnvironment 'AI_SERVICE_TOKEN' 'local-validation-token-at-least-32-characters'
    Set-TestEnvironment 'AI_PROVIDER_ENABLED' 'false'
    Set-TestEnvironment 'AI_RETRIEVAL_MODE' 'hybrid'
    Set-TestEnvironment 'AUTO_EXTRACT_ENABLED' 'false'
    Set-TestEnvironment 'MIGRATION_EXTERNAL_AI' '0'
    Set-TestEnvironment 'COMPOSE_PROFILES' ''
    foreach ($taskVariable in @(Get-ChildItem Env: | Where-Object { $_.Name -match '^(BAILIAN_|DEEPSEEK_|OSS_)' })) { Set-TestEnvironment $taskVariable.Name '' }
    foreach ($taskCache in @(@('GOMODCACHE','go-mod-validation'), @('GOCACHE','go-build-validation'))) {
        $taskCachePath = Join-Path $taskRoot ('.cache/' + $taskCache[1])
        if (Test-Path -LiteralPath $taskCachePath -PathType Container) { Set-TestEnvironment $taskCache[0] $taskCachePath }
    }
    Invoke-Check 'Python runtime and locked dependencies' $taskPython @('-c','import ctypes, psycopg, aio_pika, fastapi, langchain_openai; print(fastapi.__version__)')
    $taskCompose = @('compose','-f','compose.migration-test.yml')
    $taskInitialServices = @(& $taskDocker @taskCompose ps --services --filter status=running)
    if ($LASTEXITCODE -ne 0) { throw 'Cannot inspect isolated test infrastructure.' }
    $taskInfrastructureAttempted = $true
    Invoke-Check 'Isolated PostgreSQL, Redis and RabbitMQ' $taskDocker ($taskCompose + @('up','-d','--wait'))
    Invoke-Check 'Go migrations on the isolated database' $taskGo @('run','./cmd/migrate') (Join-Path $taskRoot 'backend')
    Invoke-Check 'Go full integration tests' $taskGo @('test','./...','-count=1') (Join-Path $taskRoot 'backend')
    Invoke-Check 'Go build' $taskGo @('build','./...') (Join-Path $taskRoot 'backend')
    Invoke-Check 'Go vet' $taskGo @('vet','./...') (Join-Path $taskRoot 'backend')
    Invoke-Check 'Python full integration tests' $taskPython @('-m','unittest','discover','-s','tests','-q') (Join-Path $taskRoot 'ai-service')
    Invoke-Check 'Python syntax' $taskPython @('-m','compileall','-q','app','tests') (Join-Path $taskRoot 'ai-service')
    Invoke-Check 'React types and production build' $taskNpm @('run','build')
    Invoke-Check 'React browser test types' $taskNpm @('run','typecheck:e2e','--workspace','client')
    if (!$SkipBrowser) {
        Invoke-Check 'React + Go + Python browser workflow' $taskNode @('scripts/run-go-e2e.cjs')
        $taskReport.browser = 'passed'
    }
    if ($Deployment) {
        Invoke-Check 'Container deployment and persistent data' 'powershell.exe' @('-NoProfile','-ExecutionPolicy','Bypass','-File','scripts/test-deployment.ps1')
        $taskReport.deployment = 'passed'
    }
    $taskReport.status = 'passed'
} catch {
    $taskExitCode = 1
    $taskReport.status = 'failed'
    Write-Host ('Validation stopped: ' + $_.Exception.Message)
} finally {
    if ($taskInfrastructureAttempted -and !$KeepInfrastructure) {
        $taskNewServices = @('postgres','redis','rabbitmq') | Where-Object { $_ -notin $taskInitialServices }
        if ($taskNewServices) {
            & $taskDocker @taskCompose stop @taskNewServices
            if ($LASTEXITCODE -ne 0) { $taskExitCode = 1; $taskReport.status = 'failed'; Write-Host 'Test infrastructure cleanup failed.' }
        }
    }
    foreach ($taskKey in $taskOriginalEnv.Keys) { [Environment]::SetEnvironmentVariable($taskKey, $taskOriginalEnv[$taskKey], 'Process') }
    $null = New-Item -ItemType Directory -Force -Path (Split-Path -Parent $taskReportPath)
    $taskReport.finished_at = [DateTime]::UtcNow.ToString('o')
    $taskReport.steps = $taskSteps.ToArray()
    $taskReport | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $taskReportPath -Encoding UTF8
    Write-Host ('Validation ' + $taskReport.status + '; report: .cache/migration-validation.json')
    Pop-Location
}
exit $taskExitCode
