#!/usr/bin/env node
import { mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { dirname } from "node:path";

const [command, path, workflow, target, phase = "planned"] = process.argv.slice(2);
if (!command || !path || path.includes("..") || !path.includes(".cenci/checkpoints/")) process.exit(2);
const read = () => JSON.parse(readFileSync(path, "utf8"));
if (command === "get") { console.log(JSON.stringify(read())); process.exit(0); }
if (command === "clear") { rmSync(path, { force: true }); process.exit(0); }
if (!["init", "advance", "block", "complete"].includes(command)) process.exit(2);
let state = command === "init" ? { schemaVersion: 1, workflow, target, phase, status: "planned", planPath: null, worktree: null, lastCompletedGate: null, agentFallbacks: [] } : read();
if (command !== "init") state.phase = phase;
state.status = command === "block" ? "needs-input" : command === "complete" ? "complete" : "running";
state.updatedAt = new Date().toISOString();
mkdirSync(dirname(path), { recursive: true });
const temp = `${path}.tmp-${process.pid}`;
writeFileSync(temp, `${JSON.stringify(state, null, 2)}\n`, { mode: 0o600 });
renameSync(temp, path);
console.log(JSON.stringify(state));
