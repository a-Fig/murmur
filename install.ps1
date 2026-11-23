# Installs Murmur's command, mur, on Windows: downloads the latest release's binary for this
# machine, checks it against the release's SHA256SUMS, puts it in %USERPROFILE%\.local\bin, and
# adds that folder to your user PATH if it isn't there. It needs no administrator rights. macOS and
# Linux have install.sh.
#
#   irm https://raw.githubusercontent.com/a-Fig/murmur/main/install.ps1 | iex
#
# Environment:
#   MURMUR_VERSION  install this release (v0.1.0 or 0.1.0) instead of the latest stable one
#   GITHUB_TOKEN    download through the GitHub API, which also works for a private copy of the repo
#   MURMUR_REPO     fetch releases of this repo (owner/name) instead of a-Fig/murmur
#
# docs/releasing.md says where the binaries come from and what the checksum does and doesn't prove.
#
# The work is in a script block so that running this through iex doesn't leave variables behind
# in your session, and it throws rather than exits, because exit would close your window.
& {
    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue'   # the progress bar makes Windows PowerShell 5.1 downloads far slower
    # Windows PowerShell 5.1 may not offer TLS 1.2 by default, and GitHub requires it.
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    $repo = if ($env:MURMUR_REPO) { $env:MURMUR_REPO } else { 'a-Fig/murmur' }
    $token = $env:GITHUB_TOKEN
    $tag = $null
    if ($env:MURMUR_VERSION) {
        $tag = if ($env:MURMUR_VERSION.StartsWith('v')) { $env:MURMUR_VERSION } else { "v$($env:MURMUR_VERSION)" }
    }

    # A 32-bit PowerShell on 64-bit Windows reports x86 here; PROCESSOR_ARCHITEW6432 has the real one.
    $machine = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    switch ($machine) {
        'AMD64' { $arch = 'amd64' }
        'ARM64' { $arch = 'arm64' }
        default { throw "mur install: unsupported architecture $machine; Murmur has Windows binaries for amd64 and arm64." }
    }
    $asset = "mur-windows-$arch.exe"

    $dir = Join-Path $env:USERPROFILE '.local\bin'
    $exe = Join-Path $dir 'mur.exe'
    $old = $null
    $tmp = Join-Path ([IO.Path]::GetTempPath()) ("mur-install-" + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp | Out-Null

    try {
        if ($token) {
            # releases/latest/download needs no login only on a public repo; a private one is read
            # through the API. The API names assets by id, and sends the file itself from a redirect
            # that must not get the token (its storage host rejects a request that carries two kinds
            # of credentials), so the redirect is followed by hand.
            $api = "https://api.github.com/repos/$repo/releases/" + $(if ($tag) { "tags/$tag" } else { 'latest' })
            $headers = @{ Authorization = "Bearer $token"; Accept = 'application/vnd.github+json'; 'User-Agent' = 'mur-install' }
            try {
                $release = Invoke-RestMethod -Uri $api -Headers $headers
            } catch {
                throw "mur install: could not read the release at $api ($($_.Exception.Message)). Is the token valid, with access to $repo, and does the release exist? 'latest' skips prereleases; pin one with MURMUR_VERSION. To download without a token, remove GITHUB_TOKEN."
            }
            function Get-Asset($name) {
                $entry = @($release.assets | Where-Object { $_.name -eq $name })[0]
                if (-not $entry) { throw "mur install: the release has no asset named $name." }
                $request = [Net.HttpWebRequest]::Create($entry.url)
                $request.AllowAutoRedirect = $false
                $request.UserAgent = 'mur-install'
                $request.Accept = 'application/octet-stream'
                $request.Headers.Add('Authorization', "Bearer $token")
                try {
                    $response = $request.GetResponse()
                } catch {
                    throw "mur install: could not start the download of ${name}: $($_.Exception.Message)"
                }
                # The answer is either the file itself (200) or a redirect to it (3xx).
                try {
                    if ([int]$response.StatusCode -eq 200) {
                        $file = [IO.File]::Create((Join-Path $tmp $name))
                        try { $response.GetResponseStream().CopyTo($file) } finally { $file.Close() }
                        return
                    }
                    $location = $response.Headers['Location']
                } catch {
                    throw "mur install: the download of $name failed: $($_.Exception.Message)"
                } finally {
                    $response.Close()
                }
                if (-not $location) { throw "mur install: GitHub did not say where to download $name from." }
                try {
                    Invoke-WebRequest -UseBasicParsing -Uri $location -OutFile (Join-Path $tmp $name)
                } catch {
                    throw "mur install: the download of $name failed: $($_.Exception.Message)"
                }
            }
        } else {
            $base = if ($tag) { "https://github.com/$repo/releases/download/$tag" } else { "https://github.com/$repo/releases/latest/download" }
            function Get-Asset($name) {
                try {
                    Invoke-WebRequest -UseBasicParsing -Uri "$base/$name" -OutFile (Join-Path $tmp $name)
                } catch {
                    throw "mur install: could not download $base/${name}: $($_.Exception.Message). Is the release published, and is $repo public? Otherwise set GITHUB_TOKEN."
                }
            }
        }

        Write-Host "Downloading $asset$(if ($tag) { " ($tag)" }) from $repo..."
        Get-Asset $asset
        Get-Asset 'SHA256SUMS'

        # SHA256SUMS lines read "<hash>  <name>" or, for binary files, "<hash> *<name>".
        $want = $null
        foreach ($line in Get-Content (Join-Path $tmp 'SHA256SUMS')) {
            if ($line -match '^([0-9a-fA-F]{64})\s+\*?(.+?)\s*$' -and $Matches[2] -eq $asset) { $want = $Matches[1].ToLower(); break }
        }
        if (-not $want) { throw "mur install: SHA256SUMS has no line for $asset." }
        $got = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $tmp $asset)).Hash.ToLower()
        if ($got -ne $want) { throw "mur install: the checksum of $asset does not match SHA256SUMS (wanted $want, got $got); nothing was installed." }
        Write-Host 'Checksum OK.'

        New-Item -ItemType Directory -Force -Path $dir | Out-Null
        # swap:start (internal/update/install_windows_test.go runs the lines from here to swap:end)
        #
        # The new file is renamed over mur.exe in one step, so an install stopped at any moment leaves
        # a whole mur.exe there, old or new (#959). Windows won't rename over a running exe (mur's
        # daemon keeps one running) unless the exe has a second name, so it is first hard-linked to
        # mur.exe.old-*, as the daemon's own updater does (#944). That name goes once nothing runs
        # it: now, or at the next install. The new file is copied beside mur.exe first, since a rename
        # is one step only within a volume.
        Get-ChildItem -Path $dir -Filter 'mur.exe.old-*' -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue
        Get-ChildItem -Path $dir -Filter 'mur.exe.new-*' -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue
        $suffix = [Guid]::NewGuid().ToString('N').Substring(0, 8)
        $new = "$exe.new-$suffix"
        Copy-Item -Path (Join-Path $tmp $asset) -Destination $new
        if (Test-Path $exe) {
            $old = "$exe.old-$suffix"
            # A volume that can't link (FAT) leaves $old unmade; the rename below then works only if
            # nothing runs mur.exe.
            try { New-Item -ItemType HardLink -Path $old -Target $exe | Out-Null } catch { }
        }
        $loaded = $false
        try {
            # MoveFileEx with MOVEFILE_REPLACE_EXISTING (1): the one-step rename. PowerShell 5.1 has no
            # other name for it.
            # A window that ran an earlier install has the type already, and adding it again under
            # another definition would fail.
            if (-not ('MurInstall.Win32' -as [type])) {
                Add-Type -Namespace MurInstall -Name Win32 -MemberDefinition '[DllImport("kernel32.dll", CharSet = CharSet.Unicode)] public static extern bool MoveFileEx(string from, string to, int flags);'
            }
            $loaded = $true
        } catch { }
        # The rename won't replace a read-only file, which the old way's move aside did.
        try { if ((Test-Path $exe) -and (Get-Item $exe).IsReadOnly) { (Get-Item $exe).IsReadOnly = $false } } catch { }
        $swapped = $false
        # A few tries: a scanner may hold the new file for a moment after it is written.
        for ($try = 0; $loaded -and -not $swapped -and $try -lt 5; $try++) {
            if ($try) { Start-Sleep -Milliseconds 300 }
            $swapped = [MurInstall.Win32]::MoveFileEx($new, $exe, 1)
        }
        if (-not $swapped) {
            $linked = $old -and (Test-Path $old)
            if ($loaded -and $linked) {
                # The rename was possible and didn't happen. The old way would only risk the mur.exe
                # that is there, so it stays.
                Remove-Item -Force -Path $new, $old -ErrorAction SilentlyContinue
                throw "mur install: could not put the new mur.exe in place at $exe; the one installed is unchanged."
            }
            # Where the rename can't be done (a running mur.exe on a volume without links, or a
            # PowerShell that may not load the call), the old way, which a stop between its steps can
            # leave without a mur.exe: the old file is moved aside, and the new one put in its place.
            if ((Test-Path $exe) -and -not $linked) { Move-Item -Force -Path $exe -Destination $old }
            try {
                Move-Item -Force -Path $new -Destination $exe
            } catch {
                # The new file couldn't be put in place: a scanner may still hold it. If that left
                # no mur.exe (it was moved aside above, or this move took it away before it failed),
                # the old one goes back under its name, so a failed install leaves the mur.exe that
                # was there (#1171).
                $why = "$_"
                if (-not (Test-Path $exe) -and $old -and (Test-Path $old)) {
                    try { Move-Item -Force -Path $old -Destination $exe } catch { }
                }
                Remove-Item -Force -Path $new -ErrorAction SilentlyContinue
                if (Test-Path $exe) {
                    if ($old) { Remove-Item -Force -Path $old -ErrorAction SilentlyContinue }
                    throw "mur install: could not put the new mur.exe in place at $exe ($why); the one installed is unchanged."
                }
                if (-not $old) {
                    throw "mur install: could not put mur.exe in place at $exe ($why); nothing was installed."
                }
                throw "mur install: could not put the new mur.exe in place at $exe ($why), and could not put the old one back. Rename $old to mur.exe."
            }
        }
        if ($old) { Remove-Item -Force -Path $old -ErrorAction SilentlyContinue }
        # swap:end
    } finally {
        Remove-Item -Recurse -Force -Path $tmp -ErrorAction SilentlyContinue
    }

    # Edit the user PATH in the registry. [Environment]::SetEnvironmentVariable would also work, but
    # it writes back the expanded value, turning an entry like %USERPROFILE%\go\bin into a fixed path.
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
    try {
        $names = [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames
        $current = [string]$key.GetValue('Path', '', $names)
        $kind = if ($null -ne $key.GetValue('Path', $null)) { $key.GetValueKind('Path') } else { [Microsoft.Win32.RegistryValueKind]::ExpandString }
        $present = $false
        foreach ($entry in ($current -split ';')) {
            if ($entry -and ([Environment]::ExpandEnvironmentVariables($entry).TrimEnd('\') -ieq $dir.TrimEnd('\'))) { $present = $true }
        }
        if (-not $present) {
            $key.SetValue('Path', ($(if ($current) { $current.TrimEnd(';') + ';' } else { '' }) + $dir), $kind)
            # Writing the registry doesn't tell running programs; setting any user variable does.
            [Environment]::SetEnvironmentVariable('MURMUR_INSTALL_REFRESH', '1', 'User')
            [Environment]::SetEnvironmentVariable('MURMUR_INSTALL_REFRESH', $null, 'User')
        }
    } finally {
        $key.Close()
    }
    $onSessionPath = @($env:Path -split ';' | Where-Object { $_.TrimEnd('\') -ieq $dir.TrimEnd('\') }).Count -gt 0
    if (-not $onSessionPath) { $env:Path = "$dir;$env:Path" }

    & $exe version
    if ($LASTEXITCODE -ne 0) { throw "mur install: installed $exe but it did not run; the file is at that path." }
    Write-Host "Installed: $exe"
    if ($present) {
        Write-Host "$dir is already on your user PATH."
    } else {
        Write-Host "Added $dir to your user PATH. Terminals opened from now on find mur; this window does too."
    }
}
