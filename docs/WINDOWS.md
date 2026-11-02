# Running on Windows

There is no native Windows build of the Studio. There are two ways to use it from a Windows computer: run the Linux build inside WSL2 (Windows Subsystem for Linux) on the same computer and open it in a Windows browser, or open, in a Windows browser, a Studio that runs on a Linux server or workstation. The second needs nothing installed on Windows; its setup is in [`DEPLOY.md`](DEPLOY.md), and the address of that Studio goes in the browser. The rest of this page is the first way.

## Requirements

- Windows 10 version 2004 or later (build 19041 or later), or Windows 11. `winver` shows the version.
- Virtualization turned on in the computer's firmware. WSL2 runs Linux in a lightweight virtual machine, and Task Manager, Performance, CPU shows whether virtualization is enabled.
- Administrator rights once, to install WSL. On a computer managed by an organization, installing WSL is often an IT decision, and group policy can block it (WSL then reports error 0x800704ec). Ask the administrator before starting. After WSL is installed, the Studio itself needs no administrator rights on Windows.
- Memory: 16 GB or more on the Windows computer. WSL2 takes half of the computer's memory by default. The first build of GUFI, the index engine, was run with 8 GB and 8 processor cores and peaked near 6 GB in total, and indexing the 7,500-file tree below stayed under that. The Studio's server uses about 0.2 GB when idle. A computer with less memory may work with fewer processors given to WSL (see [Get the Studio and build it](#get-the-studio-and-build-it)); that was not tried.
- Disk: about 3 GB in the Linux file system for the install (the clone with its packages about 1 GB, GUFI's source and build about 1 GB, Node 0.25 GB, GUFI itself 0.17 GB, and about 0.6 GB of Ubuntu packages), of which `~/gufi-src` can be deleted afterwards. The index adds to that: a test tree of 7,500 files, nearly all of them without text, made an index of 52 MB, so plan on roughly 7 KB per file for names and metadata, plus the text and embeddings of the documents. The index is kept in the Linux file system, by default under the clone, and the WSL virtual disk grows as it fills.
- A browser on Windows, Microsoft Edge or Google Chrome.

## Install WSL and Ubuntu

In PowerShell opened as administrator:

```
wsl --install -d Ubuntu-24.04
```

Restart the computer when asked, then open **Ubuntu 24.04** from the Start menu. The first start takes a few minutes and asks for a Linux user name and password. The password is what `sudo` asks for below. `wsl -l -v` in PowerShell lists the installed distributions and shows VERSION 2 for this one.

The rest of this page runs in that Ubuntu window, not in PowerShell.

## Install what the build needs

```
sudo apt update
sudo apt install -y build-essential cmake pkg-config git curl ca-certificates xz-utils \
  python3 bsdextrautils attr libattr1-dev libsqlite3-dev zlib1g-dev libpcre2-dev \
  autoconf automake libtool tesseract-ocr poppler-utils
```

`build-essential`, `cmake`, `bsdextrautils` (which provides `column`, a program GUFI's build looks for) and the `-dev` packages build GUFI, the index engine. `attr` provides `getfattr` and `setfattr`, which the Studio uses for labels. `tesseract-ocr` reads text in images and on scanned PDF pages, and `poppler-utils` reads PDFs the main converter cannot. `python3` runs the indexer scripts and needs to be 3.10 or later, which Ubuntu 24.04 satisfies.

Node 26 and pnpm are not in Ubuntu's packages. This puts Node in `~/.local/node` and needs no `sudo` (on a computer with an ARM processor, use `arm64` in place of `x64` in the file name):

```
cd ~
curl -fsSL -o node.tar.xz https://nodejs.org/dist/v26.10.0/node-v26.10.0-linux-x64.tar.xz
echo "ca70e9e349de048b9522abb3adc05b3bd6f43c5ffd3ec57916c7da292f59f022  node.tar.xz" | sha256sum -c -
mkdir -p ~/.local/node && tar -xJf node.tar.xz -C ~/.local/node --strip-components=1 && rm node.tar.xz
echo 'export PATH="$HOME/.local/node/bin:$PATH"' >> ~/.bashrc
export PATH="$HOME/.local/node/bin:$PATH"
npm install -g pnpm@11
node --version && pnpm --version
```

The checksum line is for the x64 file. For ARM, take the checksum of the `arm64` file from `https://nodejs.org/dist/v26.10.0/SHASUMS256.txt`.

## Get the Studio and build it

Keep the clone in the Linux file system, under `~`, not under `/mnt/c`. Microsoft recommends this for anything run from Linux, since files on the Windows side are much slower to read and write from WSL.

```
cd ~
git clone https://github.com/parallelworks/activate-studio
cd activate-studio
pnpm install
pnpm build
CFLAGS=-U_FORTIFY_SOURCE ~/activate-studio/indexer/setup_gufi.sh
```

`indexer/setup_gufi.sh` builds GUFI from source into `/opt/gufi` and fetches the embedding model. It is the long step, about 7 minutes on 8 processor cores, and longer with fewer. It asks for the `sudo` password, to install into `/opt`. Run it by its full path as above, since it changes directory while it runs and a relative path stops it before the embedding model is fetched. The `CFLAGS` setting turns off a compiler check that Ubuntu 24.04 enables by default, which otherwise stops the build at `spread_tree.c`; it has to be set on the first run, since the build folder remembers it, and after a failed first run `rm -rf ~/gufi-src/build` clears the old setting. `GUFI_AI=0` in front of the command builds without the dependencies that search by meaning needs, which is a quick build; names and full-text search work, and search by meaning does not.

*If the build runs out of memory:* the build starts one compile job per processor, so a computer with little memory can fail with a message that a process was killed. Lower the processors WSL uses, in `%UserProfile%\.wslconfig` on Windows (`[wsl2]`, then `processors=4`; `memory=` raises the memory limit), and run `wsl --shutdown` in PowerShell before opening Ubuntu again.

## Choose a folder and index it

The Studio works over one main folder, set with `KB_ROOT`. Without it, it opens a small sample folder inside the clone.

A Windows folder is the same folder under `/mnt/c`. `wslpath` converts a Windows path to the Linux one, and the quotes matter when a name has spaces:

```
wslpath 'C:\Users\me\Documents'
# /mnt/c/Users/me/Documents
export KB_ROOT=/mnt/c/Users/me/Documents
```

A folder inside the Linux file system, such as `~/documents`, is faster to index and search (see [Your files](#your-files)). Then build the index:

```
cd ~/activate-studio
indexer/reindex.sh
```

It makes every file findable by name first, then reads the text, then computes the embeddings, and prints how long it took. A model for chat is optional and set later.

## Start it

```
cd ~/activate-studio
pnpm start
```

Open `http://localhost:4080` in Edge or Chrome on Windows. WSL2 forwards `localhost` on Windows to the Linux side, so the page opens as if the server were on Windows. The first visit runs a short setup: a name, the theme, whether to turn on AI features, and the folder. The folder in setup is the one `KB_ROOT` names. To change it later, set `KB_ROOT` again and restart, then run `indexer/reindex.sh` for the new folder (or use Settings, Libraries, Build index).

The Studio runs for as long as `pnpm start` does, and `Ctrl+C` stops it. WSL shuts its virtual machine down when nothing is using it, so closing the Ubuntu window can stop the server too. Leave the window open while the Studio is in use.

To start it again after a restart or a closed window, open Ubuntu and run the same commands. A small script keeps the settings in one place, so that `KB_ROOT` and a model endpoint do not have to be typed again:

```
cat > ~/start-studio.sh <<'EOF'
#!/usr/bin/env bash
export PATH="$HOME/.local/node/bin:$PATH"
set -a
. "$HOME/studio.env"
set +a
cd "$HOME/activate-studio" && exec pnpm start
EOF
chmod +x ~/start-studio.sh
printf "KB_ROOT='/mnt/c/Users/me/Documents'\n" > ~/studio.env
~/start-studio.sh
```

From PowerShell, `wsl -d Ubuntu-24.04 -e bash -c "exec ~/start-studio.sh"` runs it without opening the Ubuntu window first.

In Edge or Chrome, **Install app** in the Studio's navigation (or the install icon in the address bar) installs it as an app with its own window and Start menu entry. It is the same page: the app opens `localhost:4080`, so the server has to be running first.

The server listens on all of the Linux side's addresses by default (`HOST=0.0.0.0`). With WSL's default networking, only Windows reaches it. With `networkingMode=mirrored` in `.wslconfig`, which is a Windows 11 setting, other computers on the network can reach the port too, so set `HOST=127.0.0.1` in `studio.env` there.

## Your files

*Where the folder is.* A drive on Windows is mounted in WSL under `/mnt`, so `C:\Users\me\Documents` is `/mnt/c/Users/me/Documents` and `D:` is `/mnt/d`. The Studio works with the Linux paths. Windows paths shown in the Studio are WSL paths, `/mnt/c/...` for a file on `C:`, and a path copied from the Studio does not work in Windows File Explorer without changing it back (`wslpath -w /mnt/c/Users/me/Documents` prints the Windows form).

*Speed.* Reading files under `/mnt/c` goes through a file-system bridge to Windows, and is slower than the Linux file system, most of all for folders with many small files. A folder on `C:` still works, and the first index takes longer than the same folder in `~`. If the folder is small enough, copy it into the Linux file system and index the copy. If it has to stay where it is, keep the clone, the index and the data in `~`, as above, so that only the reading of the documents crosses the bridge. New and edited files are picked up by the background sync, every five minutes by default (`SWEEP_INTERVAL_SEC`), or at once by Update index.

*Network shares.* A share is reached the way Windows reaches it. Connect to it in Windows first (open it in File Explorer, or `net use`), because WSL has no way to give a share credentials. Then mount it in Ubuntu with a UNC path, in single quotes, read-only:

```
sudo mkdir -p /mnt/share
sudo mount -t drvfs '\\server\share' /mnt/share -o ro
```

A mapped drive letter mounts the same way (`sudo mount -t drvfs Z: /mnt/z`). Set `KB_ROOT=/mnt/share`, or add the share as a library in Settings, Libraries, which indexes it without changing it. Linux sees every file on a network share with full permissions (0777) and no symbolic links, whatever the account may read. Mounts made by hand are gone after WSL restarts. An entry in `/etc/fstab` (`\\server\share /mnt/share drvfs ro 0 0`) mounts it again at each start. Reading a share is slower still than `/mnt/c`, and a share of many large files may be better indexed on the machine that holds it, then connected here (see [`LIBRARIES.md`](LIBRARIES.md)).

*Labels.* In the main folder, the Studio stores a label on the file as an extended attribute (`user.studio.tags`), written with `setfattr`. Microsoft's documentation does not describe `user.` extended attributes on Windows drives, so whether `/mnt/c` accepts them is not something to assume. Check it on the folder in use:

```
f='/mnt/c/Users/me/Documents/a-file-in-the-folder.txt'
setfattr -n user.test -v 1 "$f" && getfattr -n user.test "$f"
setfattr -x user.test "$f"
```

If `setfattr` answers "Operation not supported", the drive does not take extended attributes. The Studio tries the attribute first, and when the write fails it keeps the label itself, in `tags-overlay.json` in its index folder (`~/activate-studio/index`), matched to the file by its inode. Labels look and work the same in the interface. They are kept in that file, not on the files, so they do not go with a copy of a file, and a file that is deleted and created again is a new file with no label. Back up that file if the labels matter. If `setfattr` succeeds, the labels are on the files, as they are on Linux, and so they are for files in the Linux file system, where the file system takes them.

In a library other than the main folder, including a folder opened read-only and a share, the Studio always keeps the labels itself and never writes to the files.

## Models

Chat, agents and image descriptions need a model, and the rest of the Studio does not. Settings, Model access, or the first setup, takes any OpenAI-compatible endpoint, as on any other computer: its base URL, which usually ends in `/v1`, and its key. The same can be set in `studio.env` as `OPENAI_BASE_URL` and `OPENAI_API_KEY`. The endpoint has to serve `/models` and streaming `/chat/completions` with tool calling; Settings, Model access, **Test** says whether the model calls tools. [`HELP.md`](HELP.md) covers the options and [`DEPLOY.md`](DEPLOY.md) the environment variables.

A model server that runs on Windows, such as Ollama for Windows, is reached from WSL at the Windows host's address, not `localhost`. With the default networking, the host address is the default route's:

```
ip route show | awk '/default/ {print $3}'
```

Use that address in the endpoint, as `http://<host address>:11434/v1`. The model server has to listen on all addresses, not only on `127.0.0.1` (Ollama for Windows does this with the environment variable `OLLAMA_HOST=0.0.0.0`), and Windows Firewall may need to allow the connection. The address can change when WSL restarts. With `networkingMode=mirrored`, `127.0.0.1` reaches the Windows side directly. A model server can also run inside Ubuntu, such as Ollama, where the endpoint is `http://localhost:11434/v1`; it then uses the WSL virtual machine's memory, which `.wslconfig` limits.

## Updating and removing

To update, in Ubuntu:

```
cd ~/activate-studio
git pull
pnpm install
pnpm build
```

Then stop the running server with `Ctrl+C` and start it again. The index, the settings and the labels are kept. Settings, General, Updates, and the notice at the top of the page, show when a newer version is out and give these commands.

To remove the Studio, delete the clone, which holds the index, the settings and the conversations, and the GUFI install:

```
rm -rf ~/activate-studio ~/gufi-src ~/start-studio.sh ~/studio.env ~/.local/node ~/.local/share/pnpm
sudo rm -rf /opt/gufi
```

The line that adds Node to `PATH` in `~/.bashrc` and the Ubuntu packages stay until removed by hand. An index kept elsewhere by Settings, General, Indexing is in the folder it names. Your documents are never removed. To remove the whole Ubuntu installation instead, which deletes everything in it, run in PowerShell:

```
wsl --unregister Ubuntu-24.04
```

That removes every file in that Ubuntu, not only the Studio. If the Studio was installed as an app from the browser, remove it from the browser's Apps page or from Windows Settings, Apps.

## Known limits

- There is no native window and no folder chooser. The folder is typed, as `KB_ROOT` or in setup, and the Studio runs in a browser.
- The Windows paths a person sees in the Studio are WSL paths, `/mnt/c/...`.
- Indexing and opening files through `/mnt/c` is slower than in the Linux file system, and changes made in Windows are found by the next sync, not at once.
- Labels on `/mnt/c` and on shares may be kept by the Studio instead of on the file.
- The Studio has to be started again after a restart.
- GUFI is built from source on the computer, which needs a network connection and time.
