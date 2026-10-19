param([string]$Executable, [string]$CertFile, [string]$KeyFile, [string]$HTTPSHostname = "localhost")
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path $PSScriptRoot -Parent
$appVersion = [regex]::Match((Get-Content -Raw (Join-Path $projectRoot 'version.go')), 'const appVersion = "([^"]+)"').Groups[1].Value
if (-not $Executable) {
    $version = [regex]::Match((Get-Content -Raw (Join-Path $projectRoot 'version.go')), 'const appVersion = "([^"]+)"').Groups[1].Value
    $Executable = Join-Path (Join-Path $projectRoot "bin") "soundstreamer-$version.exe"
}
Add-Type @'
using System;
using System.Text;
using System.Runtime.InteropServices;
public static class SoundStreamerTraySmoke {
    // Pin only the supplied disposable test certificate; production validation is unchanged.
    public static string TestThumbprint;
    public static System.Net.Security.RemoteCertificateValidationCallback TestValidator = ValidateTestCertificate;
    public static bool ValidateTestCertificate(object sender, System.Security.Cryptography.X509Certificates.X509Certificate certificate, System.Security.Cryptography.X509Certificates.X509Chain chain, System.Net.Security.SslPolicyErrors errors) {
        return certificate != null && certificate.GetCertHashString() == TestThumbprint;
    }
    public delegate bool WindowCallback(IntPtr hwnd, IntPtr data);
    [DllImport("user32.dll")] static extern bool EnumWindows(WindowCallback callback, IntPtr data);
    [DllImport("user32.dll")] static extern uint GetWindowThreadProcessId(IntPtr hwnd, out uint pid);
    [DllImport("user32.dll", CharSet=CharSet.Unicode)] static extern int GetClassName(IntPtr hwnd, StringBuilder text, int size);
    [DllImport("user32.dll")] public static extern bool PostMessage(IntPtr hwnd, uint message, IntPtr wParam, IntPtr lParam);
    public static IntPtr FindTray(int targetPid) {
        IntPtr found = IntPtr.Zero;
        EnumWindows(delegate(IntPtr hwnd, IntPtr data) {
            uint pid; GetWindowThreadProcessId(hwnd, out pid);
            if (pid == targetPid) {
                StringBuilder text = new StringBuilder(256); GetClassName(hwnd, text, 256);
                if (text.ToString().StartsWith("SoundStreamer.Tray.")) { found = hwnd; return false; }
            }
            return true;
        }, IntPtr.Zero);
        return found;
    }
}
'@
$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('soundstreamer-tray-smoke-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory $testRoot | Out-Null
$process = $null
$oldCallback = [Net.ServicePointManager]::ServerCertificateValidationCallback
$oldProtocol = [Net.ServicePointManager]::SecurityProtocol
$useTLS = [bool]($CertFile -and $KeyFile)
try {
    if ([bool]$CertFile -ne [bool]$KeyFile) { throw 'Supply both test certificate and key paths' }
    if ($useTLS) {
        $cert = New-Object Security.Cryptography.X509Certificates.X509Certificate2($CertFile)
        [SoundStreamerTraySmoke]::TestThumbprint = $cert.Thumbprint
        [Net.ServicePointManager]::ServerCertificateValidationCallback = [SoundStreamerTraySmoke]::TestValidator
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
        @{version=1;httpsEnabled=$true;httpsHostname=$HTTPSHostname;certFile=$CertFile;keyFile=$KeyFile} | ConvertTo-Json | Set-Content (Join-Path $testRoot 'soundstreamer.json')
    }
    $info = New-Object Diagnostics.ProcessStartInfo
    $info.FileName = $Executable
    $info.Arguments = '-addr 127.0.0.1:0 -data-dir "' + $testRoot + '"'
    $info.UseShellExecute = $false
    $process = [Diagnostics.Process]::Start($info)
    $logPath = Join-Path $testRoot 'soundstreamer.log'
    $deadline = [DateTime]::UtcNow.AddSeconds(15)
    $match = $null
    do {
        Start-Sleep -Milliseconds 100
        if ($process.HasExited) { throw "App exited during startup ($($process.ExitCode))" }
        if (Test-Path $logPath) { $logText = Get-Content -Raw $logPath; if ($logText) { $match = [regex]::Match($logText, 'listening on 127\.0\.0\.1:(\d+)') } }
    } until (($match -and $match.Success) -or [DateTime]::UtcNow -gt $deadline)
    if (-not $match -or -not $match.Success) { throw 'App listener did not start' }
    $port = [int]$match.Groups[1].Value
    $scheme = if ($useTLS) { "https" } else { "http" }
    $base = "${scheme}://127.0.0.1:$port"
    if ($useTLS -and (Get-Content -Raw $logPath) -notlike "*Browser URL: https://${HTTPSHostname}:$port*") { throw 'Configured HTTPS hostname missing from tray/browser URL' }
    $state = Invoke-RestMethod "$base/api/control/state"
    if ($state.activePort -ne $port -or $state.version -ne $appVersion -or $state.activeHTTPS -ne $useTLS) { throw 'Listener/version mismatch' }
    if ($state.activeAudioChannels -ne 2 -or $state.activeAudioSampleRate -ne 48000) { throw 'Default audio is not stereo/48 kHz' }
    $window = [SoundStreamerTraySmoke]::FindTray($process.Id)
    if ($window -eq [IntPtr]::Zero) { throw 'Hidden tray window was not created' }
    $session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    Invoke-RestMethod "$base/api/admin/setup" -WebSession $session -Method Post -ContentType 'application/json' -Body '{"password":"tray-smoke-password"}' | Out-Null
    $savedPort = if ($port -ne 8123) { 8123 } else { 8124 }
    $state = Invoke-RestMethod "$base/api/admin/action" -WebSession $session -Method Post -ContentType 'application/json' -Body (ConvertTo-Json @{action='listenPort';to=$savedPort})
    if ($state.activePort -ne $port -or $state.port -ne $savedPort) { throw 'Saving port changed the active listener or failed to persist pending port' }
    $audioState = Invoke-RestMethod "$base/api/admin/action" -WebSession $session -Method Post -ContentType 'application/json' -Body '{"action":"audioSettings","audioChannels":1,"audioSampleRate":44100}'
    if ($audioState.audioChannels -ne 1 -or $audioState.audioSampleRate -ne 44100 -or $audioState.activeAudioChannels -ne 2 -or $audioState.activeAudioSampleRate -ne 48000) { throw 'Audio save changed the active format or failed to persist' }
    $licence = Invoke-WebRequest "$base/license" -UseBasicParsing
    if ($licence.Content -notlike '*MIT License*') { throw 'Embedded MIT licence unavailable' }
    $page = Invoke-WebRequest "$base/control" -UseBasicParsing
    if ($page.Content -notlike "*Sound Streamer v$appVersion*") { throw 'Version footer missing' }
    if (-not [SoundStreamerTraySmoke]::PostMessage($window,0x10,[IntPtr]::Zero,[IntPtr]::Zero)) { throw 'Could not request tray shutdown' }
    if (-not $process.WaitForExit(10000)) { throw 'Tray shutdown did not finish' }
    if ($process.ExitCode -ne 0) { throw "App failed during shutdown ($($process.ExitCode))" }
    $saved = Get-Content -Raw (Join-Path $testRoot 'soundstreamer.json') | ConvertFrom-Json
    if ($saved.port -ne $savedPort) { throw 'Saved port lost on exit' }
    if ($saved.audioChannels -ne 1 -or $saved.audioSampleRate -ne 44100) { throw 'Audio settings lost on exit' }
    if ($useTLS -and $saved.httpsHostname -ne $HTTPSHostname) { throw 'Saved HTTPS hostname lost on exit' }
    if ((Get-Content -Raw $logPath) -notlike '*Sound Streamer stopped*') { throw 'Clean shutdown not logged' }
    Write-Output "Windows $scheme tray smoke passed: GUI startup, embedded icon, hidden tray window, browser API, active/pending ports, version footer and clean saved-state shutdown."
} finally {
    [Net.ServicePointManager]::ServerCertificateValidationCallback = $oldCallback
    [Net.ServicePointManager]::SecurityProtocol = $oldProtocol
    if ($process -and -not $process.HasExited) { $process.Kill(); $process.WaitForExit() }
    Remove-Item -Recurse -Force $testRoot
}
