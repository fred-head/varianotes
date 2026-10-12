# Archived RepoQuill agent plan (2026-10-05)

This file preserves the former root `AGENTS.md` so milestone specifications and project history remain available. Alpha 3 candidate ideas now live in `../roadmap/alpha3-candidates.md`. This archive is historical reference material, not current instructions: its requirements, roadmap status, examples, and commands may be stale or superseded. Use the current root `AGENTS.md`, `README.md`, `CHANGELOG.md`, relevant `docs/`, source, and tests as authority. Read this archive only when a task needs historical planning context.

---

# AGENTS.md

# Project: Git-Backed Markdown Notes

## Local Go cache policy

Local development, tests, `go vet`, race tests, `govulncheck`, and `gosec` must
share the persistent caches `GOCACHE=/home/fredmin/.cache/go-build` and
`GOMODCACHE=/home/fredmin/go/pkg/mod`, with temporary Go work files in
`GOTMPDIR=/home/fredmin/.cache/repoquill/go-tmp`. Do not use the RAM-backed
`/tmp` filesystem for large build, module, or security-scan caches.

## 1. Purpose

This project is a self-hosted, browser-based notes application whose canonical data store is a normal Git repository containing plain Markdown files and regular image/assets files.

The application should feel closer to a lightweight mix of Obsidian and Trilium, while preserving a fundamentally portable data model.

The core product idea is:

> The application is an interface for ordinary Git-backed Markdown repositories, not the owner of the user's notes.

A notebook maps to exactly one Git repository.

The application may maintain internal metadata for configuration, UI state, sync state, or notebook registration, but user note content MUST remain fully usable without this application.

---

# 2. Non-Negotiable Design Principle

The following rule is the most important rule in the entire project:

> The application must never require itself to recover, interpret, migrate, or export user note content.

If the application is permanently deleted, the user must still be able to:

- clone the repository,
- browse its folders,
- open Markdown files in any editor,
- read images and attachments,
- edit the repository with VS Code, Obsidian, GitHub, GitLab, or any other Markdown-capable tool.

Any future feature that breaks this guarantee must not be implemented in that form.

The Git repository is the canonical source of truth.

The web application is replaceable.

---

# 3. Primary Goals

The application MUST provide:

- browser-based access,
- responsive desktop and mobile UI,
- PWA support,
- a hierarchical folder/file tree,
- Markdown notes stored as real `.md` files,
- live WYSIWYG-style Markdown editing,
- common formatting controls,
- clipboard screenshot/image insertion,
- image upload from mobile devices,
- notebook separation via independent Git repositories,
- full-text search,
- Git synchronization,
- simple sync status reporting,
- self-hosted deployment,
- a single-owner authentication boundary by Alpha 2 that can be explicitly
  disabled for separately protected deployments.

The application SHOULD feel simple and fast.

It must not become a general-purpose productivity platform.

---

# 4. Explicit Non-Goals

Do NOT implement the following in V0.1 unless explicitly requested:

- graph view,
- knowledge graphs,
- backlinks,
- bidirectional relationships,
- databases,
- Notion-style collections,
- Kanban boards,
- task management systems,
- calendars,
- AI features,
- embeddings,
- vector databases,
- chat with notes,
- collaborative editing,
- multiple concurrent backend writers,
- CRDTs,
- Y.js,
- WebRTC collaboration,
- WebSockets unless truly required,
- PostgreSQL for note content,
- MongoDB for note content,
- object storage as the primary note store,
- S3 as the primary note store,
- GraphQL,
- Redis,
- microservices,
- Kubernetes-specific architecture,
- built-in account registration,
- multi-user account management,
- email-based password reset,
- OAuth provider implementation,
- plugin systems,
- themes marketplace,
- public sharing platform,
- publishing engine,
- mobile-native apps,
- offline editing,
- automatic repository creation through GitHub/GitLab APIs.

Do not add infrastructure merely because it is common in modern web applications.

Prefer boring, explicit, understandable solutions.

---

# 5. Initial Technology Stack

## Frontend

Use:

- React
- TypeScript
- Vite
- Tailwind CSS
- shadcn/ui where useful
- Milkdown as the preferred Markdown editor
- ProseMirror-based extensions where appropriate
- `vite-plugin-pwa` or an equivalent lightweight PWA integration

Do not switch editor frameworks without a concrete technical reason.

Tiptap may be evaluated only if Milkdown proves unsuitable for required editor behavior.

Do not use CodeMirror as the primary editing experience because the product should provide rendered, live WYSIWYG-style editing rather than Markdown source editing.

A raw Markdown/source mode may be added later as an optional feature.

---

## Backend

Use:

- Go
- standard library where practical
- minimal third-party dependencies
- regular filesystem APIs
- regular `git` CLI invocation

The backend should preferably build into one application binary.

Do not add Node.js to the backend unless there is a compelling technical requirement.

---

# 6. Deployment Model

The intended deployment model is:

```text
Browser / PWA
      |
      | HTTPS
      v
Reverse Proxy / TLS termination
      |
      v
RepoQuill / Built-in Auth
      |
      v
Local Git Working Trees
      |
      v
GitHub / GitLab / Forgejo / Gitea / other Git remote
```

The application should be deployable as a single Docker container.

The container should contain:

- the compiled backend,
- the compiled frontend,
- the `git` executable,
- required CA certificates,
- minimal runtime dependencies.

Notebook repositories should live on a persistent mounted volume.

Example:

```text
/data/
├── app/
│   ├── auth.db
│   ├── auth.key
│   └── notebooks.json
├── keys/
└── notebooks/
    ├── 01ABCDEF...
    ├── 01GHIJKL...
    └── 01MNOPQR...
```

The application itself must not depend on ephemeral container storage for important data.

---

# 7. Notebook Model

A notebook is exactly one Git repository.

Examples:

```text
Private       -> git@github.com:user/private-notes.git
Work          -> git@gitlab.example.com:user/work-notes.git
Homelab       -> git@github.com:user/homelab-docs.git
```

The application must support multiple notebooks.

Each notebook must be independently configurable.

Required notebook metadata may include:

```json
{
  "id": "01ABC...",
  "name": "Private",
  "localPath": "/data/notebooks/01ABC...",
  "remoteUrl": "git@github.com:user/private-notes.git",
  "branch": "main"
}
```

Additional internal metadata is allowed where useful.

User notes must never be stored in this metadata database.

---

# 8. Internal Metadata Database

SQLite is allowed for application metadata.

SQLite MAY contain:

- registered notebooks,
- notebook names,
- repository URLs,
- default branches,
- local repository paths,
- last successful sync timestamps,
- UI preferences,
- application configuration,
- optional cached search metadata.

SQLite MUST NOT contain canonical note contents.

SQLite MUST NOT contain the only copy of user images or attachments.

Deleting the SQLite database must not destroy the user's notes.

---

# 9. Repository Structure

There is no mandatory global note hierarchy beyond regular folders and files.

Example:

```text
Network/
├── BGP.md
├── BGP.assets/
│   ├── 01ABC.png
│   └── 01DEF.png
├── OSPF.md
└── OSPF.assets/
    └── 01GHI.png

Linux/
├── Debian.md
└── Docker.md
```

Markdown files are ordinary UTF-8 text files.

Directories are ordinary filesystem directories.

---

# 10. Asset Model

Prefer per-note asset directories.

For:

```text
Network/BGP.md
```

use:

```text
Network/BGP.assets/
```

for note-owned images.

Example Markdown:

```markdown
## Topology

![](BGP.assets/01ABCDEF.png)
```

Benefits:

- asset ownership is obvious,
- note deletion can remove its asset directory,
- note movement can move its asset directory,
- notes remain portable,
- no global opaque media database is needed.

Asset filenames should preferably use:

- ULID,
- UUID,
- or another collision-resistant identifier.

Preserving the original filename may be considered later.

---

# 11. Asset Ownership Rules

In V0.1, an asset should belong to exactly one Markdown note.

This simplifies lifecycle management.

When a note is deleted:

- delete its `.md` file,
- delete the corresponding `.assets` directory if present.

When a note is moved:

- move the `.md` file,
- move its `.assets` directory with it.

When a note is renamed:

- rename the note,
- rename the asset directory accordingly where safe,
- update relative Markdown links when required.

When an image reference is removed from a note:

- automatic deletion of the now-unreferenced asset MAY be implemented,
- but this must be conservative,
- never delete a file if reference ownership is uncertain.

A later garbage-collection feature may detect unreferenced assets.

Data safety is more important than automatic cleanup.

---

# 12. Editor Requirements

The main editor must provide a live rendered Markdown editing experience.

The user should normally see:

- rendered headings,
- bold text,
- italic text,
- inline code,
- code blocks,
- lists,
- links,
- tables,
- images,
- blockquotes,
- horizontal rules,

while editing.

The user should not normally need to switch between "source" and "preview".

The persisted representation must still be Markdown.

Editor state must serialize cleanly back to Markdown.

Avoid proprietary syntax where possible.

If custom syntax becomes necessary, it must degrade gracefully when opened outside this application.

---

# 13. Formatting Toolbar

The editor should eventually provide a formatting toolbar.

Suggested controls:

- Undo
- Redo
- Heading
- Bold
- Italic
- Strikethrough
- Inline code
- Code block
- Link
- Image
- Bullet list
- Numbered list
- Task list
- Quote
- Table
- Horizontal rule

V0.1 does not require every control immediately.

Core formatting should be implemented first.

Toolbar buttons must work with text selection and cursor context.

---

# 14. Slash Commands

Slash commands are planned but are not required for the earliest usable build.

Typing:

```text
/
```

should eventually open a command picker.

Examples:

```text
/heading
/code
/table
/image
/quote
/list
/task
```

Typing a prefix should filter commands.

Example:

```text
/co
```

could show:

- Code block
- Inline code

The slash-command architecture should support custom snippets later.

Example future snippet:

```text
/iptable
```

could insert:

```markdown
| Hostname | IP | VLAN |
|----------|----|------|
|          |    |      |
```

Do not implement a full plugin system merely to support slash commands.

---

# 15. Screenshot and Image Paste

Clipboard image paste is a core V0.1 feature.

Desktop flow:

```text
Ctrl+V
  |
  v
Clipboard contains image/png
  |
  v
Frontend detects image
  |
  v
Upload to backend
  |
  v
Backend stores file in note asset directory
  |
  v
Backend returns relative path
  |
  v
Editor inserts Markdown image
  |
  v
Image renders immediately
```

The user should not need to:

- save the screenshot manually,
- browse for the screenshot file,
- type Markdown image syntax,
- choose an asset directory.

Mobile should support:

- image selection,
- camera/gallery upload where supported by the browser,
- touch-friendly insertion.

Clipboard support on mobile may vary by browser and must not be the only image workflow.

---

# 16. File Tree

Desktop layout:

```text
+------------------+------------------------------------+
| Notebooks / Tree | Toolbar                            |
|                  +------------------------------------+
| Private          |                                    |
|   Network        |                                    |
|     BGP.md       |             Editor                 |
|     OSPF.md      |                                    |
|   Linux          |                                    |
|                  |                                    |
+------------------+------------------------------------+
```

The file tree must support:

- nested folders,
- nested Markdown files,
- create note,
- create folder,
- rename,
- delete,
- move,
- expand/collapse,
- refresh.

Desktop may support drag-and-drop.

Drag-and-drop must not be the only way to move content.

---

# 17. Mobile UI

Mobile must be treated as a first-class supported interface.

Do not simply compress the desktop layout.

Default mobile layout:

```text
+---------------------------+
| Menu | Note title | More  |
+---------------------------+
|                           |
|          Editor           |
|                           |
+---------------------------+
| B | I | Code | List | +   |
+---------------------------+
```

The notebook/file tree should open as a drawer or sheet.

Requirements:

- touch-friendly targets,
- no hover-only functionality,
- file operations available through menus,
- usable with mobile software keyboards,
- editor remains visible when keyboard opens,
- toolbar must not consume excessive screen height,
- tables may horizontally scroll if required.

Test responsive behavior at common phone widths.

---

# 18. PWA Requirements

The frontend must support installation as a PWA.

V0.1 PWA goals:

- installable,
- standalone window mode,
- responsive icon and manifest,
- appropriate app metadata,
- service worker,
- reliable loading of frontend assets.

V0.1 is ONLINE-FIRST.

Do NOT implement offline editing initially.

Offline editing introduces:

- synchronization queues,
- conflict resolution,
- concurrent history,
- local persistence complexity.

These concerns are intentionally deferred.

When offline, the application should fail clearly and safely rather than pretending edits are synced.

---

# 19. File Save Behavior

Editor changes should be autosaved.

Suggested behavior:

```text
Typing
  |
  v
500-1000 ms debounce
  |
  v
PUT updated Markdown
  |
  v
Backend writes file atomically
```

Use safe file-write patterns where practical.

Avoid partial file corruption.

The UI should expose local/server save state.

Suggested status labels:

```text
Saving...
Saved
Save failed
```

---

# 20. Git Synchronization

Git synchronization is separate from file saving.

This distinction matters.

"Saved" means:

> The Markdown file has been successfully written to the application server's persistent filesystem.

"Synced" means:

> The changes have been committed and successfully pushed to the configured Git remote.

Suggested statuses:

```text
Saved
Syncing...
Synced
Sync failed
Conflict
```

Do not present "Saved" as "Synced".

---

# 21. Git Implementation

Use the installed system `git` executable.

Do not implement Git internals unless absolutely necessary.

Expected operations include:

```text
git clone
git status
git add
git commit
git fetch
git pull --rebase
git push
git log
```

Wrap Git invocation in a clear backend service.

Capture:

- stdout,
- stderr,
- exit code,
- operation type.

Never expose arbitrary shell execution through API inputs.

Repository paths and Git arguments must be validated.

Avoid shell interpolation.

Use direct process argument execution.

---

# 22. Sync Strategy

Do not commit on every keystroke.

Suggested model:

```text
Editor change
   |
   v
Autosave file
   |
   v
Short inactivity period
   |
   v
Git sync job
```

An initial implementation may use:

- 30-60 second inactivity debounce,
- explicit manual sync button,
- sync when switching notes,
- sync when closing or changing notebooks.

Exact behavior may evolve after real-world use.

Avoid excessive meaningless Git commits.

---

# 23. Commit Messages

Commit messages should be understandable.

Examples:

```text
Update Network/BGP.md
Add Linux/Docker.md
Delete Private/Todo.md
Rename OSPF.md to OSPF Routing.md
Update notes
```

An aggregate periodic commit is acceptable.

Do not require the user to understand Git staging for normal use.

Git should be mostly invisible in the primary UX.

---

# 24. Remote Changes and Conflicts

Even with one application backend, remote changes may happen through:

- GitHub,
- GitLab,
- code-server,
- local Git clients,
- another editor.

Before pushing, fetch or rebase as appropriate.

If an automatic rebase succeeds:

- continue sync.

If a conflict occurs:

- STOP automatic sync,
- preserve all user data,
- report the conflict clearly,
- do not silently choose local or remote content.

V0.1 conflict handling may simply pause sync and require manual repository resolution.

Future UI may offer:

- Keep local
- Keep remote
- Open conflict editor

Do not build a complex merge editor before the core application works.

---

# 25. Concurrency Model

V0.1 assumes:

> One active backend application instance owns a notebook working tree.

Do not design for multiple simultaneous backend writers.

Do not add distributed locks.

Do not add CRDTs.

Do not add collaborative cursors.

Multiple browser tabs/users talking to the same backend may happen, but real-time collaborative editing is not a goal.

Prevent obvious accidental overwrites where practical.

A file version/hash check may be added later.

---

# 26. Search

Initial search should be simple.

Preferred V0.1 implementation:

- `ripgrep` if available,
- or a lightweight Go filesystem text search.

Search should cover:

- filenames,
- folder names,
- Markdown contents.

Return:

- notebook,
- relative path,
- matching line or excerpt.

Do not build Elasticsearch.

Do not build a vector search system.

Do not build semantic search in V0.1.

---

# 27. Authentication and Security

Alpha 2 introduces a deliberately small built-in authentication boundary for a
single-owner self-hosted instance. This is an explicit architectural decision
made after Alpha 1 demonstrated that interactive forward-auth layers can expire
or intercept browser/PWA API requests without RepoQuill being able to understand
or recover the authentication state reliably.

Supported authentication modes are:

```text
local     -> RepoQuill password with optional TOTP MFA; recommended default
disabled  -> explicit operator choice for LAN, VPN, or external protection
oidc      -> possible future direct integration, not required for Alpha 2
```

Local authentication must remain single-owner. Do not add registration,
usernames, email addresses, invitations, roles, organizations, email recovery,
or general account lifecycle. An internal fixed owner principal is allowed, but
must not become canonical note metadata.

Authentication metadata and sessions may use SQLite. They MUST NOT contain
canonical note content or the only copy of an asset. Losing or resetting auth
metadata must not make the Git-backed Markdown notebooks unreadable outside
RepoQuill.

The built-in boundary does not replace HTTPS. Internet-facing deployments must
terminate TLS at a correctly configured reverse proxy. Proxy-derived client IP
or scheme headers may be trusted only from explicitly configured proxy
addresses or networks.

`disabled` mode must require explicit configuration and produce a clear startup
and UI warning. It is suitable only when access is otherwise constrained. Do
not silently infer that an external authentication proxy is secure.

Future OIDC must be a direct RepoQuill integration so the application knows the
session state. MFA for OIDC remains the identity provider's responsibility.
Do not implement a custom OAuth/OIDC protocol, local MFA cryptography, or a
password/session algorithm where a focused, maintained library or standard
primitive exists.

---

# 28. Repository Credentials

Git credentials require careful handling.

Preferred approaches:

- SSH deploy keys,
- mounted SSH keys,
- SSH agent where practical,
- provider tokens supplied through environment variables or secrets,
- credential helper if explicitly configured.

Do not store plaintext tokens inside note repositories.

Do not commit secrets.

Do not expose credentials through frontend APIs.

Do not log secret tokens.

---

# 29. Path Security

All filesystem API calls must defend against path traversal.

Never trust user-supplied paths.

Reject attempts involving:

```text
../
..\
absolute paths
symlink escapes
```

All note operations must remain inside the configured notebook root.

Resolve and validate paths before filesystem operations.

Symlinks should either:

- be disabled,
- ignored,
- or explicitly constrained to remain inside the repository root.

Data safety and host security take priority over symlink flexibility.

---

# 30. API Shape

Prefer a boring REST API.

Suggested initial routes:

```text
GET    /api/health

GET    /api/notebooks
POST   /api/notebooks
GET    /api/notebooks/:id
DELETE /api/notebooks/:id

GET    /api/notebooks/:id/tree

GET    /api/notebooks/:id/files/*
PUT    /api/notebooks/:id/files/*
POST   /api/notebooks/:id/files/*
DELETE /api/notebooks/:id/files/*

POST   /api/notebooks/:id/move

POST   /api/notebooks/:id/assets

GET    /api/notebooks/:id/search?q=

GET    /api/notebooks/:id/git/status
POST   /api/notebooks/:id/git/sync
```

Route structure may evolve.

Do not introduce GraphQL without a specific demonstrated need.

---

# 31. Suggested Backend Modules

Keep backend boundaries explicit.

Suggested packages/services:

```text
internal/
├── app/
├── config/
├── notebooks/
├── files/
├── assets/
├── git/
├── search/
├── database/
└── httpapi/
```

Responsibilities:

## notebooks

- register notebook,
- clone repository,
- validate notebook configuration,
- resolve notebook root.

## files

- list tree,
- read Markdown,
- write Markdown,
- create,
- delete,
- move,
- rename.

## assets

- save clipboard images,
- determine note asset directory,
- validate media type,
- delete assets safely.

## git

- clone,
- status,
- commit,
- fetch,
- pull/rebase,
- push,
- sync state,
- conflict reporting.

## search

- query notebook content,
- format results.

Do not create abstractions for hypothetical future storage engines.

---

# 32. Suggested Frontend Structure

A possible structure:

```text
src/
├── app/
├── components/
│   ├── editor/
│   ├── file-tree/
│   ├── notebook-switcher/
│   ├── toolbar/
│   ├── search/
│   └── sync-status/
├── hooks/
├── api/
├── state/
├── pwa/
└── types/
```

Keep state management simple.

Use React state/context first.

Do not introduce Redux or another global state library unless complexity actually requires it.

---

# 33. Error Handling

Errors must be visible and understandable.

Never silently lose edits.

Important errors include:

- file save failure,
- repository unavailable,
- invalid path,
- Git authentication failure,
- remote unavailable,
- merge conflict,
- image upload failure,
- disk full,
- repository dirty/invalid state.

Prefer explicit user-facing states over silent retries.

Automatic retry is acceptable for transient failures where safe.

---

# 34. Logging

Backend logging should include:

- startup,
- notebook operations,
- file write failures,
- Git operation result,
- sync failures,
- conflict detection,
- malformed requests,
- security-related path rejection.

Do not log note contents by default.

Do not log credentials.

Do not log complete sensitive HTTP bodies.

---

# 35. Backup Philosophy

Git remote is a major durability mechanism but should not be described as the only possible backup.

The application itself does not own backup policy.

Because notebooks are normal Git repositories, users can additionally:

- clone them elsewhere,
- back up repository volumes,
- mirror repositories,
- use GitHub/GitLab backup workflows.

The application should never make backup harder.

---

# 36. Git Provider Independence

Core Git functionality must be provider-independent.

The following must work with any normal compatible Git remote:

- clone,
- pull,
- commit,
- push.

Provider-specific integrations belong in optional layers.

Future providers may include:

- GitHub,
- GitLab,
- Forgejo,
- Gitea.

Do not couple the core notebook model to GitHub-specific APIs.

---

# 37. Future Provider Integration

A later version may support:

```text
Create Notebook
    |
    +-- Existing Git Repository
    |
    +-- Create on GitHub
    |
    +-- Create on GitLab
    |
    +-- Create on Forgejo/Gitea
```

Provider integrations may create remote repositories through provider APIs.

This is NOT required for V0.1.

V0.1 should accept an existing Git remote URL.

---

# 38. Notebook Creation - V0.1

Initial workflow:

```text
Create notebook

Name:
[ Private ]

Repository:
[ git@github.com:user/private-notes.git ]

Branch:
[ main ]

[ Clone and Add ]
```

Backend:

1. validate input,
2. create notebook ID,
3. clone repository into `/data/notebooks/<id>`,
4. register metadata,
5. return notebook,
6. render repository tree.

Local-only repositories may be considered later.

---

# 39. UX Principles

The application should feel like a notes application, not an IDE.

Prefer:

- clean content area,
- minimal chrome,
- quick navigation,
- readable typography,
- obvious save/sync status,
- keyboard-friendly desktop use,
- touch-friendly mobile use.

Git concepts should stay mostly out of the primary workflow.

Avoid requiring users to think about:

- staging,
- index,
- detached HEAD,
- rebasing,
- refspecs.

Expose technical Git information only when needed for troubleshooting.

---

# 40. Accessibility

Use accessible components.

Requirements should include:

- keyboard navigation,
- visible focus states,
- semantic controls,
- ARIA labels where required,
- reasonable contrast,
- no essential hover-only actions.

Mobile and keyboard usability are both important.

---

# 41. Testing Strategy

Do not attempt perfect test coverage immediately.

Prioritize tests for destructive or security-sensitive behavior.

Backend tests should cover:

- path traversal prevention,
- note create/read/write/delete,
- move/rename,
- asset ownership,
- notebook root confinement,
- Git command argument handling,
- sync error handling.

Frontend tests should prioritize:

- core file-tree behavior,
- editor loading/saving,
- image insertion,
- mobile drawer behavior,
- save/sync status rendering.

End-to-end tests may be added after the primary workflow stabilizes.

---

# 42. Data Safety Rules

These rules override convenience:

1. Never silently overwrite known conflicting remote content.
2. Never delete assets unless ownership is sufficiently certain.
3. Never write outside a notebook repository.
4. Never store canonical notes only in application metadata.
5. Never discard unsaved editor contents because of a failed Git operation.
6. Git sync failure must not imply file save failure.
7. Deleting the application must not make notebooks unreadable.
8. Destructive operations should be deliberate.
9. Prefer recoverable behavior where practical.
10. Do not hide serious synchronization failures.

---

# 43. V0.1 Definition

V0.1 should be intentionally small.

A successful V0.1 allows a user to:

1. run the application in Docker,
2. connect an existing private Git repository,
3. browse folders and Markdown files,
4. create folders,
5. create notes,
6. open a note,
7. edit it in a live rendered Markdown editor,
8. format basic text,
9. paste a screenshot,
10. save changes,
11. search notes,
12. commit and push changes,
13. use the interface on desktop,
14. install/use it as a PWA on mobile.

That is enough for an alpha.

---

# 44. Recommended Development Milestones

## Milestone 0 - Repository Bootstrap

Create:

- Go backend,
- React/TypeScript/Vite frontend,
- Docker build,
- basic CI,
- basic README,
- this AGENTS.md.

Goal:

```text
docker compose up
```

shows the application.

---

## Milestone 1 - Filesystem Browser

Backend:

- open configured local repository,
- list directory tree,
- read Markdown file.

Frontend:

- display tree,
- open note,
- show note contents.

No editing required yet.

---

## Milestone 2 - WYSIWYG Markdown Editor

Add Milkdown.

Requirements:

- render Markdown while editing,
- load Markdown from backend,
- serialize back to Markdown,
- save file.

Test carefully that Markdown does not become unnecessarily reformatted or corrupted.

---

## Milestone 3 - File Operations

Implement:

- create note,
- create folder,
- rename,
- delete,
- move.

Ensure all path-security rules exist before exposing these operations.

---

## Milestone 4 - Image and Screenshot Paste

Implement:

- clipboard image detection,
- upload endpoint,
- per-note asset directory,
- Markdown insertion,
- immediate rendering,
- mobile image picker/upload.

This milestone makes the application genuinely useful.

---

## Milestone 4.1 - Core Editor Toolbar

Consolidate common Markdown editing actions into one compact toolbar while
keeping Markdown as the canonical representation.

Requirements:

- undo and redo,
- paragraph and Heading 1-6 selector with active block feedback,
- bold, italic, strikethrough, inline code, and code block,
- bullet, numbered, and task lists,
- blockquote, link, image, GFM table, and horizontal rule,
- active states that are accessible and not communicated through color alone,
- contextual image and table actions shown only for the selected object,
- a sticky desktop toolbar and a touch-friendly responsive mobile layout,
- no editor mutation while the note is in Read only mode,
- all changes flow through the existing autosave and status-bar behavior.

Image insertion must retain clipboard paste, file/mobile selection, per-note asset
directories, upload validation, secure serving, and portable relative paths.
Removing an image node must not automatically delete its asset.

Tables remain ordinary GFM tables. Do not add formulas, sorting, filtering,
typed cells, or database behavior.

Completion criteria:

- core formatting is available without typing Markdown syntax,
- toolbar and contextual controls serialize to portable Markdown,
- desktop, mobile, keyboard, and Read only behavior are tested,
- no proprietary document state or duplicate standalone image action is added.

---

## Milestone 4.2 - Unreferenced Asset Cleanup

Add a deliberate maintenance workflow for reviewing and deleting unused image
assets without weakening the conservative asset lifecycle.

Requirements:

- scan only supported images inside note-specific `.assets` directories,
- resolve ordinary relative Markdown references, including encoded and
  angle-bracket paths,
- treat ambiguous ownership or references as in use,
- exclude symlinks and anything outside the active notebook,
- show path and size for each candidate,
- require explicit selection and a second confirmation before deletion,
- revalidate every candidate immediately before deletion to prevent races,
- retain referenced, changed, unsupported, uncertain, or invalid files,
- remove only asset files selected by the user and empty resulting directories,
- report deleted, retained, and failed items clearly,
- keep cleanup separate from editor image removal and Git synchronization,
- support desktop and mobile/PWA interaction.

Cleanup produces ordinary Git-visible filesystem deletions but must never commit
or push automatically.

Completion criteria:

- referenced assets are not offered or deleted intentionally,
- uncertain files are retained,
- path traversal and symlink escapes are blocked,
- cleanup remains reviewable, explicit, and tested.

---

## Milestone 5 - Git Basics

Implement provider-independent synchronization using the system Git executable.

Requirements:

- clone and register an existing SSH repository,
- report clean, dirty, remote-change, failure, and conflict states,
- commit working-tree changes, fetch, rebase, and push without force,
- keep local file save separate from Git synchronization,
- preserve local files when authentication, network, rebase, or push fails,
- stop automatic synchronization on conflict,
- sanitize credentials and sensitive diagnostics,
- use direct process arguments and disable repository hooks,
- support managed per-notebook SSH keys and existing server SSH configuration,
- require explicit SSH host fingerprint approval,
- validate remote URLs, branches, key IDs, repository paths, and Git arguments.

Do not expose staging concepts or implement a merge editor for V0.1.

---

## Milestone 6 - Multiple Notebooks and Sync Triggers

A notebook remains exactly one Git repository with an independent working tree,
remote, branch, and credential association.

Requirements:

- persistent notebook registration and active-notebook selection,
- notebook switcher with Add and Manage workflows,
- switching completes any required local save before replacing editor state,
- Git failure must not trap navigation or discard saved content,
- configurable browser-local scheduled, inactivity, startup, focus,
  note-switch, notebook-switch, and best-effort tab-close sync triggers,
- serialize overlapping sync requests and avoid redundant recent clean syncs,
- run note-navigation Git work in the background,
- pull remote changes before normal work where configured,
- preserve and report conflicts instead of choosing local or remote content.

Browser-close synchronization is best effort and must never be described as a
backup guarantee.

---

## Milestone 7 - Search

Implement case-insensitive search in the active notebook across:

- folder names,
- Markdown filenames,
- Markdown content with line-aware excerpts,
- clickable results that open the matching note.

Exclude Git metadata, dependencies, note asset directories, symlinks,
non-Markdown files, and oversized files. Prefer a simple confined filesystem
search; do not add a search service, vector index, or semantic search.

---

## Milestone 8 - Responsive Online-First PWA

Finish the mobile and installable browser experience:

- notebook tree in a touch-friendly drawer,
- responsive main and contextual editor toolbars,
- practical touch targets and software-keyboard behavior,
- PWA manifest, responsive icon, service worker, install flow, and standalone
  display mode,
- reliable frontend-shell loading,
- explicit offline/backend-unavailable messaging,
- mobile image picker and camera/gallery support.

The service worker may cache only the application shell. Do not cache note
contents or API responses and do not implement offline editing or sync queues.

---

## Milestone 9 - Editor Productivity

Improve editing speed by extending the existing Milkdown/ProseMirror commands
rather than creating parallel formatting logic.

Requirements:

- a keyboard- and touch-accessible slash-command picker,
- filtering, Arrow Up/Down, Enter, and Escape behavior,
- commands for headings, lists, tasks, quotes, inline code, code blocks, links,
  images, tables, and horizontal rules,
- slash commands and toolbar actions invoke equivalent editor transformations,
- conventional Enter and Shift+Enter paragraph/line-break behavior,
- predictable code-block line creation and an explicit multi-Enter exit,
- inline-code typing that can be entered and exited at an empty cursor,
- contextual table and image controls remain visible while their object is
  selected,
- every result serializes as ordinary CommonMark/GFM Markdown,
- Read only mode prevents all mutations.

Potential snippets or additional shortcuts must expand to ordinary Markdown and
must not execute code. Do not add collaboration, CRDTs, AI writing, arbitrary
scripting, a plugin runtime, proprietary syntax, or Notion-style blocks.

Completion principle:

> Markdown is storage. The editor is convenience.

---

## Milestone 10 - Alpha Hardening

Prepare an auditable alpha without claiming that all vulnerabilities are
eliminated.

Requirements:

- enforce notebook path confinement and symlink rejection,
- validate Git remotes, branches, request bodies, and upload types/sizes,
- apply same-origin mutation protection and defensive HTTP security headers,
- set server limits and timeouts,
- disable repository Git hooks for automated operations,
- preserve notes through save, Git, network, and conflict failures,
- harden the non-root container and persistent-volume defaults,
- exclude runtime data and credentials from Git and Docker build contexts,
- run backend, frontend, race, build, and dependency-audit checks,
- document security boundaries, recovery, persistence, and alpha limitations,
- keep application, package, binary, changelog, and image versions consistent.

The Alpha 1 application still requires an external HTTPS authentication layer
and must not be exposed directly to the public Internet. Milestone 19 replaces
this Alpha 1 limitation with the Alpha 2 single-owner authentication boundary;
HTTPS remains required for Internet-facing deployment.

---

## Milestone 11 - Public Alpha Release and Container Publishing

Prepare and publish RepoQuill as a public alpha release in small, auditable
steps. Public repository creation, pushes, tags, releases, package publication,
and changes to external services require explicit user authorization before they
are performed.

### Phase 1 - Public repository audit

Before the first public push:

- inspect every tracked file and the complete Git history for credentials,
  private SSH keys, access tokens, personal paths, private repository URLs,
  personal notes, screenshots, email addresses, and other unintended data,
- confirm runtime directories such as `.repoquill-data/`, `data/`, generated
  frontend output, local environment files, and editor state are ignored,
- confirm runtime data and secrets are excluded from the Docker build context,
- run an automated secret scan against both the working tree and Git history,
- remove secrets from history rather than only deleting them in the latest
  commit, and rotate any credential that may already have been exposed,
- verify that example configuration and demo content contain only deliberate,
  publishable placeholder data,
- review repository links, module paths, image labels, and container metadata
  for the intended public repository owner and name.

Do not publish while a suspected credential or private document remains
unresolved.

### Phase 2 - Project and community metadata

Prepare the files expected in a public repository:

- select and add an explicit open-source `LICENSE`; do not assume a license on
  the user's behalf,
- ensure `README.md` documents the Alpha 1 status, supported deployment model,
  installation, persistent volumes, upgrades, backup/recovery, and the warning
  not to expose that release without HTTPS and external authentication,
- keep `SECURITY.md` current with supported versions and a private vulnerability
  reporting path,
- add contribution and code-of-conduct documents if public contributions are
  intended,
- ensure `CHANGELOG.md` contains the alpha version and release date,
- document known alpha limitations and deferred features,
- verify package, binary, manifest, OCI image, and displayed application
  versions consistently use the chosen release version.

### Phase 3 - Reproducible release gates

The release workflow must fail closed unless all required checks pass:

- install dependencies from lock files using reproducible commands,
- run Go tests, security-sensitive tests, `go vet`, and the race detector where
  practical,
- run frontend linting, unit/component tests, TypeScript compilation, and the
  production PWA build,
- audit Go and npm dependencies using maintained vulnerability scanners,
- scan the repository and produced image for secrets and known vulnerabilities,
- build the Docker image from a clean checkout,
- smoke-test the built image through its health endpoint,
- verify the container runs as a non-root user with the documented hardened
  Compose configuration,
- verify notebook data survives container replacement and remains ordinary,
  independently usable Markdown and asset files,
- retain enough build output or attestations to diagnose a failed release.

Pin third-party GitHub Actions to immutable commit SHAs for the publishing
workflow. Keep workflow permissions read-only by default and grant package write
access only to the publishing job. Never expose repository secrets to workflows
triggered by untrusted pull requests.

### Phase 4 - GitHub Actions container publishing

Add a dedicated release workflow that:

- runs only for an explicit release tag such as `v0.1.0-alpha.1` or by a clearly
  controlled manual dispatch,
- validates that the tag, changelog, package version, binary version, manifest,
  and image metadata agree,
- builds one multi-architecture image manifest for `linux/amd64` and
  `linux/arm64`, covering normal Intel/AMD hosts and 64-bit ARM systems such as
  current ARM servers, Apple Silicon Docker environments, and Raspberry Pi 4/5
  installations using a 64-bit operating system,
- does not claim support for `linux/arm/v7`, `arm/v6`, or other 32-bit targets
  until the complete dependency, build, scan, and runtime test chain succeeds
  for each added architecture,
- authenticates to GitHub Container Registry using the short-lived
  `GITHUB_TOKEN`, not a committed personal token,
- publishes to `ghcr.io/<owner>/repoquill`,
- applies immutable version tags such as `0.1.0-alpha.1` and the matching Git
  SHA so a deployment can be reproduced or rolled back exactly,
- applies the moving `0.1.0-alpha` convenience tag only after the matching
  immutable image and all architecture builds have succeeded; this tag points
  to the newest published alpha in the `0.1.0` line and is intended for users
  who deliberately prefer easier alpha updates over exact pinning,
- does not introduce a broader moving `alpha` tag initially because it could
  later cross minor-version compatibility boundaries,
- does not publish `latest` while the project is alpha unless explicitly
  requested,
- generates an SBOM and provenance attestation where supported,
- signs or attests published images using GitHub's OIDC-based mechanism where
  practical,
- publishes nothing if tests, scans, the image build, or smoke tests fail.

Avoid unnecessary registry credentials. The minimum expected workflow
permissions are:

```yaml
permissions:
  contents: read
  packages: write
  id-token: write # only when provenance/signing requires it
```

### Phase 5 - First public alpha

After explicit approval:

1. create or confirm the public GitHub repository,
2. configure the intended default branch and basic branch protection,
3. push the reviewed source history,
4. confirm ordinary branch CI succeeds in GitHub,
5. create and push the signed or annotated alpha tag,
6. let the release workflow build and publish the image,
7. verify the GHCR package visibility and pull the image by its immutable tag,
8. perform a fresh-install smoke test using empty persistent storage,
9. perform an upgrade/restart test using non-empty test notebook storage,
10. create a GitHub prerelease with changelog-derived notes, installation
    commands, known limitations, and rollback instructions.

Do not rewrite or move an already published immutable version tag. A failed
release must receive a new version.

### Phase 6 - Post-release verification

After publication:

- verify the documented `docker compose` flow using the published image,
- confirm health, notebook onboarding, editing, image handling, search, Git
  synchronization, PWA installation, restart persistence, and recovery,
- confirm the public image contains no source credentials, private keys, local
  runtime data, development dependencies, or unintended files,
- record the image digest in the release notes,
- verify vulnerability reporting and dependency update automation,
- create issues for accepted alpha limitations rather than hiding them,
- document how to roll back to the previous immutable image tag,
- monitor initial CI, package, and security reports before promoting another
  alpha tag.

### Milestone 11 completion criteria

Milestone 11 is complete when:

- the public repository contains only deliberately publishable content,
- license, security, installation, recovery, and alpha-status documentation are
  present,
- branch CI and release gates pass from a clean checkout,
- `v0.1.0-alpha.1` or its explicitly chosen successor is published as a GitHub
  prerelease,
- matching multi-architecture images are available from GHCR by immutable
  version and digest for `linux/amd64` and `linux/arm64`,
- the `0.1.0-alpha` convenience tag resolves to the same multi-architecture
  manifest as the newest successful immutable `0.1.0-alpha.N` release,
- a fresh deployment and a persistence/upgrade test have succeeded,
- no credentials are stored in Git, workflow files, release artifacts, or the
  container image,
- the Alpha 1 release notes clearly state that the release has no built-in
  authentication and must be deployed behind an appropriate HTTPS
  authentication layer.
- a first-time GitHub user can create or identify a private repository, obtain
  the correct SSH address, add RepoQuill's dedicated public key with write
  access, verify the Git host, and finish connection by following RepoQuill's
  own guidance,
- inserted images can be inspected in a full-size/lightbox view without adding
  non-standard sizing metadata, modifying the original asset, or mutating
  Markdown,
- release-facing documentation accurately reflects the shipped Alpha 2
  authentication, guided conflict handling, onboarding, image behavior,
  recovery, storage, and synchronization model,
- the final dependency and toolchain baseline has been deliberately reviewed,
  updated where justified, and documented where a maintained older release line
  is retained,
- all thirteen Alpha 2 milestones work on desktop and mobile/PWA layouts where
  a user interface is applicable,
- Milestones 19, 20, and 24's dedicated security verification and release gates
  pass,
- no Alpha 2 milestone introduces opaque canonical content, a mandatory cloud
  dependency, or weakens Git/provider independence.

---

# 45. Alpha 2 Milestones

Alpha 2 should close the remaining trust and everyday-navigation gaps without
expanding RepoQuill into a general-purpose productivity platform. These
milestones must continue to treat ordinary Markdown files, folders, assets, and
Git history as the canonical data model.

Milestones 16, 17, 19, 20, 21, 23, and 24 are the highest-priority Alpha 2
milestones. Conflict handling, understandable synchronization, the
authentication boundary, continuous vulnerability management, beginner-friendly
GitHub notebook onboarding, release-documentation accuracy, and the final
dependency baseline are release-quality concerns rather than optional polish.

Milestones 19, 20, and 24 are security/release-gate blockers.
Milestones 21 and 23 are usability/documentation release blockers.
Milestone 22 is intentionally limited to a low-risk portable image-viewing
improvement and must not expand into persisted image sizing or an image-editing
subsystem.

## Milestone 12 - Recoverable Deletion and Trash

Replace immediate destructive deletion in normal notebook workflows with a
recoverable notebook-local trash mechanism.

Implementation status: completed on the Alpha 2 development branch. Normal
deletion now moves notes, folders, and owned assets into a confined
notebook-local Trash with explicit restore and permanent-delete workflows.

Requirements:

- move deleted notes and folders into a notebook-local `.trash` area by
  default,
- move a note's owned `.assets` directory together with the note,
- provide a clear Trash view with Restore and permanently Delete actions,
- provide recovery directly in RepoQuill and never use `recover it with Git`
  as the only normal-user recovery instruction,
- retain enough ordinary filesystem information to restore an item to its
  original location where safe,
- handle name collisions explicitly and never overwrite an existing restored
  target silently,
- require confirmation before permanent deletion,
- preserve notebook-root confinement and reject symlink or traversal escapes,
- keep trash operations visible to Git as ordinary filesystem changes,
- exclude trashed content from the normal tree, search, editor links, and
  unreferenced-asset cleanup.

Do not implement a proprietary content store for deleted notes. Recovery must
remain understandable at the filesystem and Git level.

Completion criteria:

- accidental deletion is recoverable from desktop and mobile,
- notes, folders, and note-owned assets restore together safely,
- permanent deletion is deliberate, confirmed, confined, and tested.

## Milestone 13 - Note Version History and Restore

Expose the history Git already provides in a note-oriented interface.

Implementation status: completed on the Alpha 2 development branch. The active
note can now show its provider-independent Git history, readable historical
content and differences, and restore a selected version as a new version-checked
working-tree change.

Requirements:

- show commits that affected the active Markdown note,
- display timestamp and a useful commit summary,
- allow viewing a historical note version and a readable diff,
- allow restoring an earlier version without resetting or rewriting repository
  history,
- expose restoration as a direct note action rather than requiring the user to
  identify or operate on a Git commit,
- save a restoration as a new ordinary working-tree change that can be synced
  normally,
- preserve unsaved editor content and never replace it without explicit user
  confirmation,
- handle renamed or moved notes where Git can identify the history reliably,
- report unavailable, shallow, missing, or invalid history clearly,
- do not expose arbitrary revisions, paths, or Git arguments to command
  execution.

The first version does not need a repository-wide Git client or commit browser.

Completion criteria:

- a user can inspect and safely restore a previous version of a note,
- restoration never performs a destructive reset or silent overwrite,
- history remains ordinary provider-independent Git history.

## Milestone 14 - Portable Internal Note Links

Make links between notes convenient while retaining standard Markdown
portability.

Implementation status: completed on the Alpha 2 development branch. Internal
links remain ordinary relative Markdown links; RepoQuill provides a searchable
picker and editor suggestions, opens links in the current or a new note tab,
marks missing targets, and requires an exact review token before rename/move
rewrites are applied. Link discovery and rewriting stay notebook-confined and
conservatively leave images, code, external URLs, anchors, and unsupported
syntax untouched.

Requirements:

- provide a searchable note picker from the existing link action,
- offer note suggestions when typing an internal-link trigger where practical,
- serialize new internal links as ordinary relative Markdown links rather than
  opaque application IDs,
- open internal note links inside RepoQuill,
- support opening a link in a new note tab with the existing desktop and touch
  interaction patterns,
- detect broken or missing internal-link targets without corrupting Markdown,
- safely update affected relative links when notes or folders are renamed or
  moved,
- preview every ambiguous or broad link rewrite and never guess between
  multiple possible targets,
- preserve external URLs, anchors, images, encoded paths, angle-bracket paths,
  and unsupported Markdown unchanged,
- keep link resolution confined to the active notebook.

Do not add backlinks, graph view, block references, transclusion, proprietary
link IDs, or a canonical link database in this milestone.

Completion criteria:

- users can create and follow links without manually typing paths,
- links remain useful in ordinary Markdown editors and Git forges,
- rename and move operations do not silently leave known links broken.

## Milestone 15 - Complete Notebook Management

Implementation status: completed on the Alpha 2 development branch. Notebook
registrations can be renamed or safely removed, optional local working-copy
deletion requires the exact notebook name, and compact actionable health checks
keep remote and technical details out of the normal management view.

Turn the existing Manage Notebooks overview into a small, safe management
surface.

Requirements:

- retain the current notebook details and active-state overview,
- allow changing the human-readable notebook display name,
- allow removing an inactive notebook registration from RepoQuill,
- clearly distinguish removing registration from deleting a local working
  tree,
- make registration-only removal the safe default and never affect the remote
  repository,
- require stronger explicit confirmation before any optional local working-tree
  deletion,
- refuse removal while a required save or notebook operation is active,
- close or transfer affected UI state safely when a notebook is removed,
- show branch, remote, credential association, and repository health in a
  troubleshooting-oriented but understandable form,
- show a compact health summary for local files, remote connectivity, write
  access, pending work, and the last successful synchronization,
- turn each failed health check into a specific next action such as `Retry`,
  `Check connection`, or `Repair access`,
- keep branch names, repository paths, and raw diagnostics behind optional
  technical details unless the user is editing connection settings,
- preserve the existing Add Notebook, switching, Git/SSH onboarding, and sync
  behavior.

Editing a remote URL or branch in place is not required if safely recloning the
notebook is the clearer model.

Completion criteria:

- Manage Notebooks provides meaningful management rather than only an overview,
- each notebook has an understandable health state and actionable repair path,
- registration removal cannot accidentally delete notebook content,
- all destructive options communicate their exact local and remote impact.

## Milestone 16 - Guided Conflict Resolution

Provide a safe conflict-resolution workflow that is understandable without Git
knowledge. Git remains the underlying mechanism, but the primary UI must speak
only in terms of the user's version, the other version, combined content, and
the resulting note.

Implementation status: completed on the Alpha 2 development branch. Both
optimistic save collisions and remote synchronization conflicts now use the
same plain-language review model. Source versions remain in Git, reviewed
decisions are revalidated against the remote, index, and working tree, and a
durable recovery ref is created before accepted content is applied. Markdown,
modify/delete, binary image, keep-both, and overlapping rename cases are handled
without force pushes, destructive resets, or exposing conflict markers to the
editor.

This milestone covers both conflicts detected by the optimistic file-version
check during save and conflicts produced while integrating remote Git changes.
The two technical sources should use one consistent user-facing mental model:

> Two versions overlap. RepoQuill has preserved both and needs the user to
> choose the resulting content.

Requirements:

- continue resolving unambiguous changes automatically and open the assistant
  only for changes that require a decision,
- stop automatic synchronization while any conflict remains unresolved,
- preserve unsaved editor content, committed local content, and incoming
  content before presenting resolution actions,
- create a durable, identifiable Git safety point before applying a completed
  Git conflict resolution,
- show a notebook-level overview of every affected note, folder, and asset,
- use plain labels such as `Your version`, `Other version`, `Keep note`, and
  `Confirm deletion`; do not expose `ours`, `theirs`, index stages, rebase, or
  conflict-marker terminology in the primary workflow,
- allow the user to postpone resolution without losing either version,
- allow inspecting both complete Markdown versions and a readable highlighted
  text diff,
- provide `Use your version`, `Use other version`, and `Edit combined version`
  actions for Markdown conflicts,
- use the normal RepoQuill editor for combined content where safe, while
  keeping unresolved markers and Git conflict syntax out of the editor,
- handle modify/delete conflicts with explicit `Keep note` and `Confirm
  deletion` decisions, defaulting to the non-destructive choice,
- handle rename and move conflicts with human-readable paths and the existing
  safe folder picker,
- update note-owned assets and portable internal links consistently after a
  chosen rename or move,
- show conflicting images side by side where the browser supports preview and
  offer `Use your image`, `Use other image`, and `Keep both`,
- give retained duplicate assets new collision-resistant names instead of
  overwriting either file,
- collect and review all decisions before mutating the working tree or
  continuing synchronization,
- revalidate the remote state and every affected path immediately before
  applying decisions,
- stop safely and request a new review if the remote or working-tree state
  changed during resolution,
- continue the existing Git operation and push automatically only after every
  conflict has a valid decision,
- report a clear success state and make the most recent resolution reversible
  through its safety point where practical,
- keep technical Git diagnostics available behind optional details for support
  without making them part of the normal decision flow,
- support keyboard, touch, narrow mobile/PWA layouts, and accessible labels,
- never use force push, destructive reset, silent deletion, or automatic
  preference for local or incoming content.

Implementation safety rules:

- never parse rendered editor HTML as the source for conflict resolution,
- obtain historical conflict inputs through validated direct Git arguments and
  confined notebook-relative paths,
- do not expose arbitrary revisions, ref names, paths, or Git arguments through
  frontend input,
- serialize conflict-application operations per notebook,
- do not allow ordinary automatic sync to resume while the repository still
  contains unresolved entries or an incomplete Git operation,
- preserve recovery information if the browser closes, the backend restarts,
  the network fails, or applying a resolution is interrupted,
- treat binary files as choose-one-or-keep-both decisions rather than attempting
  a textual merge,
- never write conflict markers into a note as an accepted resolution unless
  the user deliberately typed those characters as normal content.

Completion criteria:

- a user with no Git knowledge can understand why synchronization paused and
  complete every supported resolution through RepoQuill,
- ordinary text, modify/delete, rename/move, and image conflict variants have
  explicit safe workflows,
- both original versions remain recoverable until the completed resolution is
  safely recorded,
- closing or refreshing the browser does not discard pending conflict choices
  or the source versions,
- a second remote update during resolution cannot cause stale decisions to be
  applied silently,
- focused backend, frontend, integration, and interruption-recovery tests cover
  the conflict lifecycle,
- the resulting notebook remains an ordinary readable Git repository without
  proprietary conflict metadata in canonical note content.

## Milestone 17 - Git-Invisible Save and Synchronization UX

Make everyday save, synchronization, remote-change, and failure states
understandable to users who have no Git knowledge. Git remains visible only in
notebook connection settings, optional diagnostics, and other deliberately
technical views.

Implementation status: completed on the Alpha 2 development branch. The compact
status bar now opens a human-readable synchronization panel, successful external
changes are summarized without replacing the active note, and technical Git
details remain optional.

Requirements:

- clearly distinguish `saved on this RepoQuill server` from `synchronized with
  the connected service` without implying that a local save already exists on
  the remote,
- keep the compact document status bar, but use human-readable primary labels
  and accessible explanations,
- replace primary-interface states such as `Clean`, `Local changes`, `Remote
  changes`, `Diverged`, `Dirty working tree`, `Upstream`, and `Rebase` with
  phrases such as `Everything is up to date`, `Changes waiting to sync`, `New
  changes available`, and `Changes need your review`,
- provide a central synchronization details panel reachable from the status
  bar,
- show local-save state, connection state, pending synchronization state, last
  successful synchronization, next scheduled attempt where applicable, and a
  clear `Sync now` action,
- expose raw branch, ahead/behind counts, operation names, sanitized command
  diagnostics, and other Git terminology only under optional technical details,
- structure every important error around three questions: what happened,
  whether the user's note content is safe, and what the user can do next,
- never show raw Git stderr as the only user-facing explanation,
- provide specific safe actions such as `Retry`, `Check connection`, `Review
  changes`, and `Open notebook settings`,
- state explicitly when changes remain saved locally but have not reached the
  remote service,
- announce successfully integrated external changes in a non-blocking summary,
  including added, updated, moved, and deleted notes where known,
- allow opening an affected note from that summary without interrupting the
  current note unexpectedly,
- route overlapping external changes into Milestone 16 rather than describing
  them as a technical synchronization failure,
- use notebook, note, folder, saved, and synchronized terminology throughout
  the primary note-taking UI,
- reserve repository, remote, branch, SSH, commit, fetch, rebase, and push for
  connection setup, diagnostics, and advanced details,
- reorganize Settings into understandable sections such as General, Notebooks,
  Storage and recovery, and Advanced,
- place SSH keys, host trust, Git diagnostics, and other setup internals in the
  Advanced or notebook-connection area after successful onboarding,
- retain accessible text for every state and never communicate save or sync
  safety through color or icons alone,
- keep all existing synchronization scheduling, serialization, conflict
  protection, provider independence, and local-save behavior unchanged unless a
  safety correction is required.

Suggested normal-user states:

```text
Saved on this server
Waiting to synchronize
Synchronizing…
Everything is up to date
New changes were received
Synchronization could not finish
Your decision is required
Notebook is currently unavailable
```

Completion criteria:

- a user can tell whether edits are saved locally and whether they reached the
  connected service without understanding Git,
- a synchronization failure explains that saved notes remain safe whenever
  that is true and offers a concrete next step,
- no primary workflow requires interpreting `clean`, `dirty`, `diverged`,
  `ahead`, `behind`, `rebase`, or raw Git output,
- external changes are visible and understandable instead of appearing as
  unexplained content changes,
- the synchronization panel and reorganized settings work on desktop and
  mobile/PWA layouts.

## Milestone 18 - Guided Notebook Connection Onboarding

Implementation status: completed on the Alpha 2 development branch. The
provider-independent clone flow is presented as a five-step assistant with
provider-specific examples, retained progress, guided managed SSH keys, explicit
host trust, actionable connection failures, and a final review.

Make connecting an existing Git-backed notebook achievable without prior Git or
SSH knowledge while keeping the underlying synchronization provider-independent.

Requirements:

- present a step-by-step connection assistant rather than one dense technical
  form,
- begin with approachable choices such as GitHub, GitLab, Forgejo/Gitea, and
  another Git server,
- treat provider choices as instructional presets only; cloning, fetching, and
  pushing must continue to use normal provider-independent Git,
- explain that the remote repository must already exist and that RepoQuill does
  not create it through a provider API in Alpha 2,
- provide provider-appropriate SSH URL examples and detect common HTTPS, SSH,
  malformed, pasted-link, and escaped-address mistakes,
- infer the host and default branch where it can be discovered safely instead
  of requiring unnecessary input,
- guide managed-key creation, public-key copying, provider-side deploy-key
  placement, write-access selection, and connection testing one step at a time,
- preserve the existing explicit SSH host fingerprint approval and explain in
  plain language why the host must be trusted,
- translate authentication, host trust, missing repository, missing branch,
  read-only key, network, and clone failures into distinct actionable messages,
- provide direct actions such as `Copy public key`, `Open setup instructions`,
  `Test again`, and `Back`,
- tell the user which completed onboarding steps are safe and retained after a
  failure,
- allow advanced users to choose existing server SSH configuration without
  making that option the unexplained default,
- show a final review containing notebook name, service/host, repository
  address, branch, and credential choice before cloning,
- after success, open the notebook and show the health summary from Milestone
  15 plus the synchronization explanation from Milestone 17,
- keep credentials out of frontend responses, logs, repository content, and
  browser persistence,
- remain usable by keyboard, touch, mobile/PWA, and assistive technology.

Completion criteria:

- a first-time GitHub user can connect a private repository by following only
  the instructions shown in RepoQuill,
- common connection failures identify the failed step and a concrete repair
  action instead of returning a generic connection-test error,
- an advanced provider-independent SSH workflow remains available,
- no provider API, mandatory cloud integration, or proprietary notebook format
  is introduced.

## Milestone 19 - Single-Owner Authentication and Optional TOTP MFA

Implement a small built-in authentication boundary designed for one owner of a
self-hosted RepoQuill instance. The goal is reliable browser and PWA behavior
without depending on an interactive forward-auth proxy. This is a security
milestone and must be developed in the phases below; later phases must not begin
by weakening incomplete safeguards from an earlier phase.

The supported Alpha 2 modes are:

```text
local     -> built-in password and optional TOTP; recommended
disabled  -> explicit operator choice for a separately protected deployment
```

Direct OIDC is deferred. Do not add registration, usernames, multiple users,
roles, invitations, email addresses, or email-based recovery.

### Phase 1 - Architecture and persistent security metadata

- record the authentication decision, threat model, trust boundaries, public
  routes, protected routes, proxy assumptions, and recovery model before
  implementation,
- introduce a dedicated internal authentication service rather than scattering
  password and session checks through HTTP handlers,
- store only authentication configuration, sessions, one-time recovery data,
  throttling state where needed, and security events in an application metadata
  database such as SQLite,
- keep canonical Markdown, assets, Git repositories, SSH private keys, and Git
  credentials outside authentication tables,
- use a fixed internal owner principal without exposing unnecessary username or
  account management concepts,
- make schema migrations atomic, versioned, restart-safe, and backed up before
  destructive changes,
- define `local` and `disabled` configuration semantics and a safe migration
  path for existing Alpha 1 deployments,
- never silently enable unauthenticated public access during migration or after
  damaged/missing authentication metadata.

### Phase 2 - Secure first-run setup and local password

- make fresh local-auth installations start in a restricted `setup required`
  state,
- prevent first-visitor instance takeover with a cryptographically random,
  short-lived, one-time bootstrap token supplied through an operator-controlled
  channel such as a Docker secret, a root-readable file on the persistent
  volume, or an operator-only CLI command; log only retrieval instructions and
  never the token value,
- invalidate the bootstrap token permanently after successful setup and reject
  every repeated setup attempt,
- use Argon2id from a maintained cryptographic library with a unique random
  salt and stored versioned cost parameters,
- select memory and CPU parameters suitable for the minimum supported container
  size and benchmark them in CI or a documented release process,
- support transparent parameter upgrades after a later successful login,
- compare derived credentials in constant time and return uniform failure
  messages and materially similar failure timing,
- allow long passphrases, whitespace, Unicode, paste, and password managers;
  do not impose arbitrary composition or periodic-change rules,
- reject unreasonably short and excessively large password inputs before
  expensive hashing to prevent weak credentials and resource exhaustion,
- never log passwords, derived hashes, setup tokens, request bodies, or other
  authentication secrets.

### Phase 3 - Server-side sessions and API boundary

- use a maintained server-side session library and cryptographically random
  opaque tokens rather than inventing a token or JWT format,
- persist production sessions so a normal container restart does not force an
  unexplained logout,
- store only the opaque session identifier in a cookie and keep authentication
  state on the server,
- set `HttpOnly`, `Secure`, an appropriate `SameSite` value, and a confined
  cookie path in production,
- never store session IDs, refresh credentials, password material, or MFA
  secrets in `localStorage`, `sessionStorage`, IndexedDB, URLs, or frontend
  application state,
- renew the session identifier after password and MFA authentication and every
  authentication-level change to prevent fixation,
- implement idle and absolute expiration plus an explicit `Remember this
  device` duration,
- provide login, logout, authentication status, current-session revocation, and
  all-session revocation endpoints with stable JSON responses,
- protect every notebook, note, asset, search, Git, SSH, maintenance, and
  management API through deny-by-default middleware,
- keep only the minimum setup, login, static shell, and non-sensitive liveness
  surface public,
- return a stable JSON `401` for expired or missing sessions rather than HTML or
  redirects from API routes,
- do not describe authentication expiration as a Git synchronization failure.

### Phase 4 - CSRF, request origin, throttling, and proxy trust

- bind a CSRF token to the authenticated session and require it for every
  state-changing browser request,
- retain and strengthen same-origin validation; never rely on `SameSite`
  cookies as the only CSRF defense,
- ensure GET and HEAD routes have no state-changing side effects,
- apply progressive login delays and rate limits per trustworthy client address
  plus a global limit suitable for a single-owner account,
- avoid a permanent attacker-triggerable account lockout that could deny the
  owner access indefinitely,
- log authentication success, failure, throttling, setup, password change, MFA
  change, session revocation, and recovery without logging credentials or note
  content,
- accept `X-Forwarded-For`, `X-Real-IP`, and `X-Forwarded-Proto` only from
  explicitly configured trusted proxy IPs or CIDRs,
- ignore or replace spoofed forwarding headers from all other clients,
- document TLS termination and trusted-proxy configuration for common Docker
  reverse-proxy deployments,
- keep the backend unreachable except through the intended proxy where the
  deployment exposes it to the Internet.

### Phase 5 - Browser and PWA authentication lifecycle

- show first-run setup and login as first-class responsive RepoQuill screens,
- handle an expired session inside the SPA/PWA without relying on an external
  redirect or hard refresh,
- distinguish authentication required, backend unavailable, browser offline,
  frontend update required, local save failure, and Git sync failure,
- stop editing and automatic Git triggers safely when authentication expires,
- preserve an unsaved editor draft before any logout, reload, or reauthentication
  flow could discard it,
- treat a browser recovery draft as temporary data-loss protection, not offline
  editing or a canonical sync queue,
- associate recovery drafts with notebook ID, note path, and loaded file version
  and never overwrite changed server content silently,
- reconcile a recovery draft after login through the normal version check and
  route overlapping content to Milestone 16,
- remove a recovery draft only after the note is saved successfully or the user
  deliberately discards it,
- retry authentication status on browser `online`, focus, and visibility
  changes without creating request loops,
- keep the service worker limited to the application shell and never cache auth
  APIs, API responses, sessions, notes, or credentials,
- show the running RepoQuill version unobtrusively at the bottom of Settings,
  using the backend-reported build version as the source of truth and displaying
  `dev` for unversioned local builds,
- optionally show a shortened build/commit identifier when it is supplied by
  the release build, without exposing host or repository metadata; a version
  link may point to the matching public GitHub release,
- keep version information secondary to application and security state, but
  make it selectable and accessible so users can include it in diagnostics,
- verify that a stale frontend and newer backend can be distinguished without
  misreporting the condition as an authentication, save, or Git sync failure,
- support multiple tabs, installed PWA windows, mobile browsers, password
  managers, keyboard navigation, and assistive technology.

### Phase 6 - Password and session administration

- add a Security settings section for changing the password, choosing session
  durations within safe bounds, viewing active sessions, revoking another
  session, revoking all other sessions, and logging out,
- require recent password authentication and, when enabled, MFA for password or
  MFA changes and other sensitive authentication operations,
- invalidate all other sessions after a password change by default,
- identify sessions with creation time, last activity, approximate client/device
  description, and revocation state without invasive fingerprinting,
- provide an operator-only CLI recovery command through the RepoQuill binary for
  forgotten-password recovery,
- require filesystem/container administrative access for CLI recovery,
- make password reset revoke all sessions and make MFA reset a separate explicit
  decision,
- never alter, encrypt, delete, or relocate notebook contents during auth
  recovery,
- document that Git-backed notes remain independently recoverable even if all
  RepoQuill authentication metadata is lost.

### Phase 7 - Optional TOTP MFA and recovery codes

- use a maintained TOTP implementation rather than implementing the algorithm,
- require recent password verification before beginning enrollment,
- generate the TOTP secret with a cryptographically secure random source and
  render the enrollment QR code locally without an external service,
- keep enrollment pending and MFA disabled until the user successfully verifies
  a generated code,
- encrypt the stored TOTP secret with an application key kept outside the
  metadata database and never return it after enrollment completes,
- use a small documented clock-skew window and strict attempt throttling,
- accept password first and then TOTP or a one-time recovery code without
  revealing which credential failed,
- generate high-entropy one-time recovery codes, display them once, store only
  hashes, and consume each code atomically,
- require the user to confirm recovery-code storage before completing MFA
  activation,
- require recent password plus TOTP or a recovery code to disable or replace
  MFA,
- provide regeneration of recovery codes that invalidates every previous code,
- never log TOTP secrets, QR payloads, submitted codes, or recovery codes,
- do not claim that TOTP protects a stolen authenticated session; retain session
  revocation and secure-cookie controls.

### Phase 8 - Disabled mode and deployment migration

- require an explicit `disabled` setting; do not infer it from forwarding
  headers or an accessible authentication portal,
- show a persistent security warning in Settings and startup logs while auth is
  disabled,
- document disabled mode for localhost, private LAN, VPN, Tailscale, or a
  deliberately configured external authentication layer,
- warn that interactive forward-auth can still expire independently and return
  HTML or redirects to API clients,
- ensure switching authentication modes cannot accidentally leave active local
  sessions or setup tokens valid,
- define a safe upgrade flow for existing Alpha 1 installations without silently
  exposing them or irreversibly locking out the operator,
- keep HTTPS and network isolation guidance applicable in every mode.

### Phase 9 - Security verification and release gate

Implementation status: completed on the Alpha 2 development branch. The exact
release candidate must still pass the full CI, scan, container, and manual
preflight gate before an Alpha 2 tag may be published.

The post-review cleanup additionally applies persistent progressive throttling
to sensitive password and MFA checks, bounds and coalesces security-event
storage, binds expiring MFA enrollment to the initiating session, prevents
expired-session resurrection during activity updates, fails closed on
authentication-state read errors, raises the new-password minimum to 15
characters with basic weak-value rejection, and quarantines a malformed MFA key
only during an explicit recovery operation.

- add focused unit, integration, frontend, and end-to-end tests for setup,
  login, logout, expiration, remember-me, revocation, restart persistence,
  password changes, recovery, and PWA reauthentication,
- test setup takeover prevention, session fixation, session theft impact,
  timing-sensitive comparisons, CSRF, Origin validation, brute-force
  throttling, permanent-lockout resistance, proxy-header spoofing, and malformed
  input/resource exhaustion,
- test TOTP enrollment, clock windows, repeated-code handling, recovery-code
  atomicity, MFA disable/reset, secret redaction, and interrupted enrollment,
- test that all protected API routes deny unauthenticated access and every
  intentionally public route exposes no notebook, Git, SSH, or host metadata,
- test authentication metadata loss and CLI recovery without modifying note
  repositories,
- test login and session expiry in normal tabs, multiple tabs, mobile layouts,
  and installed PWA mode,
- run dependency, static, container, and vulnerability scans with no ignored
  critical result,
- perform a dedicated manual security review against current OWASP
  authentication, password-storage, session-management, CSRF, and MFA guidance,
- document the remaining threat model and accepted limitations in the release
  notes,
- do not publish Alpha 2 until the complete authentication test and review gate
  passes.

### Milestone 19 completion criteria

- a fresh instance cannot be claimed without operator-controlled bootstrap
  authority,
- the owner can sign in with a password and optionally TOTP from desktop,
  mobile, and installed PWA,
- expiration and reauthentication never masquerade as Git failure or silently
  discard an editor draft,
- password hashes, session tokens, TOTP secrets, submitted codes, and recovery
  codes are never stored or logged in plaintext outside their strictly required
  protected form and lifetime,
- password, session, MFA, recovery, CSRF, throttling, and trusted-proxy behavior
  meets the requirements above and is covered by adversarial tests,
- `disabled` mode is explicit, visibly warned, and documented,
- container restart, auth reset, or loss of auth metadata cannot destroy or make
  the underlying Git-backed Markdown notebooks unreadable,
- Alpha 2 receives a dedicated security review before release.

## Milestone 20 - Continuous Vulnerability and Supply-Chain Management

Turn the existing point-in-time CI and release checks into a continuous process
that also detects vulnerabilities disclosed after an image was built.
Automation may discover, propose, test, and report updates; it must not silently
merge security-sensitive changes or deploy an unreviewed image.

### Phase 1 - Dependency inventory and update proposals

- add `.github/dependabot.yml` coverage for Go modules at the repository root,
  npm in `/frontend`, Docker base images, and GitHub Actions,
- enable the GitHub dependency graph, Dependabot alerts, and Dependabot security
  updates in repository settings,
- create security-update pull requests as soon as a supported advisory becomes
  available,
- check ordinary dependency updates on a controlled weekly schedule and group
  only low-risk development-tool updates where this improves reviewability,
- keep authentication, session, cryptography, TOTP, SQLite, Git execution,
  editor, PWA/service-worker, and container-base updates separately reviewable,
- retain reproducible `go.sum` and `package-lock.json` state and reject builds
  that require uncommitted dependency resolution,
- inventory direct and transitive runtime dependencies in the release SBOM,
- do not enable unattended dependency auto-merge.

### Phase 2 - GitHub repository security controls

- enable and verify Dependabot security alerts and maintainer notifications,
- enable GitHub secret scanning and push protection where available,
- enable CodeQL default setup for Go and JavaScript/TypeScript and evaluate the
  `security-extended` query suite for authentication code,
- run CodeQL for pull requests, the default/protected branch, and its scheduled
  cadence,
- configure branch protection or repository rulesets so required backend,
  frontend, secret, image, and code-scanning checks must pass before merge,
- prevent routine dependency updates from bypassing required checks,
- keep GitHub Actions pinned to immutable commit SHAs while allowing Dependabot
  to propose reviewed SHA updates,
- periodically verify in GitHub's tool-status views that scanners remain active
  and cover the expected files and languages.

### Phase 3 - Scheduled source and published-image scanning

- add a scheduled security workflow that runs at least weekly and preferably
  daily for known-vulnerability data,
- run `govulncheck ./...`, `npm audit`, secret scanning, and relevant static
  analysis even when no source commit occurred,
- build and scan a fresh production container with Trivy,
- separately rescan the newest published immutable Alpha image and moving Alpha
  channel tag so newly disclosed base-image or OS-package issues are detected,
- retain HIGH and CRITICAL findings as failing results unless a documented,
  time-limited, reviewed exception explains reachability and mitigation,
- never suppress a CRITICAL authentication, remote-code-execution, credential,
  traversal, or container-escape finding merely to make CI green,
- produce an actionable signal containing the affected tag, digest, component,
  advisory, severity, and fixed version where known,
- distinguish scanner/advisory-database outages from a clean result and fail
  closed for releases when required scan results are unavailable.

### Phase 4 - Dependency pull-request safety policy

- treat every automated update as an untrusted code change that must pass normal
  pull-request review and complete CI,
- require Go tests, race detection, `go vet`, `govulncheck`, frontend lint and
  tests, production PWA build, `npm audit`, secret scanning, CodeQL, container
  build, Trivy, and runtime smoke tests before merge,
- require Milestone 19 authentication unit, integration, adversarial, and PWA
  session tests for every update that can affect authentication,
- review upstream release notes, advisories, maintenance status, license, and
  relevant transitive changes before merging a security-critical dependency,
- do not assume SemVer guarantees compatibility; patch and same-release-line
  updates are lower risk but can still alter behavior, defaults, serialization,
  timing, or security semantics,
- separate major and security-sensitive updates from unrelated changes and do
  not group multiple authentication or cryptographic libraries opaquely,
- require human approval for production runtime dependencies and every auth,
  session, MFA, cryptographic, database, Git, PWA, and container update,
- permit auto-merge only after a later explicit policy names a narrow class of
  development-only updates and proves that required review is not weakened.

### Phase 5 - Release and deployment safety

- publish only from an explicit immutable release tag after source,
  architecture, image, runtime, and security gates pass,
- never overwrite or move an immutable version tag,
- move the Alpha convenience tag only after its immutable multi-architecture
  image has passed every validation,
- do not let Dependabot, CI, or a successful merge deploy directly to user
  installations,
- recommend immutable image pinning for production-like Alpha deployments and
  document automatic channel-tag updaters as an explicit operator risk,
- test a release candidate with fresh storage and representative existing
  persistent data,
- test auth schemas, sessions, notebook metadata, Git credentials, PWA updates,
  migration, and rollback for dependency changes touching persistence or
  security,
- publish image digest, SBOM, provenance, limitations, migration, and rollback
  instructions with each Alpha release.

### Phase 6 - Vulnerability response and maintenance policy

- define triage for Dependabot, CodeQL, Trivy, `govulncheck`, npm, secret scans,
  and privately reported vulnerabilities,
- evaluate reachability and relevance without dismissing a finding solely
  because an automated tool cannot prove exploitability,
- identify affected and fixed versions clearly and ship fixes as new immutable
  releases,
- rotate any credential that may have been disclosed; removing it from the
  latest commit is insufficient,
- keep private reports confidential until remediation and coordinated
  disclosure are ready,
- periodically verify that dependency feeds, scanners, pinned actions, base
  images, and security libraries remain maintained,
- state explicitly that automation detects known advisories and patterns, not
  every unknown vulnerability, logic flaw, unsafe deployment, or auth design
  mistake.

### Milestone 20 completion criteria

- Go, npm, Docker, and GitHub Actions dependencies receive automated update and
  security proposals,
- CodeQL, Dependabot alerts/security updates, secret scanning, and push
  protection are enabled and verified where GitHub provides them,
- source and published images are rescanned independently of repository
  activity,
- no dependency pull request can merge without required functional, build,
  runtime, and security checks plus the required human review,
- no dependency automation directly publishes or deploys an image,
- newly disclosed HIGH or CRITICAL issues produce an actionable maintainer
  signal identifying affected artifacts,
- vulnerability response, supported-version, remediation, migration, and
  rollback procedures are documented and exercised,
- Milestones 19 and 20 pass their complete gates before Alpha 2 publication.

## Milestone 21 - Beginner-Friendly GitHub Notebook Onboarding

Implementation status: completed on the Alpha 2 development branch. The
existing provider-independent five-step wizard now adds a guided GitHub path
for repository creation, SSH-address discovery, managed deploy-key setup,
explicit write access, secure host trust, actionable connection repair, and a
beginner-readable final review. GitLab, Forgejo/Gitea, generic SSH Git servers,
and the advanced existing-server SSH option continue to use the same ordinary
Git-over-SSH backend without GitHub APIs, tokens, OAuth, or provider-specific
canonical metadata.

Refine the existing provider-independent notebook onboarding so that a user who
knows what GitHub is, but has little or no practical Git or SSH experience, can
connect a private GitHub repository without having to understand clone URLs,
deploy keys, branches, SSH host verification, or Git terminology in advance.

This milestone extends Milestone 18. It does NOT introduce GitHub OAuth,
GitHub Apps, provider API authentication, automatic repository creation,
provider tokens, or any mandatory cloud dependency.

The underlying connection must remain ordinary provider-independent Git over
SSH.

### Goal

The GitHub onboarding path should feel like a guided self-hosting workflow, not
like configuring a Git client.

A first-time user should be able to understand:

1. that RepoQuill needs an existing GitHub repository,
2. how to create one if necessary,
3. where to find the correct repository address,
4. why RepoQuill creates an SSH key,
5. where that public key must be added in GitHub,
6. why write access is required,
7. why the GitHub host fingerprint must be trusted,
8. how to test the connection,
9. what to fix when a connection step fails.

The user should not need to leave RepoQuill and independently research these
concepts before completing setup.

### Phase 1 - Clarify the GitHub repository requirement

When GitHub is selected as the provider:

- explain in plain language that RepoQuill connects to an existing GitHub
  repository,
- explain that a private repository is recommended for personal notes,
- explicitly state that RepoQuill does not create repositories through the
  GitHub API in Alpha 2,
- provide a clearly labeled action such as `Create a repository on GitHub` or
  `Open GitHub repository creation`,
- open external GitHub pages in a way that does not discard the current
  onboarding state,
- do not make GitHub availability a runtime dependency for RepoQuill itself.

Suggested copy:

```text
RepoQuill stores this notebook in a normal Git repository.

If you do not already have a repository for these notes, create a new private
repository on GitHub first.

RepoQuill will connect to it without changing the repository format.
```

The wizard should not require the user to understand what a Git repository is
before this explanation is shown.

### Phase 2 - Guide repository-address discovery

For GitHub, replace unexplained primary labels such as:

```text
Repository SSH address
```

with provider-aware wording such as:

```text
GitHub repository address
```

Technical SSH terminology may remain in supporting text.

Show the exact GitHub workflow for obtaining the address:

```text
Open the repository on GitHub
→ Code
→ SSH
→ Copy
```

Show a realistic example:

```text
git@github.com:username/private-notes.git
```

Explain that the browser address is NOT the required value.

Examples that should be detected and explained include:

```text
https://github.com/user/repository
https://github.com/user/repository/tree/main
https://github.com/user/repository/blob/main/README.md
```

If an HTTPS clone URL is pasted, explain that Alpha 2 uses SSH for private
repository access and tell the user exactly where to obtain the SSH form.

Continue rejecting:

- local filesystem paths,
- malformed SSH URLs,
- embedded credentials,
- option-like values,
- Markdown links,
- escaped addresses,
- unsafe Git protocols,
- branch or file browser URLs.

Error messages should explain the correction rather than merely reporting an
invalid value.

Example:

```text
This looks like the GitHub website address.

In GitHub, open Code → SSH and copy the address that starts with
git@github.com:
```

### Phase 3 - Explain the managed SSH key in normal language

The recommended beginner path remains:

```text
RepoQuill-managed SSH key
```

Do not require the user to understand asymmetric cryptography.

Explain:

- RepoQuill creates a dedicated key for this notebook,
- the private key stays on the RepoQuill server,
- only the public key is shown in the browser,
- the public key is safe to copy into GitHub,
- the key gives this RepoQuill notebook access only where the user explicitly
  adds it,
- the user's personal SSH private key is never requested or uploaded.

Suggested copy:

```text
RepoQuill uses a dedicated key to access this repository.

The private part stays on your RepoQuill server.
You only need to copy the public key below into GitHub.
```

Do not expose filesystem paths or internal key IDs as part of the normal
beginner explanation unless the user opens technical details.

### Phase 4 - Embed GitHub deploy-key instructions directly

For GitHub, RepoQuill itself must show the minimum provider-side steps needed to
complete setup.

The user should see instructions equivalent to:

```text
In GitHub:

1. Open your repository.
2. Open Settings.
3. Open Deploy keys.
4. Choose Add deploy key.
5. Paste the public key shown below.
6. Enable "Allow write access".
7. Save the key.
```

`Allow write access` must be visually emphasized.

Explain why it matters:

```text
Write access is required so RepoQuill can synchronize changes back to GitHub.
```

Do not assume the user knows that a read-only deploy key may allow part of the
connection process to succeed while preventing normal synchronization.

The existing `Open setup instructions` action should remain available for
deeper provider documentation, but external documentation must not be the only
place where the required steps are explained.

### Phase 5 - Separate repository access from SSH host trust

The user must not confuse:

```text
GitHub gave RepoQuill permission to this repository
```

with:

```text
RepoQuill trusts that this server is really github.com
```

Keep explicit SSH host fingerprint verification.

Explain the host-trust step in plain language before showing fingerprints.

Suggested copy:

```text
RepoQuill has not connected to this Git server before.

Before continuing, verify that the server identity below belongs to GitHub.
This prevents RepoQuill from silently trusting an unexpected server.
```

The UI may still show:

- hostname,
- port,
- key type,
- SHA256 fingerprint,
- previously trusted fingerprints where applicable.

Keep the current security behavior:

- unknown hosts require explicit approval,
- changed host keys remain blocked,
- changed keys must not be silently replaced,
- `StrictHostKeyChecking=no` or equivalent bypasses remain forbidden.

### Phase 6 - Make connection testing the obvious next step

After the user adds the deploy key in GitHub:

- make `Test connection` the clear next action,
- show progress while testing,
- retain completed onboarding state after failure,
- never force the user to generate another key merely because the test failed.

Connection failures should be translated into concrete repair instructions.

Authentication failure example:

```text
GitHub did not accept this key.

Check that the public key was added under:
Repository Settings → Deploy keys

Also confirm that "Allow write access" is enabled.
```

Repository-not-found example:

```text
RepoQuill reached GitHub, but this repository could not be opened.

Check that:
- the repository exists,
- the repository address is correct,
- the deploy key was added to this repository.
```

Branch-not-found example:

```text
The selected branch does not exist.

Leave the branch empty to use the repository's default branch, or enter an
existing branch name.
```

For network failures, explain that RepoQuill could not reach the Git server
from the RepoQuill host and suggest checking:

- DNS,
- outbound firewall rules,
- proxy or network configuration,
- Internet connectivity from the RepoQuill server.

Do not expose raw Git stderr as the primary explanation.

Technical details may remain available behind an optional diagnostic view.

### Phase 7 - Preserve wizard progress

Opening GitHub or external setup documentation must not discard:

- notebook name,
- selected provider,
- repository address,
- optional branch,
- selected authentication method,
- generated managed key,
- connection-test result where still valid,
- onboarding step.

Do not persist private keys or secret credentials in browser storage.

The managed private key remains server-side.

If onboarding is intentionally closed, retain generated unassigned keys through
the existing key-management behavior so the user can safely reuse or remove them
later.

### Phase 8 - Final review

The final review should show beginner-readable information first:

```text
Notebook
Git service
Git server
Repository
Branch
Access method
Connection status
```

Avoid showing internal key IDs, filesystem paths, raw Git arguments, or other
implementation details unless explicitly requested.

Explain what happens after confirmation:

```text
RepoQuill will copy the repository to this server and open it as a notebook.

Your notes remain normal Markdown files inside the Git repository.
```

Keep the distinction between:

```text
Saved on this RepoQuill server
```

and:

```text
Synchronized with GitHub
```

visible in the explanation where appropriate.

### Phase 9 - Preserve provider independence

GitHub-specific onboarding guidance must remain presentation-only.

Do not couple the notebook model, Git service, credential model, or backend Git
operations to GitHub.

GitLab, Forgejo/Gitea, and generic SSH Git servers must continue to use the
existing provider-independent Git-over-SSH implementation.

Provider-specific guidance may explain where users find repository addresses,
deploy keys, or equivalent settings, but the underlying RepoQuill behavior must
remain ordinary Git and SSH.

Keep `Existing server SSH configuration` available as an advanced connection
option.

Do not introduce GitHub API calls, GitHub-specific canonical metadata, OAuth,
GitHub Apps, or provider tokens as part of this milestone.

### Testing

Add focused frontend tests covering:

- GitHub provider selection,
- repository-creation guidance,
- repository-address instructions,
- GitHub browser URL rejection,
- HTTPS clone URL guidance,
- valid SSH URL progression,
- managed-key generation,
- public-key copy action,
- explicit `Allow write access` guidance,
- external setup-instructions link,
- preserved wizard state,
- connection-test retry,
- authentication failure messaging,
- repository-not-found messaging,
- branch failure messaging,
- network failure messaging,
- unknown-host approval,
- changed-host blocking,
- final review,
- keyboard operation,
- narrow mobile/PWA layout.

Backend security behavior must remain unchanged unless a concrete bug is found.

### Completion criteria

- a first-time GitHub user can create or identify a private repository, obtain
  the correct repository address, add RepoQuill's public key with write access,
  verify the Git host, and connect the notebook using RepoQuill's own guidance,
- the normal flow does not require the user to independently understand Git
  cloning, deploy keys, SSH authentication, branches, or host-key verification,
- no GitHub API token, OAuth flow, GitHub App, mandatory SaaS dependency, or
  provider-specific canonical state is introduced,
- the underlying Git/SSH implementation remains provider-independent,
- existing security guarantees for managed keys and SSH host verification are
  preserved.

---

## Milestone 22 - Portable Image Lightbox and Full-Size Viewing

Implementation status: completed on the Alpha 2 development branch. Images can
now be opened from the contextual Edit-mode toolbar or directly by pointer and
keyboard in Read only mode. The responsive in-app viewer provides fit-to-screen
and actual-size inspection, scrolling, accessible focus handling and closing,
alt-text fallback, and non-mutating load errors while continuing to use the
existing confined asset URL. It adds no image dependency, stored rendition,
custom Markdown, asset mutation, save, or synchronization behavior.

Add a simple, responsive image viewer so users can inspect screenshots, diagrams,
photos, and other note images at a larger size without changing Markdown
serialization or introducing a custom image format.

This milestone is intentionally limited to viewing.

It must NOT introduce:

- persisted image width or height attributes,
- custom Markdown image-size syntax,
- RepoQuill-specific Markdown extensions,
- HTML replacement syntax for ordinary inserted images,
- image cropping,
- image annotation,
- image editing,
- image compression as part of viewing,
- asset duplication solely for viewing.

The stored Markdown remains ordinary portable Markdown such as:

```markdown
![](BGP.assets/01ABCDEF.png)
```

or:

```markdown
![Topology](BGP.assets/01ABCDEF.png)
```

### Goal

Users should be able to inspect an inserted image at a useful size, especially
screenshots and diagrams that are scaled down inside the editor.

The viewer must remain purely presentational.

Opening or interacting with it must not modify:

- the Markdown document,
- the image asset,
- the note's save state,
- the note's Git state,
- image alt text,
- note tabs,
- Read only/Edit mode.

### Phase 1 - Opening behavior

In Edit mode:

- preserve the existing image-selection behavior,
- provide an explicit contextual action such as `Open full size` or
  `View image`,
- do not make normal editor image selection unreliable merely to support the
  lightbox,
- do not require double-click or another undiscoverable interaction as the only
  way to open the viewer.

In Read only mode:

- clicking or tapping an image may open the viewer directly where this is
  intuitive,
- opening the viewer must not accidentally switch the note into Edit mode.

Keyboard users must have an accessible way to open the image viewer.

Do not make hover the only discovery mechanism.

### Phase 2 - Viewer presentation

Open images in an in-app modal/lightbox.

Initial behavior should:

- center the image in the available viewport,
- fit oversized images within the viewport,
- preserve aspect ratio,
- avoid unnecessarily upscaling small images,
- visually separate the image from the note behind it,
- keep controls usable on desktop and narrow mobile screens.

The viewer must display the existing original asset through RepoQuill's
confined image-serving path.

Do not create thumbnails or alternate stored copies solely for the lightbox
unless a later performance requirement explicitly justifies them.

### Phase 3 - Zoom and original-size inspection

Provide a deliberately small inspection model.

At minimum support:

```text
Fit to screen
```

and, where practical:

```text
Actual size / 100%
```

Simple controls may additionally include:

```text
Zoom in
Zoom out
Reset
```

Do not build a full image editor or infinite-canvas system.

If the image is displayed above viewport size:

- allow the user to inspect the remaining area through sensible panning or
  scrolling,
- do not distort the image,
- do not resize or rewrite the source asset.

If pointer-wheel or pinch zoom is implemented:

- keep it optional and intuitive,
- do not interfere with normal application behavior outside the lightbox,
- provide visible controls as an accessible alternative,
- avoid introducing a large gesture library solely for this feature unless
  clearly justified.

### Phase 4 - Closing behavior

Support:

- an explicit visible close button,
- Escape-key close on desktop,
- backdrop click where appropriate and accessible,
- a touch-friendly mobile close action.

Focus should move into the modal when opened and return to an appropriate
control or image after closing.

Do not trap the user inside the viewer if the image fails to load.

### Phase 5 - Accessibility

Use existing Markdown alt text as the image's accessible description where
available.

If alt text is empty:

- provide an appropriate generic label such as `Note image`,
- do not invent descriptive content.

Controls must have accessible names.

Viewer state must not rely on color alone.

Keyboard navigation must remain possible.

Focus trapping, closing, and restoration should follow normal accessible dialog
behavior.

### Phase 6 - Mobile and PWA behavior

The viewer must work in:

- narrow mobile browsers,
- installed PWA mode,
- portrait orientation,
- landscape orientation.

Respect viewport and safe-area constraints where appropriate.

Controls must remain reachable without hover.

The viewer must tolerate device rotation and viewport resizing.

Do not make the lightbox dependent on browser-native image-opening behavior that
unexpectedly leaves the installed PWA.

### Phase 7 - Error handling

If the asset cannot be loaded:

- show a clear viewer error,
- keep the modal closable,
- do not modify the Markdown,
- do not remove the image node,
- do not mark the note unsaved merely because viewing failed,
- do not trigger asset cleanup or replacement.

If the asset disappears externally while the note remains open, treat this as a
viewing failure rather than an editor mutation.

### Phase 8 - Editor and application state safety

Opening, zooming, panning, and closing the viewer must not:

- call the note save endpoint,
- change `SaveStatus`,
- create Git-visible filesystem changes,
- reset or recreate the editor unnecessarily,
- alter Read only/Edit state,
- change the selected note tab,
- trigger image replacement,
- trigger asset cleanup,
- trigger synchronization merely because the viewer was used.

Preserve the existing editor selection where practical.

If the active note or notebook changes while the viewer is open, close the
viewer safely rather than continuing to show an asset belonging to the previous
context.

### Testing

Add focused frontend tests covering:

- opening from contextual image controls in Edit mode,
- opening from Read only mode,
- explicit close button,
- Escape close,
- backdrop behavior where implemented,
- focus handling and focus return,
- empty-alt fallback,
- failed image loading,
- fit-to-screen behavior,
- actual-size or zoom behavior where implemented,
- narrow mobile layout,
- orientation or viewport resizing where practical,
- viewer closure when note context changes,
- proof that viewer interactions do not mutate Markdown,
- proof that viewer interactions do not trigger note saves,
- proof that viewer interactions do not alter Read only/Edit state.

No backend changes should be required unless the current confined image-serving
endpoint cannot safely support displaying the original asset in the viewer.

### Completion criteria

- inserted screenshots and diagrams can be inspected substantially larger than
  their inline editor rendering,
- the user can return to the note without losing editor state,
- the original image asset remains unchanged,
- opening and using the viewer produces no Markdown change,
- standard Markdown image syntax remains the canonical representation,
- no RepoQuill-specific image-size syntax is introduced,
- no image-editing subsystem or unnecessary image dependency is introduced,
- desktop, mobile/PWA, Edit, and Read only workflows remain usable.

---

## Milestone 22.1 - Portable Image Presentation Sizes

Extend Milestone 22 with a deliberately small RepoQuill presentation layer for
inline note images.

Implementation status: completed on the Alpha 2 development branch. RepoQuill
stores exactly the four semantic presets in a confined, non-canonical metadata
file beside the notebook registry. Markdown and original assets remain
unchanged; missing or damaged presentation metadata falls back to Full. The
simpler Alpha 2 identity is per asset reference within a note, so repeated uses
of the same asset share one size. Presentation metadata follows supported note
renames and moves. Deliberately trashing a note removes its presentation
metadata; a later Trash restore safely recovers the note and assets but uses the
default presentation size.

Users should be able to choose a consistent visual size for an inserted image
without changing the canonical Markdown image syntax, modifying the original
asset, or introducing RepoQuill-specific Markdown extensions.

Supported presentation sizes are:

```text
Small
Medium
Large
Full
```

This milestone is about predictable note layout.

It is NOT an image editor and must remain substantially smaller in scope than a
general-purpose image-layout system.

### Core principle

The canonical note must remain ordinary Markdown.

For example:

```markdown
![Topology](BGP.assets/01ABCDEF.png)
```

must remain exactly ordinary Markdown regardless of the selected RepoQuill
presentation size.

Do NOT serialize presentation information as:

```markdown
![](image.png){width=400}
```

or:

```html
<img src="image.png" width="400">
```

or any other custom Markdown/HTML representation.

The original asset must also remain unchanged.

Presentation metadata may improve how RepoQuill displays a note, but it must
never be required to:

- read the note,
- locate the image,
- recover the note,
- render the image in another Markdown application,
- understand the note outside RepoQuill.

If all RepoQuill presentation metadata disappears, the note and image must
remain fully usable.

### Goal

A user inserting several screenshots or diagrams into one note should be able
to create a visually consistent document such as:

```text
Paragraph
    ↓
Medium screenshot
    ↓
Paragraph
    ↓
Medium screenshot
    ↓
Paragraph
    ↓
Large topology diagram
```

without resizing or duplicating the underlying image files.

The feature should complement the Milestone 22 lightbox:

```text
Inline presentation size
        ↓
Small / Medium / Large / Full
        ↓
click "View image"
        ↓
original asset in lightbox
```

The lightbox always operates on the original asset, not on an inline-sized
derivative.

### Phase 1 - Presentation-size model

Support exactly four user-facing presets:

```text
Small
Medium
Large
Full
```

Do not expose arbitrary pixel widths in Alpha 2.

Do not add drag handles for free resizing.

Do not store raw CSS values supplied by the user.

Treat the presets as semantic presentation choices rather than fixed physical
dimensions.

A reasonable responsive implementation may map them approximately to:

```text
Small   → about one third of the available editor width
Medium  → about one half of the available editor width
Large   → about three quarters of the available editor width
Full    → available editor width
```

Exact CSS values may be adjusted to fit the existing editor layout.

All presets must:

- preserve image aspect ratio,
- remain responsive,
- never overflow the editor viewport,
- work on narrow mobile screens,
- remain usable when the browser or PWA viewport changes.

Avoid destructive image scaling.

Where practical, do not upscale a source image beyond its useful natural
resolution merely to satisfy a preset.

### Phase 2 - Presentation metadata

Presentation size must NOT be serialized into Markdown.

Store it as optional RepoQuill presentation metadata.

Prefer the existing internal metadata/configuration model rather than modifying
the note file.

The metadata is non-canonical.

Conceptually it may contain information equivalent to:

```json
{
  "note": "Network/BGP.md",
  "image": "BGP.assets/01ABCDEF.png",
  "size": "medium"
}
```

The exact internal persistence schema may differ.

Only store the minimum required presentation information.

For Alpha 2, store:

```text
image presentation size
```

Do NOT expand the schema into a generic arbitrary style system.

Do NOT store:

- CSS,
- arbitrary width,
- arbitrary height,
- crop coordinates,
- filters,
- image transformations,
- positioning offsets,
- editor DOM identifiers.

The metadata database must remain non-canonical.

Deleting or losing it must never damage note content or assets.

### Phase 3 - Stable image identification

Presentation metadata must be associated with the actual Markdown image in a
predictable way.

Use validated note and relative asset paths or another existing stable asset
identity.

Do not rely on transient ProseMirror node positions as persistent identifiers.

Do not rely on generated browser DOM IDs.

If the same asset is intentionally referenced more than once in the same note,
consider whether presentation is:

```text
per asset
```

or:

```text
per image occurrence
```

For Alpha 2, prefer the simpler and safer model unless the existing editor
structure makes per-occurrence identity straightforward.

If presentation is stored per asset reference, document that multiple uses of
the same asset in one note share the same presentation size.

Do not invent hidden identifiers inside Markdown merely to distinguish image
occurrences.

### Phase 4 - Default behavior

Existing notes must continue to render correctly with no migration requirement.

An image with no presentation metadata uses the existing RepoQuill image
rendering behavior.

Do not rewrite existing Markdown merely to establish a default size.

A newly inserted image may initially use:

```text
Full
```

or the existing natural/default rendering behavior, whichever best matches the
current editor and avoids a surprising visual regression.

The default should be consistent across:

- clipboard paste,
- image picker,
- mobile gallery/camera upload,
- existing Markdown images.

Do not require presentation metadata for ordinary images.

### Phase 5 - Image contextual controls

Extend the existing selected-image contextual UI.

When an image is selected in Edit mode, provide controls equivalent to:

```text
Image size
[ Small ] [ Medium ] [ Large ] [ Full ]

Alt text
Replace image
View image
Remove image
```

The exact visual arrangement may adapt to available space.

The currently selected size must be visible.

Changing the presentation size should update the editor immediately.

The size controls must:

- be keyboard accessible,
- have accessible names,
- have touch-friendly targets,
- work without hover,
- fit narrow mobile layouts.

Do not overload the main formatting toolbar with four permanent image-size
buttons.

Keep these controls contextual to the selected image.

### Phase 6 - Read only rendering

Presentation metadata applies in both:

```text
Edit
```

and:

```text
Read only
```

views.

The same note should have approximately the same visual image layout in both
modes.

Read only mode must not expose controls that modify presentation metadata.

Opening the Milestone 22 lightbox from Read only remains supported.

### Phase 7 - Lightbox integration

Presentation size affects inline note layout only.

The Milestone 22 lightbox must always load and inspect the original asset.

For example:

```text
Medium inline image
        ↓
View image
        ↓
original image
        ↓
Fit / Actual size / zoom
```

Changing lightbox zoom must not change the inline presentation size.

Changing inline presentation size must not modify lightbox zoom state.

Do not generate resized derivative assets solely for presentation.

### Phase 8 - Note rename and move behavior

Presentation metadata must follow supported RepoQuill note operations.

When a note is renamed:

- preserve presentation metadata for its images,
- update the internal note identity/path safely.

When a note is moved:

- preserve presentation metadata,
- account for the corresponding existing asset-directory move behavior.

When an image is replaced:

- preserve the selected presentation size where sensible,
- do not copy unrelated metadata from another asset.

When an image is removed from the Markdown document:

- stale presentation metadata may be cleaned conservatively,
- stale metadata must never cause asset deletion,
- metadata cleanup must never be considered canonical content cleanup.

When a note is deleted:

- its presentation metadata may be removed with the note's internal metadata,
- Trash/restore behavior should restore presentation metadata where practical
  if the current architecture supports doing so safely.

If restoring presentation metadata would substantially complicate Alpha 2
Trash semantics, losing presentation size after a Trash restore is preferable
to risking note or asset recovery.

Document any accepted limitation.

### Phase 9 - External edits and missing metadata

RepoQuill must tolerate Markdown being edited outside RepoQuill.

Examples include:

- GitHub,
- VS Code,
- Obsidian,
- another Git client.

If an image reference is added externally:

- render it normally,
- use the default presentation behavior,
- do not require presentation metadata.

If an image reference is removed externally:

- ignore or conservatively clean stale presentation metadata,
- do not recreate the Markdown image.

If a note or asset path changes externally and the old presentation metadata can
no longer be matched safely:

- fall back to default rendering,
- do not guess,
- do not modify the Markdown to repair presentation metadata.

Presentation metadata must never turn an ordinary Git conflict into a content
conflict.

Canonical Git synchronization remains about the Markdown and assets.

### Phase 10 - Failure behavior

Failure to read or write presentation metadata must not block note editing.

If the user changes an image size and presentation persistence fails:

- keep the Markdown unchanged,
- keep the asset unchanged,
- show a small actionable presentation-setting error,
- do not report the note itself as unsaved if its Markdown is already saved,
- do not report a Git conflict,
- do not create or modify Git content merely to recover the presentation
  preference.

Presentation metadata failure must never become a note-data-loss scenario.

### Phase 11 - Save and synchronization semantics

Changing only:

```text
Small → Medium
```

must NOT cause the Markdown note to become dirty.

It must not trigger the normal Markdown autosave endpoint.

It must not create a Git commit containing a fake Markdown change.

It must not modify the image asset.

If presentation metadata is stored only in RepoQuill's internal metadata
storage, presentation-only changes are application metadata changes rather than
Git synchronization changes.

The UI must not misleadingly report:

```text
Unsaved note
```

or:

```text
Unsynchronized note
```

solely because an image presentation preset changed.

This distinction is important:

```text
note content
≠
RepoQuill presentation preference
```

### Phase 12 - Portability behavior

Verify the resulting repository after presentation sizes have been used.

The Markdown must still contain ordinary image references such as:

```markdown
![Topology](BGP.assets/01ABCDEF.png)
```

The asset remains the original ordinary file:

```text
Network/BGP.assets/01ABCDEF.png
```

Opening the repository without RepoQuill must still provide:

- readable Markdown,
- working relative image references,
- original images,
- no dependency on a RepoQuill renderer.

Presentation sizing disappearing outside RepoQuill is an accepted and deliberate
degradation.

Content disappearing outside RepoQuill is not acceptable.

### Phase 13 - Scope guardrails

Milestone 22.1 must NOT grow into:

- freeform image resizing,
- pixel-width input,
- drag-to-resize,
- image alignment controls,
- text wrapping around images,
- side-by-side image grids,
- galleries,
- image captions beyond existing Markdown alt behavior,
- image cropping,
- image rotation,
- image filters,
- image compression,
- image optimization pipelines,
- generated thumbnails,
- derivative image management,
- arbitrary presentation CSS,
- a generic note-layout engine.

Those may be evaluated separately after real-world use.

For Alpha 2, the feature is complete when these four presets make ordinary note
layout substantially more consistent.

### Testing

Add focused frontend tests covering:

- existing image with no presentation metadata,
- newly inserted image default behavior,
- Small selection,
- Medium selection,
- Large selection,
- Full selection,
- active-preset state,
- immediate editor rendering,
- Edit mode behavior,
- Read only rendering,
- keyboard operation,
- narrow mobile layout,
- lightbox opening from every presentation size,
- original asset remaining the lightbox source,
- note switch and return,
- browser/PWA reload with persisted presentation metadata,
- note rename,
- note move,
- image replacement,
- image removal,
- missing presentation metadata,
- stale presentation metadata,
- external Markdown image insertion,
- presentation persistence failure.

Add explicit regression tests proving that changing image presentation size:

- does not change serialized Markdown,
- does not call Markdown `onChange`,
- does not trigger note autosave,
- does not modify the image asset,
- does not create a Git-visible content change,
- does not change Read only/Edit mode,
- does not alter the active note or tab.

Backend tests must cover any new presentation-metadata API or persistence logic,
including:

- notebook confinement,
- note-path validation,
- asset-path validation,
- invalid size rejection,
- missing-note handling,
- rename/move preservation,
- safe deletion/cleanup,
- authentication and CSRF protection through the existing application boundary.

### Documentation

Update the Alpha 2 documentation in Milestone 23 to explain:

- RepoQuill supports Small / Medium / Large / Full inline image presentation,
- presentation size is a RepoQuill display preference,
- Markdown remains standard Markdown,
- the original asset is never resized,
- other Markdown applications still see the ordinary image,
- RepoQuill-specific sizing may not appear outside RepoQuill,
- the lightbox always exposes the original image.

Update `CHANGELOG.md` factually after implementation.

### Completion criteria

- users can select Small, Medium, Large, or Full for an inline note image,
- size presets produce visually consistent responsive note layouts,
- the selected presentation persists across reloads,
- presentation works in Edit and Read only modes,
- the Milestone 22 lightbox continues to inspect the original asset,
- changing presentation size never modifies serialized Markdown,
- changing presentation size never modifies or duplicates the original asset,
- ordinary Markdown image syntax remains canonical,
- notes and assets remain fully readable without RepoQuill,
- missing or corrupt presentation metadata degrades only visual layout,
- external Git/Markdown edits remain safe,
- presentation metadata does not create content conflicts,
- the feature remains limited to four semantic size presets and does not expand
  into a general image-layout or image-editing subsystem.

---

## Milestone 23 - Alpha 2 Documentation and Release Alignment

Perform a deliberate documentation and release-material sweep after the Alpha 2
functionality is complete.

Implementation status: completed on the Alpha 2 development branch. Public
documentation now describes the implemented local/disabled authentication
model, guided conflict resolution, synchronization semantics, GitHub-oriented
SSH onboarding, portable image lightbox/presentation behavior, current
limitations, and `/data` backup, upgrade, rollback, and independent-recovery
flows. The final immutable Alpha 2 version remains intentionally unset until the
Milestone 24 dependency and exact release-candidate gate.

This milestone should be performed after the user-facing Alpha 2 functionality,
including Milestones 21 and 22, has stabilized and before the final dependency
and release gate in Milestone 24.

The goal is to ensure that all public documentation describes the application
that will actually ship in Alpha 2 rather than retaining assumptions,
limitations, instructions, or wording from Alpha 1.

This milestone is not a general documentation rewrite. Focus on correctness,
clarity, consistency, beginner usability, and release readiness.

### Phase 1 - Documentation inventory

At minimum review:

- `README.md`,
- `ALPHA-RELEASE.md`,
- `KNOWN-LIMITATIONS.md`,
- `SECURITY.md`,
- `SECURITY-MAINTENANCE.md`,
- `CHANGELOG.md`,
- Docker Compose examples,
- environment-variable documentation,
- user-facing version strings,
- release workflow/version checks,
- setup and recovery instructions,
- documentation linked directly from the application.

Search the repository for Alpha 1-specific statements, old milestone numbers,
obsolete feature descriptions, stale screenshots or examples, and technical
wording that no longer matches the current UI.

### Phase 2 - Authentication documentation

Remove or update stale statements that imply Alpha 2:

- has no built-in authentication,
- always requires an external interactive authentication proxy,
- cannot detect its own session expiry,
- requires manual browser refresh after authentication expiry.

Document the actual Alpha 2 model:

```text
local
→ built-in single-owner password
→ optional TOTP MFA

disabled
→ explicit operator decision
→ LAN/VPN/external protection responsibility

OIDC
→ future feature
```

Document where applicable:

- first-run/bootstrap-token setup,
- password requirements,
- session behavior,
- Remember this device behavior,
- session revocation,
- password change/reset,
- MFA enrollment,
- MFA recovery codes,
- operator recovery,
- PWA/session reauthentication,
- HTTPS requirement,
- trusted reverse-proxy configuration.

Do not imply that local authentication removes the need for TLS on
Internet-facing installations.

Do not describe authentication access control as note encryption or confuse it
with any future Secure Notes/Secure Folders feature.

### Phase 3 - Conflict-resolution documentation

Remove stale instructions that say normal supported conflicts must be resolved
through:

```text
git status
git rebase
manual Git client
```

Document the current guided conflict model instead.

Explain in user-facing terms:

- RepoQuill pauses synchronization when overlapping changes require a decision,
- both versions are preserved,
- the user reviews `Your version` and `Other version`,
- Markdown conflicts can be resolved through the guided review,
- supported delete/modify, rename/move, and image conflicts have explicit UI
  flows,
- RepoQuill creates a recovery point before applying a completed Git conflict
  decision,
- RepoQuill does not silently choose a winner,
- RepoQuill does not force-push through a conflict.

Technical Git-client recovery may remain documented as an emergency or
administrator fallback where appropriate, but it must no longer be presented as
the normal workflow for conflicts that RepoQuill supports directly.

### Phase 4 - Synchronization documentation

Use consistent terminology across all public documents.

```text
Saved on this server
```

means the Markdown file is safely persisted in RepoQuill's persistent storage.

```text
Synchronized
```

means RepoQuill successfully committed and transferred the change to the
configured Git remote.

Do not imply that saving automatically guarantees remote backup.

Document:

- manual synchronization,
- configured automatic synchronization triggers,
- inactivity synchronization,
- startup/focus synchronization where implemented,
- note/notebook navigation synchronization behavior,
- best-effort browser-close synchronization,
- remote-change reception,
- conflict pause behavior,
- locally safe state after a failed push where applicable.

Explain external edits clearly:

- GitHub,
- VS Code,
- another Git client,
- another compatible editor

may modify the same files outside RepoQuill.

If those changes overlap with RepoQuill changes, synchronization may pause for
guided conflict resolution.

Do not imply that RepoQuill can prevent conflicts caused by arbitrary external
writers.

### Phase 5 - Notebook onboarding documentation

Update public onboarding instructions to match Milestone 21.

For GitHub, explain the beginner flow:

```text
Create or use a private repository
→ copy Code → SSH address
→ RepoQuill creates a dedicated key
→ add public key under GitHub Deploy keys
→ enable Allow write access
→ verify the Git host
→ test connection
→ connect notebook
```

Explain that the RepoQuill-managed private key remains on the RepoQuill server
and that only its public key is copied to GitHub.

Avoid requiring README readers to understand:

- Git staging,
- rebasing,
- personal SSH keys,
- shell-based key installation,
- Git internals.

Document that GitHub is not the only supported Git service.

The core mechanism remains provider-independent Git over SSH.

Do not document GitHub Apps, OAuth, HTTPS/PAT authentication, automatic remote
creation, or other deferred ideas as implemented functionality.

### Phase 6 - Image documentation

After Milestone 22 is implemented:

- document the image lightbox/full-size viewing behavior where useful,
- explain that viewing or zooming an image does not modify the underlying asset,
- do not imply that RepoQuill stores custom image-size metadata,
- retain the existing portable per-note `.assets` explanation,
- ensure image examples still use ordinary relative Markdown image syntax.

If persistent image resizing remains unsupported, document that limitation
accurately rather than implying the lightbox changes stored image dimensions.

### Phase 7 - Public feature list

Ensure public feature lists accurately reflect the Alpha 2 application,
including where implemented:

- WYSIWYG Markdown editing,
- multiple note tabs,
- search,
- screenshots and image upload,
- per-note portable assets,
- image lightbox/full-size viewing,
- recoverable Trash,
- note version history and restore,
- portable internal links,
- safe rename/move link updates,
- guided conflict resolution,
- human-readable synchronization details,
- multiple notebooks,
- managed SSH keys,
- explicit SSH host verification,
- beginner-friendly GitHub notebook onboarding,
- single-owner local authentication,
- optional TOTP MFA,
- asset cleanup,
- responsive online-first PWA behavior.

Do not list planned features as though they are implemented.

Keep the README approachable. Deep implementation detail belongs in more
appropriate documentation rather than overwhelming the initial product
description.

### Phase 8 - Known limitations

Keep limitations explicit and current.

Review at least:

- online-first PWA behavior,
- no offline editing,
- single-owner scope,
- one RepoQuill backend writer per notebook working tree,
- best-effort browser-close synchronization,
- external edits can create overlapping versions,
- no collaboration or CRDT model,
- no automatic GitHub repository creation,
- no GitHub App integration,
- OIDC deferred,
- no persistent portable image-sizing model if that remains the case,
- any accepted provider/onboarding limitations.

Do not describe intentional architectural constraints as unresolved bugs unless
they actually are bugs.

Do not hide meaningful Alpha limitations behind marketing language.

### Phase 9 - Upgrade, persistence, and recovery

Verify the documented upgrade path against the exact Alpha 2 release candidate.

Test and document:

- backup of `/data`,
- container replacement,
- authentication metadata migration,
- session behavior after upgrade,
- managed SSH key persistence,
- trusted-host persistence,
- notebook registration persistence,
- Git working-tree persistence,
- PWA update behavior,
- rollback to the previous immutable image,
- independent access to notebook repositories if RepoQuill fails.

Authentication reset and MFA recovery must never imply that notebook contents
are modified.

Make clear that canonical note content remains ordinary Markdown and assets in
Git repositories independently of RepoQuill's authentication metadata.

### Phase 10 - Release-version consistency

Before tagging Alpha 2, verify consistency across:

- application version,
- frontend package version,
- backend/binary version,
- Docker/OCI metadata,
- changelog,
- release documentation,
- example image tags,
- Git tag,
- GitHub release title,
- moving Alpha tag.

Do not publish placeholder Alpha 2 version strings before the final version is
selected.

Immutable release tags must never be reused or moved.

### Phase 11 - Command and example verification

Review or execute all important published examples where applicable:

- Docker Compose,
- first-run/bootstrap workflow,
- password-recovery commands,
- MFA-recovery commands,
- environment variables,
- persistent volume paths,
- reverse-proxy configuration,
- trusted-proxy configuration,
- GHCR image names,
- backup paths,
- upgrade commands,
- rollback commands.

Remove or fix examples that no longer match the implementation.

A command appearing in public documentation should not be assumed correct merely
because it existed in Alpha 1.

### Phase 12 - Changelog and release notes

Keep `CHANGELOG.md` factual.

Only implemented changes belong under:

```text
Unreleased
```

or a released version.

Future ideas remain in issues, future-feature sections, or later milestones.

Before release, organize Alpha 2 changes into understandable Keep a Changelog
categories such as:

```text
Added
Changed
Fixed
Security
```

Release notes should emphasize user-visible changes and important deployment or
security changes rather than dumping internal implementation details.

Clearly identify upgrade considerations and accepted Alpha limitations.

### Completion criteria

- no public document contradicts the shipped authentication behavior,
- no public document incorrectly requires manual Git conflict resolution for
  workflows RepoQuill now handles,
- onboarding documentation matches the actual beginner-friendly GitHub flow,
- image documentation matches the actual portable lightbox behavior,
- installation and upgrade examples work against the final Alpha 2 candidate,
- recovery instructions preserve the core plain-file safety model,
- known limitations remain honest and current,
- version strings and release references are internally consistent,
- future features are not presented as shipped functionality,
- a new self-hoster can understand installation, authentication, notebook
  onboarding, synchronization, backup, recovery, and major limitations without
  reading `AGENTS.md`.

---

## Milestone 24 - Final Alpha 2 Dependency and Toolchain Review

Perform a deliberate final review of every dependency and build-tool release
line after all Alpha 2 functionality, usability, documentation, and
authentication work is complete.

This is the final Alpha 2 milestone and release baseline review.

The goal is a current, maintained, reproducible, and vulnerability-free release
baseline, not blindly selecting the numerically newest major version.

### Phase 1 - Complete inventory and currency review

Status: completed on 2026-08-30. The reviewed baseline, dependency classes,
license/maintenance findings, official currency sources, reproducibility gaps,
and controlled Phase 2 decision queue are recorded in
`docs/dependency-inventory-alpha2.md`. No dependency or toolchain update was
applied in this inventory phase.

- inventory direct and transitive npm dependencies, Go modules, Docker base
  images, system packages, and pinned GitHub Actions from the final lockfiles,
  image, and SBOM,
- record installed, permitted, and latest available versions for direct
  dependencies and identify unsupported or unmaintained release lines,
- review the final Vite, React, Milkdown, TypeScript, ESLint, Vitest, jsdom,
  Tailwind, PWA, authentication, session, MFA, cryptographic, and SQLite-related
  versions explicitly,
- distinguish production runtime dependencies from build-, test-, and
  development-only tooling,
- review licenses and maintenance status for newly introduced Alpha 2
  dependencies.

### Phase 2 - Controlled updates

Status: completed on 2026-09-01. Node.js 24 LTS, compatible dependency updates,
Vite 8, ESLint 10, Vitest 4, jsdom 30, TypeScript 6, and Milkdown 7.22.1 were
adopted through isolated green migration blocks. RepoQuill's empty-cursor
inline-code extension was adapted to Milkdown's non-inclusive mark boundary and
is guarded by a regression test; reassess and remove that compatibility logic
only when a future Milkdown command natively provides the same start/type/stop
behavior. React plugin 6 and TypeScript 7 remain blocked by unresolved official
peer constraints. Go 1.26 remains an upstream-supported release line and was
retained to avoid a late Alpha 2 toolchain migration. Floating supported base
tags remain the fresh-patch input, while the release pipeline now validates and
promotes one exact immutable candidate digest instead of rebuilding after its
security gate.

- apply compatible patch and minor updates where their release notes and
  transitive changes are acceptable,
- evaluate major upgrades such as Vite, TypeScript, ESLint, Vitest, jsdom, or
  related plugins separately,
- never combine unrelated major upgrades merely to make the version inventory
  appear current,
- adopt a major upgrade only when its supported runtime requirements,
  compatibility, migration cost, maintenance status, and security benefit
  justify it,
- document any intentionally retained older major with its reason, support
  status, and a follow-up trigger,
- accept a maintained older release line when it has no release-blocking known
  vulnerabilities and upgrading would introduce disproportionate risk,
- regenerate and commit lockfiles reproducibly,
- ensure a clean install does not create uncommitted dependency changes,
- do not weaken scanner policy, suppress findings, or use force/legacy peer
  resolution merely to complete an upgrade.

### Phase 3 - Full Alpha 2 regression

Status: in progress. The complete local Go and frontend regression, race,
static-analysis, vulnerability, and production-build gates passed on 2026-09-01.
The final protected pull-request and exact container-candidate runs remain
required because this code server does not provide Docker.

Run the complete Milestone 20 pull-request and release checks after the final
dependency set is selected.

Exercise at minimum:

- Markdown loading and serialization,
- editor toolbar behavior,
- slash commands,
- note tabs,
- autosave and version checking,
- file and folder operations,
- Trash and restore,
- note history and restore,
- internal links,
- image upload and clipboard paste,
- asset cleanup,
- Milestone 22 image lightbox,
- search,
- multiple notebooks,
- Milestone 21 GitHub onboarding,
- SSH key handling,
- SSH host verification,
- Git synchronization,
- remote-change handling,
- guided conflict resolution,
- authentication,
- session expiration and renewal,
- password changes and recovery,
- TOTP enrollment and recovery,
- PWA installation,
- PWA authentication lifecycle,
- mobile layouts,
- persistence across container restart/replacement.

Do not treat successful compilation as sufficient regression coverage.

### Phase 4 - Final security and container gate

Status: in progress. The manual Alpha 2 security review found no Critical or
High application vulnerability. Combined conflict drafts were moved from
persistent `localStorage` to tab-scoped `sessionStorage` with authentication-end
cleanup. The release workflow now builds one immutable source-SHA manifest,
scans and smoke-tests both architectures from its exact digest, and promotes
only that digest. Hosted CodeQL, secret, Trivy, hardened-container, fresh-data,
upgrade, and rollback evidence remains mandatory before tagging.

- run the complete Milestone 19 authentication/security gate,
- run the complete Milestone 20 supply-chain/security gate,
- build the final production image from a clean checkout,
- build every supported CPU architecture,
- run container vulnerability scanning against the actual release image,
- run source/dependency vulnerability checks,
- run secret scanning,
- run CodeQL and other required static analysis,
- verify non-root runtime behavior,
- verify persistent-volume behavior,
- smoke-test the built image through the documented health and startup flow,
- test a fresh installation,
- test an upgrade using representative existing Alpha data,
- test rollback where documented.

Require zero unresolved known vulnerabilities at the configured release policy
threshold across npm, Go, container OS packages, CodeQL, Dependabot, and secret
scans.

A scanner or advisory-database failure must not be interpreted as a clean scan.

### Phase 5 - Exact release-candidate artifacts

Status: pending the immutable Alpha 2 tag and successful hosted release gate.

Generate release artifacts from the exact immutable candidate that passed the
gate.

Produce or verify:

- final SBOM,
- provenance/attestation,
- dependency inventory,
- container digest,
- architecture manifest,
- release notes,
- changelog version,
- documented image tags.

Do not generate security or dependency documentation from a different commit
than the artifact being released.

### Phase 6 - Final documentation verification

Status: source documentation reviewed and synchronized on 2026-09-01. The final
version, digest, release-note, changelog-date, and published-link pass remains
pending until the immutable release candidate has passed Phase 4.

Because Milestone 23 occurs before the dependency/toolchain review, verify that
the final dependency updates did not invalidate release documentation.

Recheck:

- runtime requirements,
- Docker examples,
- environment variables,
- authentication behavior,
- supported browser/runtime assumptions,
- upgrade instructions,
- image tags,
- version numbers,
- known limitations.

If Milestone 24 changes user-visible or operational behavior, update the
documentation before the release candidate is considered final.

### Milestone 24 completion criteria

- every direct dependency and toolchain component has been reviewed against its
  current maintained releases,
- accepted updates are tested and reflected reproducibly in lockfiles and the
  release image,
- retained older majors have a documented technical reason and remain supported
  without known release-blocking vulnerabilities,
- the final multi-architecture image and complete Alpha 2 application pass all
  Milestone 19 and 20 security and regression gates,
- Milestone 21 beginner-friendly onboarding passes final regression,
- Milestone 22 image-lightbox behavior passes final regression,
- Milestone 23 documentation matches the exact final release candidate,
- the final fresh-install, upgrade, persistence, recovery, authentication, Git,
  and PWA smoke tests succeed,
- the released SBOM accurately describes the dependency baseline shipped to
  users,
- Alpha 2 is published only after this milestone is complete.

## Alpha 2 completion criteria

Alpha 2 is ready when:

- the existing multiple-note tab workflow remains stable,
- normal deletion is recoverable through Trash,
- an earlier Git version of a note can be inspected and restored safely,
- portable internal links can be created, followed, and maintained across
  ordinary rename and move operations,
- notebooks can be renamed and safely removed from RepoQuill,
- overlapping file saves and Git synchronization conflicts can be completely
  resolved through a non-technical guided workflow,
- save, synchronization, external-change, and failure states are understandable
  without Git terminology,
- a private notebook can be connected through a guided, actionable onboarding
  flow,
- a single owner can securely authenticate with an optional second factor and
  recover access without placing notebook content under the auth system,
- authentication expiry is handled natively and safely in browser and PWA
  workflows,
- continuous dependency, source, secret, and published-image monitoring is
  active and proposes reviewed updates without unattended merges,
- the final dependency and toolchain baseline has been deliberately reviewed,
  updated where justified, and documented where a maintained older major is
  retained,
- all applicable Alpha 2 milestones work on desktop and mobile/PWA layouts where a user
  interface is applicable,
- destructive, path-sensitive, Markdown-rewriting, Git-history, and conflict
  recovery behavior plus synchronization wording and onboarding failures are
  covered by focused tests,
- Milestones 19, 20, and 21's dedicated security verification and release gates
  pass,
- no milestone introduces opaque canonical content or weakens Git/provider
  independence.

---

# 46. Alpha 3 Roadmap

The candidate ideas from this section are preserved in [`../roadmap/alpha3-candidates.md`](../roadmap/alpha3-candidates.md); verify their status before planning work.

# 47. Alpha Release Criteria

Before calling a build "alpha", verify:

- notes survive container replacement,
- repositories survive application deletion,
- a notebook can be cloned and read independently,
- Markdown is standards-friendly,
- images render outside the application,
- repository history is valid,
- failed push does not lose edits,
- path traversal is blocked,
- mobile layout is usable,
- image paste works,
- Docker volume behavior is documented,
- Git credentials are not leaked,
- no note content is stored only in SQLite,
- the default local authentication boundary fails closed,
- session expiry and operator recovery do not discard notebook content,
- the exact released container digest passed both supported-architecture gates.

---

# 48–49. Future feature ideas and portability scrutiny

Preserved with the candidate roadmap in [`../roadmap/alpha3-candidates.md`](../roadmap/alpha3-candidates.md).

# 50. Coding-Agent Rules

When an AI coding agent works on this project, it MUST:

1. Read this file before making architectural changes.
2. Prefer the simplest implementation that meets current requirements.
3. Avoid adding dependencies without need.
4. Avoid speculative abstractions.
5. Preserve plain Markdown compatibility.
6. Preserve provider-independent Git support.
7. Preserve one-repository-per-notebook semantics.
8. Preserve application-independent readability.
9. Avoid modifying unrelated code.
10. Keep commits focused.
11. Add tests for destructive or security-sensitive behavior.
12. Never silently change the canonical storage model.
13. Never replace filesystem/Git storage with a database.
14. Never introduce collaborative editing architecture without an explicit project decision.
15. Keep authentication within the explicit single-owner scope and Milestone 19 security requirements; do not add general account management without another project decision.
16. Never introduce a cloud dependency for core functionality.
17. Treat data loss risks as higher priority than UI convenience.
18. Ask for clarification only when a decision would materially alter architecture or risk data loss; otherwise choose the simplest interpretation consistent with this document.
19. Write a CHANGELOG.md in the Keep a Changelog format

---

# 51. Agent Change Checklist

Before finishing a change, verify:

- Does this preserve plain Markdown files?
- Are assets still normal files?
- Can the repository still be used without the app?
- Does this stay inside the notebook root?
- Can this operation lose data?
- Is Git failure separated from save failure?
- Did this introduce unnecessary infrastructure?
- Does this work reasonably on mobile?
- Does this create a hidden proprietary dependency?
- Is this feature actually required now?

If any answer is problematic, redesign before merging.

---

# 52. Project Philosophy

The application should stay boring in all the right ways.

Markdown is storage.

Folders are structure.

Git is history and synchronization.

The browser is the interface.

The application should provide convenience without taking ownership of the data.

The preferred failure mode is:

> The application disappears and the user still has a perfectly ordinary Git repository containing readable Markdown files and images.

That is the product.
