package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// installSwapHarness runs install.ps1's swap, the lines between its swap:start and swap:end marks,
// in a folder of its own, and prints what it saw. Each command the swap uses to change files is
// stood in for by one that runs the real command and then looks for mur.exe: a swap stopped after
// any one of them must still leave it there.
const installSwapHarness = `
param([string]$Swap, [string]$Root, [string]$State)
$ErrorActionPreference = 'Stop'
$dir = Join-Path $Root 'bin'
$tmp = Join-Path $Root 'download'
New-Item -ItemType Directory -Path $dir, $tmp | Out-Null
$exe = Join-Path $dir 'mur.exe'
$asset = 'mur-windows-amd64.exe'
$old = $null
Set-Content -Path (Join-Path $tmp $asset) -Value 'NEW' -Encoding ascii
$running = $null
if ($State -ne 'none') { Copy-Item "$env:WINDIR\System32\waitfor.exe" $exe }
if ($State -eq 'read-only') { (Get-Item $exe).IsReadOnly = $true }
if ($State -like 'running*') {
    # A signal name of its own: a second waitfor on one name exits at once.
    $running = Start-Process -FilePath $exe -ArgumentList ('MurSwap' + [Guid]::NewGuid().ToString('N').Substring(0, 12)), '/T', '120' -PassThru -WindowStyle Hidden
    Start-Sleep -Milliseconds 700
    if ($running.HasExited) { throw 'the stand-in mur.exe did not stay running' }
}
if ($State -like '*no call') { function Add-Type { throw 'this PowerShell may not load the call' } }
$had = Test-Path $exe
function Watch($step) { if ($had -and -not (Test-Path $exe)) { Write-Output "no mur.exe after $step" } }
function Move-Item { Microsoft.PowerShell.Management\Move-Item @args; Watch 'Move-Item' }
function Copy-Item { Microsoft.PowerShell.Management\Copy-Item @args; Watch 'Copy-Item' }
function New-Item { Microsoft.PowerShell.Management\New-Item @args; Watch 'New-Item' }
function Remove-Item {
    if ($MyInvocation.ExpectingInput) { $input | Microsoft.PowerShell.Management\Remove-Item @args } else { Microsoft.PowerShell.Management\Remove-Item @args }
    Watch 'Remove-Item'
}
try {
    . ([scriptblock]::Create((Get-Content -Raw $Swap)))
    Watch 'the swap'
    Write-Output ("mur.exe holds " + (Get-Content -Raw $exe).Trim())
    if ($running) { Write-Output ("the old one still runs: " + (-not $running.HasExited)) }
    Write-Output ("staged files left: " + @(Get-ChildItem $dir -Filter 'mur.exe.new-*').Count)
} finally {
    if ($running) { Stop-Process -Id $running.Id -Force -ErrorAction SilentlyContinue; $running.WaitForExit(5000) | Out-Null }
}
`

// install.ps1 puts the new mur.exe in place so that stopping it at any step leaves a whole mur.exe,
// old or new, also while the old one runs, as the daemon's does (#959). Before, it moved the old
// file aside first, and a stop before the new one came left none.
func TestInstallScriptNeverLeavesNoExe(t *testing.T) {
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("no powershell.exe")
	}
	script, err := os.ReadFile(filepath.Join("..", "..", "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	_, rest, found := strings.Cut(string(script), "# swap:start")
	swap, _, ended := strings.Cut(rest, "# swap:end")
	if !found || !ended {
		t.Fatal("install.ps1 has no swap:start and swap:end marks")
	}
	_, swap, _ = strings.Cut(swap, "\n") // the rest of the swap:start line is a comment
	for _, c := range []struct {
		state string
		want  []string
	}{
		{"running", []string{"mur.exe holds NEW", "the old one still runs: True", "staged files left: 0"}},
		// Constrained language mode, say: with the link made, the old way's move still finds a
		// mur.exe to replace.
		{"running, no call", []string{"mur.exe holds NEW", "the old one still runs: True", "staged files left: 0"}},
		{"idle", []string{"mur.exe holds NEW", "staged files left: 0"}},
		{"read-only", []string{"mur.exe holds NEW", "staged files left: 0"}},
		{"none", []string{"mur.exe holds NEW", "staged files left: 0"}},
	} {
		t.Run(c.state, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, text string) string {
				path := filepath.Join(dir, name)
				if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
				return path
			}
			out, err := exec.Command(powershell, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", write("harness.ps1", installSwapHarness),
				"-Swap", write("swap.ps1", swap), "-Root", dir, "-State", c.state).CombinedOutput()
			if err != nil {
				t.Fatalf("the harness: %v\n%s", err, out)
			}
			got := strings.Fields(strings.ReplaceAll(string(out), "\r\n", "\n"))
			if strings.Join(got, " ") != strings.Join(strings.Fields(strings.Join(c.want, " ")), " ") {
				t.Errorf("got\n%s\nwant\n%s", out, strings.Join(c.want, "\n"))
			}
		})
	}
}
