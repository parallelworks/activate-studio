# ACTIVATE Studio documentation

These are the same pages the app shows under Help, and the guides for running and building it. The user guide calls the app Studio; a deployment with its own product name shows that name in the app.

## Using it

| Document | Covers |
|---|---|
| [User guide](HELP.md) | every page of the app, as the in-app Help shows it; the assistant reads it too |
| [Reference](REFERENCE.md) | the details behind the user guide, part by part |
| [Libraries](LIBRARIES.md) | several indexes at once, building an index of a folder here or on a remote system over ssh or pw ssh, library lists to export and import, the Library's Details card, and which parts of the app appear |
| [Transfers](TRANSFERS.md) | copying, moving and syncing files between libraries, this computer, clusters, other computers over ssh or SFTP and CONDUIT: locations, plans, movers, history and settings |
| [Workflows](WORKFLOWS.md) | the Workflows tab |
| [Agents](AGENTS.md) | delegated agents |

## Installing and updating

| Document | Covers |
|---|---|
| [macOS](MACOS.md) | the macOS app, and running from a clone on macOS |
| [Linux](LINUX-APP.md) | the Linux app: per-user install, `.deb` and `.rpm`, requirements, update and uninstall, the install layout |
| [Windows](WINDOWS.md) | the Linux app on Windows through WSL2: install, window or browser, Windows folders under `/mnt/c`, labels and sync there |
| [Updates](UPDATES.md) | update channels (Stable and Preview), installing another version, the state snapshot and format |

## Running it for others

| Document | Covers |
|---|---|
| [Deploying](DEPLOY.md) | running it on ACTIVATE, in a container, or as a plain server, and connecting models |
| [Several people](MULTI-USER.md) | several people on one deployment |
| [Access control](ACCESS-CONTROL.md) | who can find what: GUFI and the file system's permissions, deploying for user and group access, and an administrator's index |
| [Customization](CUSTOMIZATION.md) | settings, branding, identity, the help text, and the assistant's tools |

## How it is built

| Document | Covers |
|---|---|
| [Architecture](ARCHITECTURE.md) | how the index, retrieval, and server work |
| [Go server](GO-PORT.md) | the port of the server to Go: its layout, the plan and state of the port, and where it differs from the Node server |
| [GUFI for macOS](GUFI-MACOS.md) | GUFI built for macOS 14 and later: the published archive, building and checking it |

What changed in each version is in the [change log](../CHANGELOG.md).
