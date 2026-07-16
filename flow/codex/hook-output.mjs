#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import { join } from "node:path";

const root = process.env.PLUGIN_ROOT;
const mode = process.argv[2];
if (!root || !["session", "compact", "stop"].includes(mode)) process.exit(2);

if (mode === "stop") {
  console.log(JSON.stringify({ systemMessage: "Capture only specific non-obvious lessons during implementation. Shared guidance belongs in AGENTS.md; otherwise propose the lesson without editing main." }));
  process.exit(0);
}

const script = mode === "session" ? "check-lessons-file.sh" : "preserve-context.sh";
const result = spawnSync(join(root, "hooks", "scripts", script), { encoding: "utf8" });
if (result.status !== 0) process.exit(result.status ?? 1);
const additionalContext = result.stdout.trim();
if (!additionalContext) { console.log("{}"); process.exit(0); }
console.log(JSON.stringify({ hookSpecificOutput: { hookEventName: mode === "session" ? "SessionStart" : "PreCompact", additionalContext } }));
