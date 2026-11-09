# Running on Windows

There is no native Windows build of the Studio. On a Windows computer it runs as the Linux app inside WSL2 (Windows Subsystem for Linux): in its own window, which WSLg puts on the Windows desktop, or in the background with a Windows browser open on it. Its index and settings live in the Linux file system, and Windows folders are indexed through `/mnt/c`. A Windows browser can also open a Studio that runs on a Linux server or workstation, which needs nothing installed on Windows; that setup is in [`DEPLOY.md`](DEPLOY.md).

*Status:* this page has been tested only in Ubuntu 24.04 containers that stand in for WSL (the install, browser mode through Windows' URL opener, Windows paths, and labels on a file system that refuses extended attributes). It has not yet been run on Windows 11. The items still to confirm there are marked *(not yet confirmed on Windows)*.

## Requirements

- Windows 11, with WSL2 and WSLg, which shows Linux windows on the Windows desktop. This is the tested target. Windows 10 22H2 with WSL from the Microsoft Store may work, but is untested.
- Virtualization turned on in the computer's firmware (Task Manager, Performance, CPU shows it).
- Administrator rights once, to install WSL. On a computer managed by an organization, group policy can block WSL (error 0x800704ec); ask the administrator first. The Studio itself needs no administrator rights.
- 16 GB of memory or more. WSL2 takes half of the computer's memory by default.
- About 700 MB in the Linux file system for the program, plus the index.

## Install WSL and Ubuntu

In PowerShell opened as administrator:

```
wsl --install -d Ubuntu-24.04
```

Restart when asked, then open **Ubuntu 24.04** from the Start menu. The first start asks for a Linux user name and password; the password is what `sudo` asks for below. The rest of this page runs in that Ubuntu window.

## Install the Studio

Download `activate-studio-linux-x86_64.tar.gz` in Windows, then in Ubuntu unpack it into the Linux file system and run the installer (replace `me` with the Windows user name):

```
tar -xzf /mnt/c/Users/me/Downloads/activate-studio-linux-x86_64.tar.gz -C ~ && ~/activate-studio/install.sh --move
```

This is the Linux app's per-user install ([`LINUX-APP.md`](LINUX-APP.md)): the program goes to `~/.local/lib/activate-studio`, the command to `~/.local/bin/activate-studio`, and a menu entry to `~/.local/share/applications`. It needs no `sudo` and no systemd. Keep the program in the Linux file system, never under `/mnt/c`, where the updater's moves and permissions do not work as on Linux.

For the Studio's own window, install WebKitGTK once:

```
sudo apt install -y libwebkit2gtk-4.1-0
```

Without it the Studio opens in a Windows browser. Optionally, `sudo apt install -y wslu` adds `wslview`, which the Studio uses first to open links and folders in Windows; without it the Studio uses Windows' own `rundll32.exe` and `explorer.exe`.

## Start it

```
activate-studio
```

With WebKitGTK installed, this opens the Studio's window on the Windows desktop. WSLg also lists Linux menu entries in the Windows Start menu, under the distribution's name, so **ACTIVATE Studio** may be started from there *(not yet confirmed on Windows)*. Where WSLg has no GPU device, the window turns off WebKit's DMA-BUF renderer, which otherwise can draw a blank window; set `WEBKIT_DISABLE_DMABUF_RENDERER=0` to try it anyway. If the window stays blank, use the browser mode.

```
activate-studio --browser
```

The browser mode runs the server in the background and opens the default Windows browser on it, through `wslview` or `rundll32.exe`. The server listens on `127.0.0.1` inside WSL, and WSL forwards `localhost` from Windows, so the address it opens (such as `http://127.0.0.1:4180/`) works in Edge or Chrome on Windows. With `networkingMode=mirrored` in `.wslconfig` it is the same address. The server keeps running after the browser closes; `activate-studio --stop` stops it. In Edge or Chrome, **Install app** gives it a window and a Start menu entry of its own; the server has to be running first.

WSL can stop Ubuntu, and the Studio with it, a short time after its last window closes. Keep an Ubuntu window open while the Studio is in use, and start it again after a restart *(how long a background server survives is not yet confirmed on Windows)*.

## Where to keep libraries

The Studio's data (index, settings, logs and labels kept by the Studio) is in `~/.local/share/activate-studio-data`, in the Linux file system. The main folder is `~/Documents/ACTIVATE Studio` in Linux until another is chosen.

A Windows folder can be the main folder or a library. In setup, Settings, Indexing, Libraries, and every other place that takes a folder, a Windows path can be typed as it is: `C:\Users\me\Documents` is read as `/mnt/c/Users/me/Documents`, and `\\wsl.localhost\Ubuntu-24.04\home\me\notes` as `/home/me/notes`. A drive mounted somewhere else by `[automount] root` in `/etc/wsl.conf` is followed. The window's folder chooser is a Linux one, and Windows drives are under `/mnt/c`, `/mnt/d` and so on. The Studio shows Linux paths; `wslpath -w` turns one back into the Windows form.

A folder in the Linux file system, such as `~/documents`, is indexed and searched much faster than the same folder under `/mnt/c`. Where the files can move, keep them in Linux; where they have to stay in Windows, index them in place.

## Windows folders under /mnt/c

*Speed.* Every file read and every file's details under `/mnt/c` cross a bridge to Windows, which is slow for folders of many small files. The first index takes longer than in Linux.

*Changes made in Windows.* The Studio has no file watcher on any system. The background sync finds changes by reading the time of every folder and file, every 300 seconds by default, and changes made in Windows are found the same way. On a large folder under `/mnt/c` that walk is slow, so set Settings, General, Indexing, **Background sync interval** to 1800 or 3600 seconds, and press **Update index** after large changes.

*Threads.* Settings, Indexing, Building the index sets the threads each indexing phase uses, one per processor core by default. Under `/mnt/c` the time goes mostly to waiting on Windows, and up to twice the number of cores may index faster *(a suggestion, not yet measured on Windows)*.

*Labels.* In the main folder, the Studio stores a label on the file as an extended attribute (`user.studio.tags`). Windows drives in WSL do not take `user.` extended attributes, so the Studio keeps those labels itself, in `tags-overlay.json` in its index folder, matched to each file by its file number and checked by its creation time. Labels look and work the same. They do not travel with a copy of the file, and a file deleted and created again is a new file with no label; back up `tags-overlay.json` if labels matter. Libraries other than the main folder always keep labels in the Studio.

*Names.* Windows folders ignore case, so `Report.pdf` and `report.pdf` are one file. Renaming a file to the same name in another case works as a rename.

*Permissions.* Without the `metadata` mount option, WSL shows every file on a Windows drive as readable and writable by everyone (0777), whatever Windows allows. A folder Windows refuses is reported as one the Studio could not read.

*Network shares.* Connect to the share in Windows first (File Explorer, or `net use`), then mount it read-only in Ubuntu and add it as a library:

```
sudo mkdir -p /mnt/share
sudo mount -t drvfs '\\server\share' /mnt/share -o ro
```

A mount made this way is gone after WSL restarts; a line in `/etc/fstab` (`\\server\share /mnt/share drvfs ro 0 0`) mounts it at each start.

## Opening files in Windows

Where the Studio opens something outside itself (a link, or a folder from the window), it opens it in Windows: through `wslview` when installed, otherwise links through `rundll32.exe url.dll,FileProtocolHandler` and folders in File Explorer.

## Models

Settings, Model access takes any OpenAI-compatible endpoint, as on Linux. A model server on Windows, such as Ollama for Windows, is reached from WSL at the Windows host's address, which `ip route show | awk '/default/ {print $3}'` prints, as `http://<host address>:11434/v1`. That server has to listen on all addresses (`OLLAMA_HOST=0.0.0.0`), and Windows Firewall may need to allow it. With `networkingMode=mirrored`, `127.0.0.1` reaches Windows directly.

## Updating and removing

The Studio updates itself as on Linux (Settings, General, Updates), inside `~/.local/lib/activate-studio`. Your data is kept.

```
activate-studio --uninstall           # removes the program, keeps the data
activate-studio --uninstall --purge   # also removes the index and settings
```

Your documents are never removed. To remove the whole Ubuntu installation, and every file in it, run `wsl --unregister Ubuntu-24.04` in PowerShell.

## Known limits

- Changes made in Windows are found by the next background sync, not at once.
- Labels on Windows drives are kept by the Studio, not on the files.
- Indexing through `/mnt/c` is slower than in the Linux file system.
- The Studio has to be started again after Windows or WSL restarts.
