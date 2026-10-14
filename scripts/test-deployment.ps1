param(
    [switch]$SkipBuild,
    [ValidatePattern('^ai-library-[a-z0-9-]+-validation$')][string]$ProjectName = 'ai-library-react-validation',
    [ValidateRange(1024,65535)][int]$Port = 58089
)
$ErrorActionPreference = 'Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)
$docker = if (Test-Path -LiteralPath 'D:/Docker/Desktop/resources/bin/docker.exe') { 'D:/Docker/Desktop/resources/bin/docker.exe' } else { 'docker' }
$env:DATABASE_URL = 'postgresql://library:local-compose-test-only@postgres:5432/ai_library'
$env:POSTGRES_PASSWORD = 'local-compose-test-only'
$env:POSTGRES_USER = 'library'
$env:POSTGRES_DB = 'ai_library'
$env:RABBITMQ_USER = 'library'
$env:RABBITMQ_PASSWORD = 'local-compose-test-only'
$env:RABBITMQ_URL = 'amqp://library:local-compose-test-only@rabbitmq:5672/'
$env:JWT_SECRET = 'local-compose-test-secret-at-least-32-characters'
$env:AI_SERVICE_TOKEN = 'local-compose-service-token-at-least-32-characters'
$env:AI_PROVIDER_ENABLED = 'false'
$env:AUTO_EXTRACT_ENABLED = 'false'
$env:COMPOSE_PROFILES = ''
$env:WEB_PORT = [string]$Port
$env:BAILIAN_API_KEY = ''
$env:DEEPSEEK_API_KEY = ''
foreach ($key in @('OSS_REGION','OSS_BUCKET','OSS_ACCESS_KEY_ID','OSS_ACCESS_KEY_SECRET','OSS_SECURITY_TOKEN')) { Set-Item -LiteralPath ('Env:'+$key) -Value '' }
$prefix = @('compose','-p',$ProjectName,'-f','compose.go-python.yml')
function Compose([string[]]$Arguments) {
    & $docker @prefix @Arguments
    if ($LASTEXITCODE -ne 0) { throw 'Isolated compose command failed' }
}
function SQL([string]$Statement) {
    $output = $Statement | & $docker @prefix exec -T postgres psql -U library -d ai_library -At -v ON_ERROR_STOP=1
    if ($LASTEXITCODE -ne 0) { throw 'Isolated SQL probe failed' }
    return $output
}
try {
    Compose @('config','--quiet')
    if (!$SkipBuild) { Compose @('build','go-api','ai-api','web') }
    Compose @('up','-d','--no-build','--wait','--wait-timeout','120')
    foreach ($path in @('/','/knowledge-bases/00000000-0000-0000-0000-000000000000/chat','/health/live')) {
        $response = Invoke-WebRequest -UseBasicParsing -Uri ('http://127.0.0.1:'+$Port+$path) -TimeoutSec 5
        if ($response.StatusCode -ne 200) { throw 'HTTP smoke probe failed' }
        if ($path -eq '/' -and $response.Content -notmatch 'id="root"') { throw 'Deployed page is not the React entry point' }
    }
    foreach ($probe in @(@('/assets/missing.js',404),@('/health/ready',503),@('/api/auth/me',401))) {
        $status = 0
        try { $status = (Invoke-WebRequest -UseBasicParsing -Uri ('http://127.0.0.1:'+$Port+$probe[0]) -TimeoutSec 5).StatusCode }
        catch { if ($_.Exception.Response) { $status = [int]$_.Exception.Response.StatusCode } else { throw } }
        if ($status -ne $probe[1]) { throw ('Unexpected status for '+$probe[0]) }
    }
    $null = SQL "INSERT INTO `"user`"(id,username,email,password_hash,updated_at) VALUES('10000000-0000-4000-8000-000000000001','deployment_probe','deployment_probe@example.test','unused-local-probe',now()) ON CONFLICT(id) DO NOTHING"
    $null = SQL "INSERT INTO knowledge_base(id,user_id,name,updated_at) VALUES('20000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','deployment probe',now()) ON CONFLICT(id) DO NOTHING"
    Compose @('stop')
    Compose @('start','--wait','--wait-timeout','120')
    if ((SQL "SELECT count(*) FROM knowledge_base WHERE id='20000000-0000-4000-8000-000000000001' AND name='deployment probe'") -ne '1') { throw 'Persistent database probe was lost' }
    $null = SQL "DELETE FROM `"user`" WHERE id='10000000-0000-4000-8000-000000000001'"
    Write-Output 'Deployment smoke, disabled-provider readiness, restart and volume persistence passed.'
} finally {
    Compose @('stop')
}
