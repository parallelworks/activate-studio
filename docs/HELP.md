### Quick start

Files collect in many folders, drives and systems. Studio indexes your folders with GUFI, so you can find a file without remembering where it was saved. **Search** finds files by name, by the words inside them (PDFs and office documents, and text in images and scanned pages where OCR is available, as it is in the macOS app), and by meaning. **Query** answers questions about the files themselves, such as the largest, the newest, or the totals by type.<!-- ai --> **Assistant** answers questions from the files and links each source it used.<!-- /ai -->

Files stay where they are. Studio keeps its index in a folder of its own, reads each file in place, and follows the file system's permissions. Indexing, search, and query run where Studio runs and need no network connection.<!-- ai --> Assistant sends text only to the models you connect, which can be on the same computer.<!-- /ai --> The same index works for a folder on a laptop and for a cluster's file systems.

1. **Choose what to search.** On your own computer, setup asks which folder is **My library**. On a Studio you sign in to, the Shared library is already there; see the first day below.
2. **Search.** Type a word or phrase in **Search**, then click a result to open the file at the match.
3. <!-- ai -->**Ask.** Open **Assistant** and ask in plain language. The answer links the files it used.<!-- /ai -->
4. **Query.** Open **Query** and pick a preset, or build one, to list files by size, age, type, owner, or label.

### Your first day on a Studio you sign in to

Several people use this Studio, each signed in as themselves. The **Shared library** is the one everyone searches, and only its administrators change it. **My library** is your own folder on a cluster, read and written as you.

1. **Sign in.** Open the Studio and sign in with your ACTIVATE account when it asks. A card at the foot of the page welcomes you; **Got it** closes it. This tells the Studio who you are.
2. **Let the Studio act as you.** In **Settings**, **Accounts**, open **ACTIVATE**, click **Sign in with ACTIVATE**, and approve the code it shows on the platform. My library, buckets, the Code page, workflows and desktops run as you with that sign-in. It is yours alone and lasts until you sign out. Without it, browsing and searching the Shared library still work. Some Studios ask everyone to do this first; **Settings**, **Accounts** then says so<!-- ai -->, as a new chat does<!-- /ai -->.
3. **Add My library.** In **Library**, click the arrow beside **+ Add**, then **Add a library…** and choose the cluster this Studio runs on. The folder starts at `studio` in your home folder; keep it, then click **Index** and **Create it and build**.<!-- ai --> The chats you save and the assistant's files go there.<!-- /ai -->
4. **Add another folder or a bucket.** Use **Add a library…** again: choose a cluster and a folder, then **Index**; or choose one of your ACTIVATE buckets from the same list, then **List and index**. It is searched with the rest once it is built.
5. <!-- ai -->**Start a chat.** Open **Assistant**, click **New chat** and ask. At the top right, choose what answers: **pw code** runs a session of yours that works in files and runs the commands you approve; **Studio** answers from the libraries with the model under the message box.<!-- /ai -->
6. **Share a folder.** In My library, right-click a folder, then **Share…**. Choose a person or a group, choose **Read** or **Read and write**, and click **Share**. They get a notice of it with **Add to my libraries**, and find it in **Add a library…** too, once they choose the same cluster there.
7. **Send a link.** Right-click a file or folder, then **Copy link**. Whoever opens it signs in if they need to, then sees the file only if they already can open it. A link gives no access of its own, so share the folder first.
8. **Read Notices.** The sidebar's **Notices** lists what needs you, such as an approval<!-- ai --> in a chat<!-- /ai --> or a folder somebody shared with you, and what failed, such as a transfer or an index.

### On your own computer

A Studio of one needs no account. Setup chooses the folder that is **My library**, and you add, rename, move and delete its files in the **Library** page. <!-- ai -->For Assistant, connect a model under **Settings**, **Accounts**: an endpoint of your own, or a model on this computer. The Studio answers with it and its own tools. <!-- /ai -->Signing in with ACTIVATE is optional and adds pw code, workflows, desktops, ACTIVATE systems and their models.

Setup can run again at any time, starting from what is set now: **Run setup again** in **Settings**, **General**, or **Run Setup…** in the macOS app's menu.

- **macOS:** the app needs no administrator rights. Opened from its disk image, it offers to install itself in Applications in your home folder (`~/Applications`), where it updates itself. An administrator can put it in `/Applications` instead; that copy updates itself only for users who can write there. On a Mac that does not allow the app's background item (a LaunchAgent), the app starts its server when it opens, instead of at login.
- **Linux:** the app installs for one user with no administrator rights (`install.sh` from the unpacked archive), or for everyone with a `.deb` or `.rpm` package. It opens in its own window where WebKitGTK is installed, and in the default browser where it is not; then `activate-studio --stop` stops it. `activate-studio --uninstall` removes a per-user install and never your documents. See `docs/LINUX-APP.md`.
- **Windows:** the Linux app runs inside WSL2 (Windows Subsystem for Linux), in its own window on the Windows desktop, or with `activate-studio --browser` in a Windows browser. Windows folders are under `/mnt/c`, and a Windows path such as `C:\Users\me\Documents` can be typed wherever a folder is asked for. Labels on files in Windows folders are kept by Studio, and changes made in Windows are found by the background sync. See `docs/WINDOWS.md`.

# Basics

## Assistant

Ask in plain language. The assistant searches your libraries, reads the files that matter, and links each source; a link opens the file in the Library, in the library it came from. A file a reply only names, such as `reports/plan.docx` or its full path, opens the same way when it is in one of your libraries. Conversations are kept in the Studio. A chat goes into a library only when you save it, and is then searchable like any other file.

### Start a chat

**New chat** opens an empty one; the starters under the box are examples to click.

- **+**, then **Add files**, attaches files or images. They are kept in the main library under `uploads/chat` and indexed. Where the Shared library is read only for you they are refused: put the file in My library and name it with **@** instead. **Attachments**, in the chat list, lists what was attached.
- **@** points at a library file. The list starts at the top folder of the chat's library and narrows as you type; choosing a file writes `@[path]`, and the assistant gets its text with your question.
- **+**, then **Tools**, switches kinds of tools on or off: the knowledge base, platform workflows, platform changes, agents, editing the library, transfers, extensions, and each attached MCP server. A dot on **+** means something is off. The choice is kept in this browser. Without ACTIVATE the platform switches are not shown.
- **Scope**, at the top right, chooses which libraries the chat searches, and limits answers to the labels you choose. It starts at **All libraries**; at least one stays ticked.
- **Persona**, at the top right, gives the conversation a standing way of working; see **Agents**.
- **Effort**, beside the model, sets how long the model reasons before it answers. **Default** leaves it to the model; **Low**, **Medium**, **High**, **Extra high** and **Max** ask for less or more. Each conversation keeps its own, and a new chat starts with the last one you chose. A model that takes no effort setting answers as it always does. In a chat pw code answers it is pw code's own slider, from **Low** to **Max**: until you choose, the session starts at pw code's own default, **High** unless pw code on that machine is set otherwise.

### Choose what answers: pw code or the Studio

The button at the top right, **pw code** or **Studio**, names what answers the chat and changes it. Without an ACTIVATE sign-in the Studio always answers and there is no button.

- **Studio** answers from the libraries with the Studio's own tools and the model under the message box. It needs no ACTIVATE account, only a model (see **Accounts**).
- **pw code** is the ACTIVATE agent harness. It answers in a pw code session of yours that searches and reads the libraries with the Studio's tools, works in files, and runs the commands you approve. It is the default where the Studio runs under ACTIVATE and pw code can be reached. On a Studio several people share it runs as you, once you sign in with ACTIVATE, and works in your own space.

A conversation keeps what answered it last, in any browser, until you switch it. A new chat starts with your last choice, marked **default** in the menu.

**About switching.** Switching keeps the conversation: pw code is given the turns it did not see, and the Studio reads them all. In a chat both have answered, each reply names which one gave it. You cannot switch while a reply is being written.

**When pw code cannot answer.** If you are no longer signed in with ACTIVATE, the Studio answers, with one line under the reply saying why and linking **Settings**, **Accounts**. The chat stays with the Studio until you sign in and switch back. If pw code cannot reach the Studio's tools, the Studio answers that message and is the default for the next ten minutes. If pw code is down or failing, the chat says so and does not answer another way.

### A pw code chat

- **Where it runs** shows over the message box: the machine and the folder. They start at the Studio's own computer or cluster, named with **(this Studio)**, for example **my-cluster (this Studio)**, and listed first, where pw code runs as you; and your library's folder, or `~/studio-chat`. Choose others before the first message; after it they stay.
- **From the Library:** right-click a folder or file and choose **New pw code chat here** (see **Library**). The machine and folder are set already.
- **Another folder:** the list shows the folders your own account can read. The session has the Studio's tools as always, and the Studio writes nothing of its own into a folder you chose.
- **Another machine:** one of yours that is connected to ACTIVATE and takes new sessions from it. A mark beside it says whether pw code there has the Studio's tools, and **Add the Studio's tools to cluster-a** (the machine's name), ticked on your first message, adds them where it can. Its models and its widest permission mode are that machine's own. A persona does not go to another machine.
- **Model:** pick one under the message box. A new chat offers pw code's models, starting with the one set under **Settings**, **External access**, **pw code model**, or where none is set the one you chose last. If it says "Choose a model to start", choose one with **Select a model** in the message box; its **Choose a model** button opens that list. If the model pw code is set to use is not enabled for you, the chat moves to one that is and says so.
- **Approvals:** when pw code asks to run a command, a bar over the message box shows it with **Allow** and **Deny**. A plan shows with **Approve** and **Reject**; what you type under it goes back with **Reject**. You can also answer in PW Code on the web, and the bar leaves when either answers. Escape interrupts the turn. A turn runs on when the page reloads.
- **Open this chat in PW Code on the web**, in the menu at the top right, opens the session in ACTIVATE to follow or continue it there. **Open in Code**, in that menu, in the chat's own menu and beside the button where there is room, opens it on the Code page, where the Studio knows the session's machine.
- **The Studio's tools:** pw code searches with the index, reads files, writes reports and other files into the library, applies labels, runs workflows, and starts delegated work. Tools that only read, and delegating, run without asking; those that write ask first, and kinds you switched off under **+**, **Tools** stay off. The menu says whether the session has them. Deletes, cluster changes and transfers stay with the Studio.
- **Instructions:** in a folder of the Studio's, such as My library, the Studio keeps a section in that folder's `AGENTS.md` that tells pw code to search with the index first. Your own text in the file stays.

### Type / for commands

**/** lists what the chat can run, and narrows as you type.

- With the Studio answering: skills, tools, personas, and `/help`, which lists them all.
- With pw code: pw code's own commands, and the skills, custom commands and prompts of the session's folder. In a new chat they are those of the machine and folder chosen over the box.
- Some pw code commands are not sent from here: those that work only in a terminal, and those that manage the session (rename, fork, archive, resume, remote control), which the Code page's menu has. A line pw code does not know comes back to the box with the reason.

### The chat list

- Each chat shows its state: a spinning ring while a reply is written or its session works, an amber dot with **Waiting on you**, a blue dot with **New reply** or **New note**, a green dot while its pw code session is idle, and a gray dot when that session has ended. A pw code chat names the machine its session is on, with the machine's icon. Chats that are working or waiting are first, under **Active**. **Assistant** in the menu counts the chats that need you.
- On a Studio people sign in to, your chats are yours alone unless an administrator turns sharing on. Where it is on, **My chats** and **All chats** show over the list, and **All chats** lists everyone's, with a mark, **Not your chat**, on other people's. Others open a chat read only: its message box says **You can read this chat but not add to it**, and its menu has only **Download**. Only its owner sends to it, renames or deletes it. They read yours too, with what its answers quote from your own libraries, except a chat that continues a pw code session on another of your machines.
- Where signing in is optional, you sign in before your first chat: a notice at the top of the chat says **Sign in to chat**, with **Sign in**, and what you wrote is still in the box after.
- **Search conversations** (⌘/Ctrl+Shift+F) finds chats. **Select chats**, above New chat, deletes several.
- A chat's menu has **Rename**, **Download** (a Markdown file; a notice says where it went) and **Delete**.
- **Save**, at the top right of an open chat, writes a copy to `chat-sessions/` in My library, where only you read it, or in the main library. It says where the copy goes before it writes, saving again replaces the copy, and deleting the chat leaves it. You can also ask the assistant to save or remember a chat.
- The button at the foot of the list folds it to a rail of marks. The arrow keys, Home and End move between them. On a phone the list opens over the page.
- **Model access**, at the foot of the list, opens the settings that say where models come from.

### Ask for files and reports

- **Write a file.** Ask for a note, a script, a PDF or Word report, or a table as an Excel workbook. It writes the file, indexes it and links it, and replaces a file that exists only when you say so. A large file is written in parts.
- **Reports:** ask for a cover page, landscape, a font, text size, margins, line spacing, page breaks, or a line at the top or bottom of every page. Images from the library are placed where the report names them. Pages are numbered, footnotes are listed under Notes, and tables are fitted to the page. Slides are not made; ask for a PDF or Word document with a heading per slide.
- **Several formats:** ask for Markdown, PDF and Word together and it writes the Markdown once and makes the others from it. Ask for a change and it edits the Markdown and remakes them in seconds. A PDF or Word document you replaced with your own is left alone. A text file in the library is edited in place too.
- **Move, rename, copy and delete** in the library. Moves, renames and copies run at once, inside one library, and never replace a file. A delete asks first and cannot be undone; only files are deleted, never folders. Copying or moving to another library or system is a transfer (see **Transfers**).
- When a reply ends, chips above the box suggest what to do next, and one for each file the reply wrote opens it in the Library.

### Run work from a chat

- Ask to run work on a connected HPC system. The assistant follows the run and explains a failure. A side view shows the run's form, the live graph of its jobs, each step's log, and the web session it opens. **Beside chat** adds a session as a tab there, **Full screen** shows it over the Studio, and **Pop out** opens it in a window.
- Ask to see your sessions or your desktop. Each comes back as a card that opens it and keeps its state current. The assistant does not see what is on a desktop's screen.
- Ask what storage you have, or to start, resume, stop or destroy a cluster, cancel a run, launch a Status Monitor, or start pw code on a cluster. Each change shows the exact call and waits: **Yes, allow this call** or **No, deny this call**. A request left for 30 minutes expires. Only the person who asked can answer, and it runs with their own sign-in.

### While a reply is written

- A message sent while it runs steers it: the reply takes it in at its next step and shows "You added". One sent while the final answer is written goes out after it. Escape stops the reply while the chat is on screen.
- Thinking and tool calls stay folded to one line each; click one to open it. Finished calls fold into a row such as "Ran 3 searches, read 2 files". A search's row also has **GUFI command**, the commands it ran, as Search does. Only you see them.
- A reply with no answer says why and offers **Retry with** links to the other models. Nothing goes to another model until you click one. A provider that rate limits is retried twice. A message whose reply was cut off offers **Retry**.
- Ctrl+F (⌘F on a Mac) finds text in the open conversation; Enter and Shift+Enter step through the matches, and Escape closes the bar.
- Where the chat is too narrow, the buttons at the top right show only their icons; hover one for its name.

## Library

The folder tree beside a viewer. Markdown, code, CSV and TSV tables, images, PDFs, office documents, and STL and STEP models open in place.

A name too long for the tree shows whole when the pointer rests on it or the arrow keys reach it. Drag the edge between the tree and the file to widen the tree, or double-click it to fit the widest name. Tab reaches the tree: the arrow keys move between rows and open and close folders, and Enter opens.

### Which libraries you see

- **On your own computer**, the main library is your folder, read and written as you, called **My library** or the name you give it in **Settings**, **General**. Other libraries are named by their folder.
- **On a Studio you sign in to**, the main library is the one everyone searches, called **Shared library** unless the deployment names it. **My library** is your own folder on a cluster, read and written as you, and your other libraries are yours too: folders on clusters, ACTIVATE buckets, and folders shared with you.
- The picker above the tree names each library with what you may do in it, **Read/write**, **Read only** or **Shared**, and the system it is on, such as "Runs · Read only (on cluster-a)". **All libraries** shows them as one tree, a folder each. Each browser tab remembers its choice.
- The Shared library is **Read only** for you unless you administer the Studio. Upload, New folder, New file, Edit, Rename, Cut, Paste, Delete and Labels are shown disabled, with the reason on hover. You can read, search, download and copy from it. Keep your own files in My library.

### Add a library

Click the arrow beside **+ Add**, then **Add a library…**; **Settings**, **Indexing**, **Libraries**, **Add a library** opens the same form. The arrow shows to those who may add libraries.

1. **Choose a computer.** It lists your clusters, then your ACTIVATE buckets, and on a Studio only you use also **This computer**, the hosts in your `~/.ssh/config`, **Another host…** and **Another bucket (rclone remote)**. Without ACTIVATE only this computer, hosts over ssh and rclone remotes are listed. A cluster that is off is greyed; start it on ACTIVATE first. **Another cluster…** takes a name.
2. **Choose a folder.** The list shows that computer's folders as you; type a path or open one. **New folder** takes one that is not there yet, which is made, closed to other accounts, once you confirm. A remote folder is indexed, read and written there as you, never as the Studio's account. The folder is checked as soon as you choose it, and one card says what it found. A red line stops and says what to do; **Check again** repeats it. An amber line, such as a folder another account could change, does not.
3. **Review and Index.** **Name** is the folder's. **Who sees it** is **Only me** or **Shared**, offered for a folder you own. **Index depth** says what it adds: names, text, text in images, meaning. **More options** holds **Use as My library** (chosen already for your first folder), the index's folder and the rest. Then **Index**.

A library joins the picker when its build finishes, without a restart. It is searched with the rest.

- **Connect an existing index**, under the row, takes the path of an index that is there already, read only.
- **Folders another account could change**, such as a project folder its group writes in, are indexed, read and written as you, and the card says who else can change them. The Studio makes each change with a small helper of its own in your `~/.activate-studio`, which works from the folder itself and follows no link. Where the helper cannot run, the folder is read only here and the warning says why. If someone moves or replaces the folder, the Library stops writing and says so; add it again to go on. To keep a folder to yourself, run the `chmod` the check names, then **Rebuild**.
- **A folder somebody shared with you** is listed under the form once you choose its cluster, under **Shared with me**, with **Add**. It is indexed and read as you.
- **Import a library list…** in the same menu adds the libraries of a file exported from another Studio; see the end of this section.

### Add an ACTIVATE bucket

In **Add a library…** (Preview), choose the bucket from the same list as the computers. Choose a folder of it, or the whole bucket, and tick others under **Add as well** to add several. **List and index** lists it; nothing is copied and no key is entered.

- It needs only your ACTIVATE sign-in: your own under **Settings**, **Accounts** on a Studio people sign in to. There it is yours alone, and it is listed again while the Studio keeps your sign-in, otherwise when you next open the Studio.
- Listing takes about one request per 1,000 objects, and search, Stats and the tree then work on names, sizes and times. Opening a file reads that object alone, the first 512 KB of a text file, or up to 50 MB for a download or preview. The card shows how many objects it holds, what a listing costs on AWS, and **List again**; it is listed once a day to begin with, or hourly, every 6 hours, or only when you ask.
- **Read text** is off until you choose **After each listing**. Then it reads the text of files and documents up to 100 MB each, so search finds their words, after saying how many objects, how much data and what it costs. **Stop reading** stops it, and later listings read only what is new or changed. Images are not read for text.
- Buckets on AWS and Google Cloud are asked directly, those in Azure through the pw CLI, and one in Oracle Cloud cannot be read. If the Studio reaches the store only through a VPC endpoint, choose it under **More options**, **Endpoint**; where people sign in it is chosen from those the administrator lists.
- Removing a bucket library deletes its index and never the objects. An rclone remote (**Another bucket (rclone remote)**, on a Studio without sign-in) works the same way for any S3 compatible store.

### Share a folder

Right-click a folder you own in My library or a library of yours on a cluster, then **Share…**. Choose **A person**, **A group** you are in, or **Every account**; a person is named by their account on the cluster. Choose **Read** or **Read and write** (everyone only reads) and click **Share**. The Studio sets the access as you with the cluster's own permissions. **Stop sharing**, in the same dialog, takes it back.

The people you share with, by name or through a group, get a notice with **Add to my libraries**, and find it in **Add a library…**, once they choose the same cluster, under **Shared with me**. The notice shows once they sign in with ACTIVATE, and goes if you stop sharing. A folder shared with every account sends none. On a host over ssh **Share…** is not offered. Details are in `docs/MULTI-USER.md`.

### Copy a link

**Copy link**, on a right-click of a file, folder or library and in the viewer's header, copies a link to that item. It opens the file in the viewer, or the folder in the tree.

- **A link gives no access.** Whoever opens it signs in and needs their own access to the item, checked as them each time; being an administrator of the Studio is not access. Share the folder first. Someone without access and someone with a made-up link get the same answer.
- The link holds no file name or path, so it can go in an email or a ticket. The Studio keeps a log of links made and opened, with who and when, and no path or name.
- It follows the item when it is renamed or moved in the Studio. If the item was deleted, moved outside the Studio, or replaced by another file, the link says so.
- Opened while signed out, it asks for sign-in and then opens the item. If ACTIVATE itself asked you to sign in, the Studio may open its first page instead: **open the link again**.
- Someone you shared a folder with opens links into it once they have added that folder.
- A link lasts as long as the Studio's address. A hosted Studio needs a fixed subdomain for that; ask its administrator. In the app on your own computer the menu reads **Copy link (this computer only)**, since nobody else reaches that Studio.

### Add, edit and move files

- **+ Add** takes files or a folder, by dropping them or with **Choose a folder**. Folders keep their structure; names that start with a dot are left out. A file over 16 MB goes up in parts and appears only when all of it has arrived. The free space is checked first. Progress is below the tree, and you can leave the page.
- **New file…**, on a right-click of a folder or the arrow beside **+ Add**, makes a text file; `.md` and `.txt` are common. The file opens in the viewer, ready to type.
- **Edit**, in the viewer's header, changes a text file up to 1 MB. **Ctrl+S** (Cmd+S) saves and keeps editing. Leaving with unsaved changes asks first. A save never writes over a change made since you opened the file: the Studio offers **Keep theirs**, **Save mine as a copy**, **Overwrite** or **Keep editing**.
- **Right-click** a file or folder to label it, rename it, add a folder, copy it, download it or delete it. **Download as zip**, on a folder, leaves out names that start with a dot and refuses a folder of more than 50,000 files or 8 GB (500 files or 1 GB in a library on another system).
<!-- ai -->- **New pw code chat here**, on a right-click of a folder, file or library, opens a new pw code chat whose session works in that folder, as you, on the machine that holds it. On a file it uses the file's folder and puts a link to the file in the message. Nothing starts until you send, and the choice is for that chat alone. It shows to people with ACTIVATE. Where a chat cannot start there, it is greyed out and says why, such as a bucket, a folder you cannot write in, or a machine with no pw code connected; it is never started elsewhere instead. Where pw code stops answering, the menu says so within a few seconds of being opened again, and offers it again as soon as it answers. **Add to chat** puts the file in the chat you have open as a link, or a folder's path, with the library's name.<!-- /ai -->
- **Copy to another library…** copies through **Transfers** when that page is on: choose the folder, check the plan, then start. Dragging between libraries, and **Paste** into another library, copy the same way. Copies leave the originals and are written as you.
- A change in the main library is indexed at once. The index of a library of yours on a cluster catches up about 20 seconds after the last change.
<!-- ai -->- **Where the assistant writes** on a Studio with My library: the library you name ("save it to my library", or a path that starts with its name, such as `My library/notes/plan.md`). When you name none, it uses the library the chat has been working in, or asks **Where should this be saved?** with a button for each place. A file the assistant may not change is refused with the reason.<!-- /ai -->

### View files

- **Find**, in the viewer's header, searches the open file. **Print** opens an HTML page in a tab of its own.
- **Indexed text** shows what search sees for a file, including text read from images. A file with none says why, such as **Not indexed yet** or **Added or changed**, and names what adds it.
- **Excel workbooks** (`.xlsx`, `.xlsm`) open as a grid with a tab for each sheet, values as the file formats them, and the formula of the selected cell above. Filter rows, wrap text, and copy a selection. **Print view** shows the workbook as LibreOffice prints it, charts included. `.xls` and `.ods` open on their print view.
- **CSV and TSV** open in the same grid with the first row as the header; **Source** shows the text. Very long sheets are cut at 10,000 rows, 256 columns or 300,000 values, and say so.
- **PDFs** open on their pages. **Word and PowerPoint** files are drawn by LibreOffice where it is installed, and otherwise show the text and pictures read from them.
- **Zoom** a PDF, Office file or image with **+** and **-** in the header, Ctrl (Cmd on a Mac) and scroll, or a pinch. Click the percentage to fit again.
- **Email** (`.emlx`, `.eml`) opens as its message.
- The eye above the tree shows hidden files and folders. A file moved or deleted while open says so in place of the preview.
- **Details**, in the status panel, describes what you last clicked: size, files, latest change, kinds, labels, and the library it comes from.

### Your libraries in Settings

**Settings**, **Indexing**, **Libraries** shows each library as a card: what you may do in it, its state and counts, and the action that keeps its index current (**Update index**, **Rebuild**, **List again**, **Check** or **Refresh**). **Delete index** and **Remove** are under **More** (**⋯**) and ask first.

- **Originals via**, on a library whose files are not on the host that holds its index, names another host over ssh that has them. The viewer, download and **read_original** then try both.
- **Transfers**, **Keep a local copy and index it…** keeps a copy of a folder from a Transfers location, such as an SFTP host, and indexes the copy. Open the folder to copy first, or choose **Copy this whole folder**: browsing often starts at a whole home folder. **Update now** looks for changes at once. Offered only on a Studio without sign-in.
- **Export…** saves ticked libraries as a `.studio-libraries.json` file: where each one is and how it is built, and no keys or settings. **Leave out the user name**, on by default, lets each person connect with their own login, and **Connect as** takes it when the file names none. **Import…** (or a file dropped on the page where you may add libraries) lists what is in such a file, and adds what you tick.
- A folder on your own computer also offers suggested folders, such as Documents, Desktop and Downloads, and on macOS Apple Mail's folder.


## Transfers

Transfers copies, moves and syncs files between the places Studio can reach, and lists every transfer with its progress. A transfer runs on its own while Studio is running, so you can use other pages. **Transfers** in the navigation shows how many are under way, and a red dot for a failure in the last day you have not seen.

### Start a transfer

**New transfer** shows two locations side by side. Select files or folders on one side and press the **Start** button pointing to the other. **Back to activity** returns to the list.

- **Copy** puts the selection in the destination folder. A name already there stops the plan unless **Replace files that already exist at the destination** is ticked.
- **Move** copies, checks the copy, and only then removes the originals.
- **Sync** makes the destination match one source folder by sending new and changed files. It deletes nothing unless **Delete files at the destination that are not in the source** is ticked, and the plan then lists every deletion.

Every transfer is confirmed. **Start** shows the plan: what goes where, the count and size, what is replaced or deleted, and how the files move. Files land in a staging folder and take their names only after Studio checks the count and sizes. On an object store rclone writes each file straight to its name, so a transfer that fails leaves the files already sent there. Choosing **Automatic** for how files move uses rsync where both ends have it, rclone where one end is an rclone remote, otherwise Studio's own copy over ssh or SFTP, or CONDUIT where a site runs it.

A transfer into the main library is indexed when it finishes, and the tree, Stats and the counts follow within about 15 seconds. Another library's index shows the change when it is next refreshed; the plan says when.

You can also start one from Search or Query: tick results and press **Transfer…**, then choose **Copy** or **Move** and a destination. A selection in several libraries makes one transfer per library, confirmed one at a time.

### Locations

A location is a library (marked **Indexed** when it has an index), a folder on this computer, one of your clusters on ACTIVATE, a folder on another computer over ssh, or a remote of your rclone configuration, such as an S3 bucket. A read-only location can be a source and not a destination.

- A library can be written when Studio reaches its files with write access: the main folder, a library folder on this computer, or one on another computer reached with your own key or login. With sign-in on, the main folder is written only by administrators, and other folders on this computer are read only. Before a transfer, Studio checks that it can write in the destination, and on an object store writes and deletes an empty test object.
- **Settings**, **Transfers** shows what is ready. **Add a location** there takes a name, a host and a folder, and tests the connection before saving. An account that allows SFTP only works, with Studio's own copy. With sign-in off, a side whose library is read only offers **Save a location for its files…**.
- With sign-in on, saved locations and rclone are off, each person sees only their own transfers and reaches clusters with their own sign-in, and the operator sets these settings in the Studio's environment.
- Studio uses the rsync and rclone installed on each end and never changes them or their configuration. Copies here and sends work with rsync 2.6.9 and later. Pulling from a host needs rsync 3.2.5 or later on this computer, or a build with the fix for CVE-2022-29154; without it Studio's own copy is used and the plan says so. On a Mac, Homebrew's rsync is used when it can do more.

<!-- ai -->### Ask the assistant for one

Ask for a transfer, such as "copy the reports/2026 folder from the library to my cluster". The chat shows the plan and waits for **Yes, allow this call**; **No** cancels it. The transfer is then listed here, and a note in the chat says how it ended. The assistant can report on transfers and cancel one, which asks first. With sign-in off it can propose a saved location, tested before it is saved. Switch these tools off in the chat's Tools menu.<!-- /ai -->

## Search

One box searches four ways, and each result says how it was found: **name** (any part of a file's name), **folder** (its folder has the search in its name), **words** (whole words inside files: PDFs, office documents, notes, code, and text in images), and **meaning** (passages that say the same thing in other words). Hover the label to see every way a result was found. **How search works**, under the results, explains each kind.

- **Searching**, above the box, picks the libraries: tick any of them, or **All libraries**. Each result says which library it is in when more than one is ticked.
- **Refine** splits the box in two. **Name or path** takes words and patterns, such as `*.docx`, with OR and -word. **Text or meaning** takes words inside files in full-text syntax (`AND`, `OR`, `NOT`, `"phrase"`, `prefix*`). **Search text and meaning only in files whose name or path matches** lists only files that match both.
- Use "quotes" for a phrase, OR between alternatives, -word to leave a word out, and word* for words that start the same way. A search with quotes, a short word, or a code such as ABC-12 matches words only.
- Results found more than one way come first, then the best of each kind. Label chips under the box narrow them.
- Tick results to label them together, or press **Transfer…** to copy or move them.
- **Export CSV** saves the results listed (with **Load more** for up to 1,000), and **Copy** puts them on the clipboard for a spreadsheet. The address carries the search, so it can be bookmarked.
- **GUFI commands**, under the results, shows the commands that found them, ready to copy and run, with their time, exit status and **Error output**. Lines saying "no such table" for the text or vector tables are expected, from folders without text or vectors.

When a search leaves a library out, or searches it only in part, a line above the results names it and says why, such as that its text is not read yet, it did not answer within the time limit, or it could not be reached over ssh. Its name opens the library's card in Settings. Each library's search has a time limit, 20 seconds by default; **Settings**, **Indexing**, **Sync and search** sets 10 seconds to an hour, or **No limit**. **Stop**, beside **Search**, ends one that is running.

### How deep a library is searched

A library's **index depth** is how far its index goes, each stop adding to the one before and taking longer: **Names**; **Text** (the words inside files); **Text + OCR** (text in images and scanned pages); **Meaning on documents** (search by meaning over the first few passages of documents and images); and **Meaning on all files**, about three times the indexing time. Libraries Studio builds start at Meaning on documents. Change it in **Settings**, **Indexing**, **Libraries**, in each card.

Search by name works first, then text, then meaning in the background; the line above the results says when meaning is still being computed. A library built on this computer takes a new depth at once. One on a remote computer offers **Rebuild now** or **Later**, and keeps answering from its current index until then. Where a rebuild says **Ready; some files were not read**, its card names the reason and **Rebuild** reads only those.

## Adding material

Drag files or folders onto the tree in the **Library**, or use **+ Add**, which also takes a web address to save a page or PDF. On a Studio you sign in to, add to My library or your other libraries unless you administer it. New material in the main library is searchable in about a second. Files copied in outside Studio are picked up within minutes by the background sync, or at once with **Sync now**, at the bottom of the navigation, for those who may change the main library.

# Organize and analyze

## Labels

Labels organize files without moving them. Label a folder and everything in it carries the label, including files added later. Apply labels from the tree, the viewer, search results<!-- ai -->, or by asking the assistant<!-- /ai -->; filter by them in Search, Query<!-- ai --> and the chat's Scope<!-- /ai -->.

- With several items selected, the Labels panel lists a label only some of them carry under **On some of these items**: click it to add it to the rest, or its X to remove it. The tag button at the top of the tree shows or hides each item's own labels.
- In the main library on this computer, labels are saved on the files; elsewhere, including read-only folders, Studio keeps them itself and never changes the files. They are removed with the library. The Labels panel says which.
- <!-- ai -->**Suggest labels…**, on a folder's right-click menu, asks the model to propose labels for the files under it that have none. Nothing is saved until you accept.<!-- /ai -->

## Query

Structured questions about the files themselves (largest, oldest, totals by type or owner), answered in milliseconds in the libraries ticked under **Querying**.

- Pick a preset under **Start from** and change it, add conditions, or use **SQL** for read-only SQL of your own; **Save query** keeps one to reuse. Sizes take units (`10 MB`), and dates take a day (`2026-01-01`) or a time ago (`7 days`).
- **GUFI commands** shows what ran, and **Show SQL** or a saved query's code button shows the SQL it runs.
- Columns of your own SQL are named as SQLite names them: `SELECT name AS file, size AS bytes` heads the columns **file** and **bytes**, and a column named `bytes` shows sizes in units. With `*` columns are named col1, col2 and so on.
- Rows with a **path** or **directory** column can be ticked to label or **Transfer…**. A row totalled over libraries can be labeled but not transferred.
- **Export CSV** and **Copy** take the rows returned, up to 2,000; for more, narrow the query or run the command in a terminal.

## Stats

What your libraries hold: size, file types, labels, and recent changes. **Statistics for** picks a library. Click any figure to see the files behind it. **Refresh** reads everything again, and **Index runs** lists recent builds and how long each phase took; **View log** opens one.

Two maps each show one library, picked beside the map or in its full screen:

- **Corpus topology** shows folders by size. Click a folder to open it; the crumbs back out.
- The **semantic map** places files by what they say. Click a cluster for its files, or a file for its nearest files by meaning; **Open in Library**, a double-click or Enter opens it. **Show links** draws the closest clusters, and **3D** turns it. It needs a library with search by meaning; on a remote computer the map is fetched, with a sample of up to 3,000 files, and **Refresh** fetches it again.

## History

Snapshots of the main library's file list, with sizes and dates, taken after each index pass, each with what it added, removed and changed. Pick one to see the folders and files as they were; opening a file shows it as it is today, since contents are not kept. **Capture now** takes one at once. Deleting a snapshot removes only the record.

# Run work

## Workflows

On the ACTIVATE platform, Workflows shows the workflows chosen for Studio; administrators choose them under **Settings**, **Workflows**. Open a tile, fill in the form, and press **Run**. **Validate** checks the inputs without running anything. Runs use your own account.

- Saved configurations appear as presets at the top of the form. The form says where the workflow comes from, and one kept in a repository shows its README beside it; **Hide README** puts it away. A workflow from GitHub that asks for your account variables waits until you tick the approval.
- **Sessions** lists the apps your runs open, such as a notebook, and your ACTIVATE desktops. Click one to show it in a side view, or press **Window** or **New tab**. Click a run's id for its form, graph and session. A session started from a chat also shows in that conversation.
- **Desktops** are drawn by the Studio. Click in one to type. The bar above it has **Fit**, **Scale** or **Actual size**, the picture quality, **Clipboard**, **Ctrl+Alt+Del** and **Logs**; **Pan** moves around a desktop larger than its view. A dropped connection is tried again by itself.
- Without ACTIVATE, run workflow YAML files of your own with [Meridian](https://github.com/parallelworks/meridian), the open-source workflow engine.

## Agents

Ask the assistant to delegate a piece of work, such as "delegate the Q3 pump test summary to the reviewer". It starts a pw code session in a new folder under `agents/<instructions>/`, or the folder you name, with the instructions you chose in that folder's `AGENTS.md` and a goal it works toward until it is met. The session is on the **Code** page, marked delegated, with a **Delegated** filter. When it finishes, the Studio indexes what it wrote, and the assistant gives you a link and can say how it is going. It reads the library and writes in its folder without asking; a shell command beyond listing its folder waits for you. Delegation is turned on under **Settings**, **External access**.

You can also ask the assistant to run a pw code agent on one of your clusters ("start an agent on my cluster in ~/project to fix the failing tests, and keep going until they pass"). It starts there with your key and its goal; ask how it is doing, give it direction, or pause, resume or clear its goal. `pw code attach <cluster>/<session>` opens it in a terminal. When it runs where your library is, what it writes is indexed and linked when it finishes. To pick up a session you have, name it or say what it was about.

The **Agents** page has three tabs:

- **Earlier tasks**: tasks of the earlier engine, kept to read, with their results under `tasks/`. Work you delegate now is on the Code page.
- **Fleet**: standing agents that wake on a schedule or when something changes, each a pw code session working in `agents/fleet/<name>`. What an agent asks is named above the list, with **Approve** and **Deny**; its detail has its conversation, **Wake now**, **Pause**, **Retire**, and its session on the Code page. They run as the account this computer's pw code is signed in to, so they need a Studio of one with ACTIVATE; on a Studio where people sign in, none run. Without ACTIVATE, **Fleet** shows only where there are agents already, to read or retire.
- **Instructions and skills**: write how agents should work once, and **Apply to a folder** puts it in that folder's `AGENTS.md` (or `.agents/skills` for a skill). Pick a library you may write, then a folder. The list below shows where each is applied, with **take off**. pw code uses a folder's skills once you have opened that folder in pw code and approved it.

## Code

Code shows your pw code sessions as PW Code on the web does. It is in the menu once you sign in with ACTIVATE under **Settings**, **Accounts**, and can be shown or hidden in Setup's **Sidebar**. It lists your sessions on the platform's machines, with your own sign-in or key and never the Studio's account. Its entry in the menu counts the sessions waiting on you, which **Notices** lists too. On a Studio of one with the pw CLI signed in, it lists this computer's own sessions.

### The list

Sessions are grouped: what needs you, pinned, your own groups, then active and inactive, and archived last. Each row has its state, its machine, its pull requests, and **in a chat** (a link that opens that chat) or **delegated** where the Studio uses it. **Delegated** shows only delegated work. A row's **⋯** menu has **Open**, **Continue in Assistant**, **Open in ACTIVATE**, **Rename**, **Pin**, **Fork**, **Stop**, **Archive** or **Restore**, **Move to group**, **Leave worktree** and **Delete**. Archiving or deleting a session in a worktree asks what to do with the worktree. **New session**, **Open in ACTIVATE** and **How sessions appear** are at the right of the header. To list a session you run in a terminal, run `/rc` in it, or start it with `pw code --remote`.

### Start a session

Choose the machine (the Studio's own, marked **(this Studio)**, is first and is the default until you pick another), the folder (type a path, Tab opens a folder, Enter uses it), the permission mode, the model and the effort, then describe the task. The first message may have images and may start with `/goal` or `/plan`. While it starts you can open other sessions; if it does not start, your message is back in its box with the reason. On the Studio's own machine a session always has the Studio's tools; on another machine, where pw code there has them, which a mark beside the machine shows. Below, what waits on you is answered in place.

### Continue in Assistant

**Continue in Assistant** carries a session on in a chat, which shows its whole transcript and each turn live as it runs, whoever started it. A session on another machine stays there; your messages, answers and **Stop** go to it through ACTIVATE as you, and it takes at most what its machine allows from ACTIVATE. On a Studio where people sign in only you see such a chat, even where chats are shared with everyone, since it holds your session's turns. The chat asks first and says where it runs. The chat and the session have one name, and the last rename wins.

- **/** lists the session's commands (`/clear` starts a new session in the same folder). **!** runs a shell command on its machine. **+** or a paste adds an image. **Ctrl+B** moves a shell command it waits on to the background. Tasks, goal, subagents and pull requests sit over the message box. The session's terminal button gives the command that picks it up in a terminal.
- A question shows its choices and a box for your own answer; **Add notes** (or **N**) on a choice sends a note with it, which pw code 7.142.0 or later passes on. A plan waits for **Approve plan** or **Keep planning**. A subagent can be read, steered, stopped or dismissed.
- A session the Studio cannot show for you has its card with why, and what it waits on to answer; an archived one is restored from its menu.

### The Studio's tools on another machine

A session on another machine has the Studio's search tools once its pw code has this Studio added, with a `studio` entry that asks pw on that machine for its own access token, so nothing secret is written there. It works where the Studio is opened through ACTIVATE and MCP access is on.

- **A cluster of yours, or your user workspace:** the first session or chat you start there has **Add the Studio's tools to cluster-a** (the machine's name), ticked; continuing a session there asks the same; and **Add** is in the Code page's machine menu. It reaches your machine with your own sign-in over pw ssh, checks pw is signed in as you, and adds the `studio` entry. A workspace that has shut down says so: start it, then add them. It leaves the entry for another Studio alone until you say so. **Check** and **Remove**, at the end of the machine menu, look again and take it out.
- **A computer that connected itself to ACTIVATE:** the Studio cannot open a terminal on it. The mark beside the machine shows a one-line command to run there, with **Copy**, and a chat's card has it too. It fetches the setup from this Studio with that computer's own pw sign-in. It needs curl; without it, **No curl there? Use an ACTIVATE API key** has another.
- The tools that only read run without asking on that machine in turns you type; a turn sent from a chat or PW Code on the web asks, and in read-only or plan mode does not run them. **Remove** takes out the entry and these rules and leaves the rest. A project with its own `.mcp.json` needs the Studio added there too.

# More

## Getting around

The address bar follows what you are looking at, so back and forward work.

- **Notices**, the bell in the navigation, lists what needs you across the Studio: chats and sessions waiting on you or with a reply you have not seen, delegated work that ended (for people with ACTIVATE), transfers that failed in the last day, libraries whose index could not be built, and folders shared with you. A shared folder's notice says who shared it, on which machine and whether you may write in it. Each opens where it is dealt with, and **Dismiss** takes one off until something else happens there. Assistant, Code and Agents in the menu count from the same list, amber where something waits on you and blue where there is only something to read. Where people sign in, each person sees only their own.
- **Search**, in the navigation, or Ctrl+K (⌘K on a Mac), opens Search from anywhere.
- In the desktop app, Ctrl+= and Ctrl+- (⌘= and ⌘- on a Mac) make everything larger or smaller, and Ctrl+0 returns to actual size. In a browser, its own zoom does this.
- The pages are under **Knowledge** and **Run**; click a group's name to fold it away. On a window 760 pixels wide or narrower, as on a phone, the navigation is a bar along the bottom, and in the Library the tree fills the page until you open a file.
- **Install app**, in the navigation, installs Studio as a desktop app in its own window, with Studio's icon or the one this Studio was given. An app installed before the icon changed keeps the old one: in Chrome, open it, click **App update available** in its title bar, then **Review app update** and **Update**. In another browser, or if it was added from Safari, remove the app and install it again.
- The bottom of the navigation shows who you are signed in as, whether the models are reachable, the index's file count for the library selected, and when the background sync last ran, with **Sync now** for those who may change the main library. Click any of them for details.
- A machine or cluster of yours shows its icon and name wherever the Studio names one; point at it for its host name.
- A status reads the same everywhere: a blue pulsing lamp is work under way, green is done or ready, red is a failure, amber needs a look or an answer, and an unlit ring is idle.
- The **status panel** at the right shows what is running, your libraries, the model in use and **Connections**. The panel button at the top right of each page opens it, and a card's header opens its detail.
- **About**, at the foot of Help's list, shows the version and license. ACTIVATE Studio is built and maintained by Parallel Works.

## Setup and the sidebar

On a computer of its own, Studio opens with a short setup. **Skip this step** moves past one without changing it, and **Finish later** closes setup; everything in it is also in Settings. On a Studio people sign in to there is no setup, since the settings are the deployment's.

- **Your Studio**: the name and icon. **Appearance**: Light, Dark or System, and the accent color.
- **AI features**: off on a new install until turned on. Set an OpenAI-compatible endpoint, its key and a default model; **Test** lists its models and whether the model calls tools. Off, Assistant and Agents leave the sidebar, the settings only they use are hidden, and nothing is sent to a model. Search, the library, query and statistics work the same. Turn it on in **Settings**, **General**, **AI features**.
- **Sidebar**: which pages show. Workflows starts hidden on a new install; it needs an ACTIVATE credential.
- **My library**: the folder to index. Choosing another folder restarts Studio on it in the macOS app; elsewhere set `KB_ROOT` and restart. **Index depth** sets how far its index goes, and **Build the index** runs in the background.
- **Other libraries**: another folder on this computer to index, or under **Other kinds** an existing GUFI index here, over ssh, or on one of your running clusters. A machine behind a jump host works with its Host alias, or `-J you@jumphost` in **SSH options**. A machine whose login runs only `gufi_query` can be browsed, searched and queried, but not opened or indexed. **Import a library list from your administrator** adds the libraries in a file.

**Settings**, **General** has tabs for **Appearance**, **Sidebar**, **AI features**, **Updates** and **Server**. **Settings**, **Indexing** has **Libraries**, **Sync and search**, **Building** and **Storage and logs**. Each setting saves as it changes, except where a **Save** shows. **Width**, on the Sidebar tab, sets how wide the sidebar is, and dragging its edge does the same.

On a Studio with sign-in, settings belong to the administrators: the deployment's owner and the accounts they name. Everyone else sees them read only, with that reason at the top. The theme you pick, your sidebar width, your own accounts and keys, and your own libraries stay yours.

## Signing in

A Studio may ask you to sign in with your organization's account, and then takes you to the page you were on. Your name at the bottom of the navigation shows what the sign-in says, with **Sign out**, which ends your session here and not your organization's. Inside ACTIVATE's session view, **Sign in** opens a new tab; keep using Studio there.

Signing in tells Studio who you are. It does not let Studio act for you on the platform: that takes your own **Sign in with ACTIVATE** under **Settings**, **Accounts**.

## Accounts

**Settings**, **Accounts** holds the accounts Studio uses for you. A Studio of one works without any: libraries on this computer, remote computers over ssh, and chat with a model of your own. On a Studio people sign in to, browsing and searching the main library need none. Where its administrator requires everyone to use an account of their own, sign in with ACTIVATE first; its row starts open. Click a row to open or close it.

- **OpenAI-compatible endpoint**: models from any service that speaks the OpenAI API, such as a site's portal or Ollama on this computer. The base URL usually ends in `/v1`. **Advanced** is for portals that differ, such as the header the key goes in. **Test** checks the endpoint and whether the model calls tools, which search needs. It covers models only, never the platform. A Studio started with `OPENAI_BASE_URL` and `OPENAI_API_KEY` for a service that is not an ACTIVATE platform shows that endpoint here as **In the Studio's environment**: its key is a model key, so ACTIVATE stays **Not signed in**.
- **ACTIVATE**: turns on pw code and the Code page, libraries on ACTIVATE systems, workflows, desktops, the assistant's platform tools and models. **What it turns on in this Studio** lists each with its state; a part that has no sign-in says **Sign in to use** there, and **Sign in with ACTIVATE to use this** on its own page. The Code page and the choice of pw code show once you sign in.
- **Claude Code accounts**: the accounts pw code signs in with where it runs for you here, with how much of each one's limits is used. They are on for a Studio of your own; where people sign in, or ACTIVATE deployed the Studio, they are off unless you already had some, and the administrator chooses under **Settings**, **External access**, **Claude Code accounts**.

For ACTIVATE, choose how you sign in:

- **Sign in with ACTIVATE** (a Studio several people use): a code to approve on the platform. Studio keeps it for you alone and renews it until **Sign out**. If the platform stops accepting it, or it cannot be renewed, **ACTIVATE** says **Sign in again**, and so does each page that needs it; click **Sign in again** and approve a new code.
- **pw CLI on this computer** (a Studio of one): the platforms the pw CLI is signed in to; **Use** picks one, and **Follow the pw CLI's current context** follows it. **Stop using the pw CLI** makes Studio read none of this computer's ACTIVATE sign-ins and never signs the CLI out; **Use it again** turns it back on.
- **ACTIVATE API key**: a token or key from your account, for models and the platform. The host is the platform's own, such as `platform.example.com`; an address with a path is an endpoint, and **Use as an endpoint** moves it there.

On a Studio several people use, every ACTIVATE part runs as you, so workflows never run under someone else's account. Without a sign-in no part runs; only models set by the administrator for everyone are shared, shown as **From the administrator's endpoint**. **Subscriptions through ACTIVATE** connects a ChatGPT plan or GitHub Copilot.

Assistant uses your endpoint while one is saved, then your ACTIVATE sign-in, then your ACTIVATE API key. The platform uses only your ACTIVATE sign-in or key. Keys are sealed, for you alone, in this browser on a Studio several people use (untick **Remember in this browser** to keep them only in the server's memory).

## Model access

**Settings**, **Model access** says where models come from, with the models available or what failed; **Re-check** asks again. **Where models come from** chooses between ACTIVATE and an OpenAI-compatible endpoint; choosing ACTIVATE removes a stored endpoint only on **Remove the endpoint and use ACTIVATE**.

## Working offline

With no network, browsing, search, query, statistics and adding material keep working, and chat does too with a model on this computer. Anything that needs the network says so in one line, such as **offline** in the model banner.

## Networks that inspect HTTPS, and proxies

Studio trusts the certificate authorities this computer trusts as well as its own, so a network that inspects HTTPS works once its authority is installed; on a Mac, in Keychain Access. Behind a proxy it uses `HTTPS_PROXY` and `NO_PROXY`, and the macOS app takes the proxy from System Settings unless a PAC file sets it.

## Using it from other tools

pw code and other MCP clients can search this knowledge base, and any OpenAI-compatible client can use it as a model that answers from it. **Settings**, **External access** has the addresses and the exact commands.

## Updates

When a newer version is available, the version at the bottom of the navigation says **Update available**; click it for the **Build** panel, where **What's new** lists the changes. The macOS app, and the Linux app installed in your home folder, offer **Update now**: it downloads the release, checks its signature, installs it and restarts. A Linux package copy updates with `apt` or `dnf`, and a clone shows its commands. A hosted Studio is updated by its operator, or, when the ACTIVATE workflow launched it, by the person who launched it.

**Settings**, **General**, **Updates** also has **Roll back**, **Check for updates automatically**, and **Install updates automatically when idle**. Before every change of version Studio saves your settings, conversations and credentials, and puts them back if an older version cannot read them. **Channel** is **Stable** or **Preview**, which gets new versions first. If a new version fails to start three times, the app puts the earlier one back.

To remove the macOS app, choose **Uninstall ACTIVATE Studio…** in its app menu; it never removes your documents.

## The index

Studio is built on GUFI, the Grand Unified File Index from Los Alamos National Laboratory: a tree of small databases, one per folder, holding each file's details, its text, and search vectors. Access follows the file system's permissions, and a change re-indexes only the folder it touched.

A build runs in four phases, each with its count: **names**, **documents** (PDFs and Office files converted to text), **text** (images, other formats and full-text tables) and **meaning**. The index line at the bottom of the navigation, Settings and Stats show its progress; click the line for **Stop** and **View log**. Text and vectors are kept between builds, so an update redoes only what changed. A cut-off build is taken up at the next start.

Studio uses GUFI 0.8.1. An index built by an earlier version keeps working. One from GUFI 0.7.0 or earlier with rolled-up folders lists some files more than once, so its library says to rebuild it.

On macOS, a folder in Desktop, Documents or Downloads, in iCloud Drive, or on a removable or network volume can be read only once macOS allows it, in **System Settings**, **Privacy & Security**, **Files and Folders** (or Full Disk Access). Until then Studio indexes the rest and names what it could not read.

**Settings**, **Indexing**, **Building** has the threads, how documents are read, and **What this computer can read**, with the command that installs each reader this computer lacks. **Storage and logs** says where the index is and has **Log GUFI commands**. A format Studio does not read can be given a reader of your own, listed in `extractors.json`; CUSTOMIZATION.md has the details.

<!-- kbLabel -->
