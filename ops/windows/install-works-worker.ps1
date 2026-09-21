[CmdletBinding()]
param(
  [Parameter(Mandatory=$true)][string]$WorkerExe,
  [Parameter(Mandatory=$true)][string]$ApiUrl,
  [string]$WorkerId='wrkr_jonas_lenovo',
  [string]$Pool='jonas-lenovo',
  [ValidateSet('untrusted','standard','privileged')][string]$TrustClass='standard',
  [string]$TaskName='Aftergraph WORKS Worker',
  [string]$InstallDir=(Join-Path $env:LOCALAPPDATA 'Aftergraph\WORKS')
)

$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest

if (-not (Test-Path -LiteralPath $WorkerExe -PathType Leaf)) { throw 'WorkerExe not found' }
$u=[Uri]$ApiUrl
if ($u.Scheme -ne 'https' -and -not ($u.Scheme -eq 'http' -and @('127.0.0.1','localhost','::1') -contains $u.Host)) { throw 'ApiUrl must use HTTPS unless loopback' }

New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
$installedExe=Join-Path $InstallDir 'works-worker.exe'
Copy-Item -LiteralPath (Resolve-Path -LiteralPath $WorkerExe).Path -Destination $installedExe -Force

$enroll=Read-Host 'WORKS enrollment secret' -AsSecureString
$github=Read-Host 'GitHub token for private source checkout (Enter for none)' -AsSecureString
$secretFile=Join-Path $InstallDir 'worker.secrets.json'
[ordered]@{version=1;worksEnrollSecret=(ConvertFrom-SecureString $enroll);githubToken=(ConvertFrom-SecureString $github)} | ConvertTo-Json | Set-Content -LiteralPath $secretFile -Encoding UTF8

$acl=New-Object System.Security.AccessControl.FileSecurity
$acl.SetAccessRuleProtection($true,$false)
$current=[Security.Principal.WindowsIdentity]::GetCurrent().Name
foreach($identity in @($current,'NT AUTHORITY\SYSTEM')) {
  $rule=New-Object System.Security.AccessControl.FileSystemAccessRule($identity,[System.Security.AccessControl.FileSystemRights]::FullControl,[System.Security.AccessControl.AccessControlType]::Allow)
  [void]$acl.AddAccessRule($rule)
}
Set-Acl -LiteralPath $secretFile -AclObject $acl

$launcher=Join-Path $InstallDir 'Start-AftergraphWorksWorker.ps1'
$q=[char]39
$launcherLines=@(
  '$ErrorActionPreference=' + $q + 'Stop' + $q,
  '$cfg=Get-Content -Raw -LiteralPath ' + $q + $secretFile + $q + ' | ConvertFrom-Json',
  'function U([string]$c){ if([string]::IsNullOrWhiteSpace($c)){return ' + $q + $q + '}; $s=ConvertTo-SecureString $c; $p=[Runtime.InteropServices.Marshal]::SecureStringToBSTR($s); try{[Runtime.InteropServices.Marshal]::PtrToStringBSTR($p)}finally{[Runtime.InteropServices.Marshal]::ZeroFreeBSTR($p)} }',
  '$env:WORKS_API=' + $q + $ApiUrl + $q,
  '$env:WORKS_WORKER_ID=' + $q + $WorkerId + $q,
  '$env:WORKS_POOL=' + $q + $Pool + $q,
  '$env:WORKS_TRUST_CLASS=' + $q + $TrustClass + $q,
  '$env:WORKS_ARTIFACTS=' + $q + (Join-Path $InstallDir 'artifacts') + $q,
  '$env:WORKS_ENROLL_SECRET=U $cfg.worksEnrollSecret',
  '$env:WORKS_GITHUB_TOKEN=U $cfg.githubToken',
  '& ' + $q + $installedExe + $q,
  'exit $LASTEXITCODE'
)
Set-Content -LiteralPath $launcher -Value $launcherLines -Encoding UTF8

$arg='-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "' + $launcher + '"'
$action=New-ScheduledTaskAction -Execute 'powershell.exe' -Argument $arg
$trigger=New-ScheduledTaskTrigger -AtLogOn -User $current
$settings=New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 10 -RestartInterval (New-TimeSpan -Minutes 1)
Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Settings $settings -Description 'Aftergraph WORKS native Windows worker' -Force | Out-Null
Start-ScheduledTask -TaskName $TaskName

Write-Host ('Installed native WORKS worker: id=' + $WorkerId + ' pool=' + $Pool)
Write-Host ('Verify: works runners --pool ' + $Pool + ' --alive')
