---
name: attachments
description: Discover, select, download, and inspect GitHub ticket attachments. Use when an issue or pull request contains screenshots, mockups, documents, or uploaded files needed as task context.
user-invocable: false
---

**Conversation-owning agent only**: selection requires user interaction in Step 2.
Never delegate this procedure to a worker/subagent that cannot surface questions to
the active conversation.

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

Use the client's available user-input mechanism and allow multiple selections:

> "Found N attachment(s) on this ticket. Which would you like to download for context?"

Options (one per attachment if ≤ 4, grouped by type if > 4):
- `"<name> (<type>, <size>)"` per attachment when ≤ 4
- `"All images (N)"`, `"All documents (N)"`, `"All other files (N)"` when > 4

If user selects none → skip the rest of this procedure and return to the calling skill.

### Step 3: Download Selected Attachments

Store downloads under `${TMPDIR:-/tmp}/agentflow/attachments/<scope>` — a per-run
subdirectory, where `<scope>` is the calling skill's run identifier (ticket id/slug or
run id), so concurrent runs never pile same-named downloads together. Create the
directory with the client's filesystem tool or a standalone shell command.

```bash
mkdir -p "${TMPDIR:-/tmp}/agentflow/attachments/<scope>"
```

Prefer a connected GitHub attachment-download tool when one is available, especially
for private `user-attachments` URLs. Otherwise use `curl`:

```bash
curl -fsSL "<url>" -o "${TMPDIR:-/tmp}/agentflow/attachments/<scope>/<filename>"
```

If a private-repository download fails and no connector can download it, retry with
the authenticated GitHub CLI token without printing the token.

Post-download: check size with `stat`. If > 10 MB and user wasn't warned → report and skip that file.

On any download failure → warn and continue with remaining files.

### Step 4: Load Attachment Content

For each downloaded file, use the client's matching file or image inspection tool:
- **Images** (png, jpg, jpeg, gif, webp): use multimodal image inspection
- **SVG**: read as text unless visual rendering is specifically needed
- **Text files** (md, txt, json, yaml, yml, csv, xml, html, log): read as text
- **PDF**: use the client's PDF reader or extraction support
- **Binary/other**: Report file path, note content cannot be read as text

**After reading each image**, produce a brief structured summary (5–15 lines) covering applicable aspects:
- **Type**: mockup, wireframe, screenshot, diagram, flowchart, error state, etc.
- **Layout**: overall structure, regions, and spatial arrangement
- **UI elements**: buttons, inputs, tables, navigation, modals, etc. — include their labels
- **Text content**: headings, labels, error messages, data values visible in the image
- **Visual style**: colors, spacing, typography, and key design details
- **Annotations**: arrows, callouts, numbered markers, or highlighted areas

Keep all attachment context available for the rest of the calling skill's execution.
