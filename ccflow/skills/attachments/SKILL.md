---
name: attachments
description: Canonical procedure for discovering, downloading, and loading ticket attachments
user-invocable: false
---

**Main-agent only**: This skill uses `AskUserQuestion` (Step 2) and must only be invoked from the main agent — never from subagents, where user interaction deadlocks silently.

## Attachment Handling

After fetching the ticket, check for attachments that may provide additional context (mockups, design specs, API docs, screenshots, etc.).

### Step 1: Discover Attachments

If the calling skill already provides a discovered attachment list (e.g. from the `context-gatherer` digest), use it and skip the scan below — continue at Step 2.

Scan `body` and each `comments[].body` for URLs matching these domain patterns, embedded with either `![alt](url)` (image embed) or `[text](url)` (link) markdown syntax:
- `https://user-images.githubusercontent.com/...`
- `https://github.com/<owner>/<repo>/assets/...`
- `https://github.com/user-attachments/files/...`
- `https://github.com/user-attachments/assets/...`

**Note**: `user-attachments/assets/` URLs are often extensionless. If embedded with `![...]()` syntax, classify as **image**; if embedded with `[...]()` syntax, classify as **document** (file download).

Extract the display name (alt text or link text, fallback to filename from URL) and URL. Size is unknown until download.

If **no attachments found** → skip the rest of this procedure and return to the calling skill.

### Step 2: Present to User

Classify each attachment by extension:
- **image**: png, jpg, jpeg, gif, svg, webp
- **document**: md, txt, json, yaml, yml, csv, xml, html, log, pdf
- **binary**: everything else

Present via `AskUserQuestion` with `multiSelect=true`:

> "Found N attachment(s) on this ticket. Which would you like to download for context?"

Options (one per attachment if ≤ 4, grouped by type if > 4):
- `"<name> (<type>, <size>)"` per attachment when ≤ 4
- `"All images (N)"`, `"All documents (N)"`, `"All other files (N)"` when > 4

If user selects none → skip the rest of this procedure and return to the calling skill.

### Step 3: Download Selected Attachments

```bash
mkdir -p /tmp/claude/attachments
```

```bash
curl -sL "<url>" -o "/tmp/claude/attachments/<filename>"
```
If download returns 404 (private repo), retry with auth:
```bash
curl -sL -H "Authorization: token $(gh auth token)" "<url>" -o "/tmp/claude/attachments/<filename>"
```

Post-download: check size with `stat`. If > 10 MB and user wasn't warned → report and skip that file.

On any download failure → warn and continue with remaining files.

### Step 4: Load Attachment Content

For each downloaded file, use the Read tool:
- **Images** (png, jpg, jpeg, gif, webp): Read tool renders visually (Claude is multimodal)
- **SVG**: Read as text
- **Text files** (md, txt, json, yaml, yml, csv, xml, html, log): Read content
- **PDF**: Read tool extracts PDF content
- **Binary/other**: Report file path, note content cannot be read as text

**After reading each image**, produce a brief structured summary (5–15 lines) covering applicable aspects:
- **Type**: mockup, wireframe, screenshot, diagram, flowchart, error state, etc.
- **Layout**: overall structure, regions, and spatial arrangement
- **UI elements**: buttons, inputs, tables, navigation, modals, etc. — include their labels
- **Text content**: headings, labels, error messages, data values visible in the image
- **Visual style**: colors, spacing, typography, and key design details
- **Annotations**: arrows, callouts, numbered markers, or highlighted areas

Keep all attachment context available for the rest of the calling skill's execution.
