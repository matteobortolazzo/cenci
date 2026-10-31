#!/usr/bin/env node
// Native Codex workflow checkpoint (schema v2). Standard library only.
//
//   checkpoint.mjs <init|advance|block|complete|get|clear> <path> [<workflow> <target> [<phase>]]
//                  [--plan-path P --plan-digest D] [--worktree W] [--gate G]
//                  [--fallback role:worker]... [--goal created|skipped-active|unavailable] [--force]
//   checkpoint.mjs verify <path> <workflow> <target>   (read-only: approved-plan handoff check)
//   checkpoint.mjs digest <plan-path>                  (read-only: sha256 of a .plans/ artifact)
//
// Exit codes: 0 success, 1 unexpected I/O failure, 2 rejection or usage error, 3 missing state.
// Every error is one `checkpoint: <file>: <problem>; <remedy>` line on stderr: no stack traces,
// no echoed file content. Nothing is written to stdout unless the command succeeded.
import fs from "node:fs";
import path from "node:path";
import { execFileSync } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";

const WORKFLOW_PHASES = {
  implement: ["planned", "worktree", "baseline", "tests", "implement", "refactor", "review", "docs", "pr"],
  maintain: ["planned", "worktree", "baseline", "fix", "review", "pr"],
  refactor: ["planned", "worktree", "baseline", "refactor", "review", "pr"],
  "address-review": ["planned", "worktree", "fix", "verify", "push"],
  configure: ["planned", "apply", "pr"],
  refine: ["planned", "apply"],
  review: ["planned", "review", "post"],
  sync: ["planned", "sync"],
};
const STATUSES = ["planned", "running", "needs-input", "complete"];
const GOALS = ["created", "skipped-active", "unavailable"];
const COMMANDS = ["init", "advance", "block", "complete", "get", "clear", "verify", "digest"];
const V2_KEYS = [
  "schemaVersion", "workflow", "target", "phase", "status", "planPath", "planDigest",
  "worktree", "lastCompletedGate", "agentFallbacks", "goal", "updatedAt",
];
const VALUE_FLAGS = { "plan-path": "planPath", "plan-digest": "planDigest", worktree: "worktree", gate: "gate", fallback: "fallback", goal: "goal" };
const FLAG_NAMES = Object.fromEntries(Object.entries(VALUE_FLAGS).map(([name, key]) => [key, name]));
const META_FLAGS = ["planPath", "planDigest", "worktree", "gate", "fallback", "goal"];
const FLAGS_BY_COMMAND = {
  init: META_FLAGS, advance: META_FLAGS, block: META_FLAGS, complete: META_FLAGS, get: [], clear: ["force"],
  verify: [], digest: [],
};

const TARGET_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const NAME_RE = /^[a-z][a-z0-9-]*$/;
const DIGEST_RE = /^[0-9a-f]{64}$/;
const ISO_RE = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/;
const MAX_FILE_BYTES = 1 << 20;
const USAGE = "checkpoint.mjs <init|advance|block|complete|get|clear> <path> [<workflow> <target> [<phase>]] [flags] | verify <path> <workflow> <target> | digest <plan-path>";

class Failure extends Error {
  constructor(exitCode, message) {
    super(message);
    this.exitCode = exitCode;
  }
}

const printable = (text) => String(text).replace(/[\u0000-\u001f\u007f]/g, "?");
const fail = (exitCode, label, problem, remedy) =>
  new Failure(exitCode, `checkpoint: ${printable(label)}: ${problem}${remedy ? `; ${remedy}` : ""}`);
const usageError = (problem) => new Failure(2, `checkpoint: usage: ${problem}; ${USAGE}`);

const isObject = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
const hasOwn = (object, key) => Object.prototype.hasOwnProperty.call(object, key);

// ---------------------------------------------------------------------------
// Argument parsing (hand-rolled: util.parseArgs is not stable across Node 18.x).
// ---------------------------------------------------------------------------
function parseArgs(argv) {
  const positionals = [];
  const flags = { fallback: [] };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (!arg.startsWith("--")) {
      positionals.push(arg);
      continue;
    }
    let name = arg.slice(2);
    let value;
    const eq = name.indexOf("=");
    if (eq >= 0) {
      value = name.slice(eq + 1);
      name = name.slice(0, eq);
    }
    if (name === "force") {
      if (value !== undefined) throw usageError("--force takes no value");
      if (hasOwn(flags, "force")) throw usageError("--force given more than once");
      flags.force = true;
      continue;
    }
    if (!hasOwn(VALUE_FLAGS, name)) throw usageError(`unknown flag --${printable(name)}`);
    if (value === undefined) {
      i += 1;
      value = argv[i];
      if (value === undefined || value.startsWith("--")) throw usageError(`--${name} requires a value`);
    }
    const key = VALUE_FLAGS[name];
    if (key === "fallback") {
      flags.fallback.push(value);
    } else {
      if (hasOwn(flags, key)) throw usageError(`--${name} given more than once`);
      flags[key] = value;
    }
  }
  return { positionals, flags };
}

// ---------------------------------------------------------------------------
// Value validation.
// ---------------------------------------------------------------------------
function validRelativePath(value) {
  return (
    typeof value === "string" && value.length > 0 && value.length <= 4096 &&
    !/[\u0000-\u001f\u007f\\]/.test(value) &&
    !value.startsWith("/") && !value.endsWith("/") && value !== "." &&
    path.posix.normalize(value) === value &&
    !value.split("/").includes("..") &&
    value.split("/")[0].toLowerCase() !== ".git"
  );
}

function parseFallback(raw) {
  const parts = raw.split(":");
  if (parts.length !== 2 || !NAME_RE.test(parts[0]) || !NAME_RE.test(parts[1])) {
    throw usageError("invalid --fallback (expected <role>:<worker>, both matching [a-z][a-z0-9-]*)");
  }
  return { role: parts[0], worker: parts[1] };
}

function validateMetadata(command, flags, workflow) {
  for (const key of [...META_FLAGS, "force"]) {
    const present = key === "fallback" ? flags.fallback.length > 0 : hasOwn(flags, key);
    if (present && !FLAGS_BY_COMMAND[command].includes(key)) {
      throw usageError(`--${FLAG_NAMES[key] ?? key} is not valid for ${command}`);
    }
  }
  const meta = {};
  if (hasOwn(flags, "planPath") !== hasOwn(flags, "planDigest")) {
    throw usageError("--plan-path and --plan-digest must be given together");
  }
  if (hasOwn(flags, "planPath")) {
    if (!validRelativePath(flags.planPath)) throw usageError("invalid --plan-path (expected a normalized repo-relative path)");
    if (!DIGEST_RE.test(flags.planDigest)) throw usageError("invalid --plan-digest (expected 64 lowercase hex characters)");
    meta.planPath = flags.planPath;
    meta.planDigest = flags.planDigest;
  }
  if (hasOwn(flags, "worktree")) {
    if (!validRelativePath(flags.worktree)) throw usageError("invalid --worktree (expected a normalized repo-relative path)");
    meta.worktree = flags.worktree;
  }
  if (hasOwn(flags, "gate")) {
    if (!workflow || !WORKFLOW_PHASES[workflow].includes(flags.gate)) {
      throw usageError("invalid --gate (expected a phase of the workflow)");
    }
    meta.gate = flags.gate;
  }
  if (hasOwn(flags, "goal")) {
    if (!GOALS.includes(flags.goal)) throw usageError(`invalid --goal (expected one of ${GOALS.join(", ")})`);
    meta.goal = flags.goal;
  }
  meta.fallbacks = flags.fallback.map(parseFallback);
  return meta;
}

function identityFrom(positionals, command) {
  const [workflow, target, phase, ...extra] = positionals;
  const needIdentity = command !== "get";
  if (workflow === undefined && target === undefined) {
    if (needIdentity) throw usageError(`${command} requires <workflow> <target>`);
    return {};
  }
  if (target === undefined) throw usageError("<workflow> and <target> must be given together");
  if (extra.length > 0) throw usageError("too many arguments");
  if (!hasOwn(WORKFLOW_PHASES, workflow)) {
    throw usageError(`unknown workflow (expected one of ${Object.keys(WORKFLOW_PHASES).join(", ")})`);
  }
  if (!TARGET_RE.test(target) || target.includes("..")) {
    throw usageError("invalid target (expected [A-Za-z0-9][A-Za-z0-9._-]{0,127}, no '..')");
  }
  const phases = WORKFLOW_PHASES[workflow];
  if (phase !== undefined) {
    if (command === "get" || command === "clear" || command === "verify") throw usageError(`${command} takes no <phase>`);
    if (!phases.includes(phase)) {
      throw usageError(`"${printable(phase)}" is not a phase of workflow ${workflow} (expected one of ${phases.join(", ")})`);
    }
    if (command === "init" && phase !== "planned") throw usageError("init only accepts the planned phase");
  } else if (command === "advance" || command === "block") {
    throw usageError(`${command} requires <phase>`);
  }
  return { workflow, target, phase };
}

// ---------------------------------------------------------------------------
// State root and containment.
// ---------------------------------------------------------------------------
function resolveMainCheckout(label) {
  let common;
  try {
    common = execFileSync("git", ["rev-parse", "--path-format=absolute", "--git-common-dir"], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    }).trim();
  } catch {
    throw fail(2, label, "not inside a git repository (or git is unavailable)", "run it from the repository, a worktree or the sandbox");
  }
  let real;
  try {
    real = fs.realpathSync(common);
  } catch (err) {
    throw fail(2, label, `cannot resolve the git directory (${err.code ?? "unknown"})`, "run it from a normal checkout");
  }
  if (path.basename(real) !== ".git") {
    throw fail(2, label, "unsupported repository layout (no main checkout)", "bare repositories cannot hold checkpoints");
  }
  return fs.realpathSync(path.dirname(real));
}

// true when <dir> exists as a plain directory, false when absent; a symlink or file is refused.
function plainDirectory(dir, label) {
  let st;
  try {
    st = fs.lstatSync(dir);
  } catch (err) {
    if (err.code === "ENOENT") return false;
    throw err;
  }
  if (!st.isDirectory()) {
    throw fail(2, label, "state directory is not a plain directory (symlinks are refused)", "remove it by hand if it is stale");
  }
  return true;
}

function realpathAllowMissing(target, label) {
  const tail = [];
  let current = target;
  for (;;) {
    try {
      return path.join(fs.realpathSync(current), ...tail);
    } catch (err) {
      if (err.code === "ELOOP") throw fail(2, label, "path crosses a symlink loop", "use the documented .cenci/checkpoints path");
      if (err.code !== "ENOENT" && err.code !== "ENOTDIR") throw err;
      const parent = path.dirname(current);
      if (parent === current) throw err;
      tail.unshift(path.basename(current));
      current = parent;
    }
  }
}

function locate(rawPath, label, identity) {
  if (rawPath.includes("\0")) throw fail(2, label, "path contains a NUL byte", "use the documented .cenci/checkpoints path");
  if (rawPath.split("/").includes("..")) {
    throw fail(2, label, "path contains a '..' path segment", "use the documented .cenci/checkpoints/<workflow>-<target>.json path");
  }
  if (rawPath.endsWith("/")) throw fail(2, label, "path names a directory", "use the documented .cenci/checkpoints/<workflow>-<target>.json path");
  const main = resolveMainCheckout(label);
  const cenci = path.join(main, ".cenci");
  const root = path.join(cenci, "checkpoints");
  if (plainDirectory(cenci, label)) plainDirectory(root, label);
  const absolute = path.resolve(main, rawPath);
  const base = path.basename(absolute);
  if (realpathAllowMissing(path.dirname(absolute), label) !== root) {
    throw fail(2, label, "path is outside the checkpoint root", `use ${path.join(".cenci", "checkpoints", "<workflow>-<target>.json")} in the main checkout`);
  }
  if (identity.workflow !== undefined && base !== `${identity.workflow}-${identity.target}.json`) {
    throw fail(2, label, "path does not match workflow and target", `the file name must be ${identity.workflow}-${identity.target}.json`);
  }
  return { main, cenci, root, base, file: path.join(root, base), label };
}

function ensureRoot(loc) {
  for (const dir of [loc.cenci, loc.root]) {
    if (plainDirectory(dir, loc.label)) continue;
    try {
      fs.mkdirSync(dir, dir === loc.root ? { mode: 0o700 } : undefined);
    } catch (err) {
      if (err.code !== "EEXIST") throw err;
    }
    plainDirectory(dir, loc.label);
  }
  if (fs.realpathSync(loc.root) !== loc.root) {
    throw fail(2, loc.label, "path is outside the checkpoint root", "the state directory resolves elsewhere");
  }
  const ignoreFile = path.join(loc.root, ".gitignore");
  let fd;
  try {
    fd = fs.openSync(ignoreFile, fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_NOFOLLOW, 0o644);
  } catch (err) {
    if (err.code === "EEXIST") return; // an existing .gitignore is never overwritten
    throw err;
  }
  try {
    writeAll(fd, "*\n");
    fs.closeSync(fd);
  } catch (err) {
    try { fs.closeSync(fd); } catch { /* the original error is reported */ }
    try { fs.unlinkSync(ignoreFile); } catch { /* remove the partial file so a later init retries */ }
    throw err;
  }
}

// A short write is a failure: the caller must never commit a truncated file.
function writeAll(fd, body) {
  if (fs.writeSync(fd, body) !== Buffer.byteLength(body)) {
    throw Object.assign(new Error("short write"), { code: "EIO", syscall: "write" });
  }
}

// ---------------------------------------------------------------------------
// Reading and validating state.
// ---------------------------------------------------------------------------
function readExisting(loc) {
  let st;
  try {
    st = fs.lstatSync(loc.file);
  } catch (err) {
    if (err.code === "ENOENT" || err.code === "ENOTDIR") return null;
    throw err;
  }
  const notRegular = () => fail(2, loc.label, "checkpoint is not a regular file (symlinks are refused)", "remove it by hand if it is stale");
  if (!st.isFile()) throw notRegular();
  let fd;
  try {
    // O_NONBLOCK: a FIFO swapped in after the lstat check must fail the isFile test below, not block on open.
    fd = fs.openSync(loc.file, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW | fs.constants.O_NONBLOCK);
  } catch (err) {
    if (err.code === "ENOENT") return null;
    if (err.code === "ELOOP") throw notRegular();
    throw err;
  }
  try {
    const opened = fs.fstatSync(fd);
    if (!opened.isFile()) throw fail(2, loc.label, "checkpoint is not a regular file", "remove it by hand if it is stale");
    const content = readCapped(fd);
    return { text: content === null ? null : content.toString("utf8") };
  } finally {
    fs.closeSync(fd);
  }
}

// Reads at most one byte past the cap, so a file that grows after fstat is still bounded.
// -> the content, or null when it exceeds MAX_FILE_BYTES.
function readCapped(fd) {
  const buffer = Buffer.alloc(MAX_FILE_BYTES + 1);
  let total = 0;
  while (total < buffer.length) {
    const count = fs.readSync(fd, buffer, total, buffer.length - total, null);
    if (count === 0) break;
    total += count;
  }
  return total > MAX_FILE_BYTES ? null : buffer.subarray(0, total);
}

// The approved-plan artifact reader shared by `verify` and `digest`: a repo-relative regular file
// under .plans/ of the main checkout, no symlinked component, read within the size cap.
// -> { digest: <64 hex>, absolute }
function readArtifact(main, rel, label) {
  if (!validRelativePath(rel) || !rel.startsWith(".plans/")) {
    throw fail(2, label, "plan artifact path is not under .plans/", "use a repo-relative path such as .plans/<name>.md");
  }
  const missing = () => fail(2, label, "plan artifact missing", "plan in Plan mode, then persist");
  const unreadable = () => fail(2, label, "plan artifact unreadable", "plan in Plan mode, then persist again to rewrite it");
  const absolute = path.join(main, rel);
  let realDir;
  try {
    realDir = fs.realpathSync(path.dirname(absolute));
  } catch (err) {
    throw err.code === "ENOENT" || err.code === "ENOTDIR" ? missing() : unreadable();
  }
  // main is already a realpath, so any difference means a symlinked directory component.
  if (realDir !== path.dirname(absolute)) throw unreadable();
  let fd;
  try {
    // O_NOFOLLOW refuses a symlinked leaf (ELOOP); O_NONBLOCK keeps a FIFO from blocking the open.
    fd = fs.openSync(absolute, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW | fs.constants.O_NONBLOCK);
  } catch (err) {
    throw err.code === "ENOENT" ? missing() : unreadable();
  }
  try {
    if (!fs.fstatSync(fd).isFile()) throw unreadable();
    const content = readCapped(fd);
    if (content === null) throw unreadable();
    return { digest: createHash("sha256").update(content).digest("hex"), absolute };
  } catch (err) {
    throw err instanceof Failure ? err : unreadable();
  } finally {
    fs.closeSync(fd);
  }
}

function validateV2(state) {
  const keys = Object.keys(state);
  if (keys.length !== V2_KEYS.length || !V2_KEYS.every((key) => hasOwn(state, key))) return "unexpected or missing fields";
  const phases = hasOwn(WORKFLOW_PHASES, state.workflow) ? WORKFLOW_PHASES[state.workflow] : null;
  if (typeof state.workflow !== "string" || phases === null) return "invalid field workflow";
  if (typeof state.target !== "string" || !TARGET_RE.test(state.target) || state.target.includes("..")) return "invalid field target";
  if (!phases.includes(state.phase)) return "invalid field phase";
  if (!STATUSES.includes(state.status)) return "invalid field status";
  if (state.planPath !== null && !validRelativePath(state.planPath)) return "invalid field planPath";
  if (state.planDigest !== null && (typeof state.planDigest !== "string" || !DIGEST_RE.test(state.planDigest) || state.planPath === null)) return "invalid field planDigest";
  if (state.worktree !== null && !validRelativePath(state.worktree)) return "invalid field worktree";
  if (state.lastCompletedGate !== null && (!phases.includes(state.lastCompletedGate) || phases.indexOf(state.lastCompletedGate) > phases.indexOf(state.phase))) {
    return "invalid field lastCompletedGate";
  }
  if (!Array.isArray(state.agentFallbacks)) return "invalid field agentFallbacks";
  const roles = new Set();
  for (const entry of state.agentFallbacks) {
    if (!isObject(entry) || Object.keys(entry).length !== 2 || !NAME_RE.test(entry.role ?? "") || !NAME_RE.test(entry.worker ?? "") || roles.has(entry.role)) {
      return "invalid field agentFallbacks";
    }
    roles.add(entry.role);
  }
  if (state.goal !== null && !GOALS.includes(state.goal)) return "invalid field goal";
  if (typeof state.updatedAt !== "string" || !ISO_RE.test(state.updatedAt) || Number.isNaN(Date.parse(state.updatedAt)) || new Date(state.updatedAt).toISOString() !== state.updatedAt) {
    return "invalid field updatedAt";
  }
  return null;
}

function validateV1Identity(state) {
  if (typeof state.workflow !== "string" || !/^[a-z][a-z-]*$/.test(state.workflow)) return "invalid field workflow";
  if (typeof state.target !== "string" || !TARGET_RE.test(state.target) || state.target.includes("..")) return "invalid field target";
  return null;
}

// -> { kind: "v2" | "v1" | "invalid" | "unknown", state?, problem? }
function inspect(text) {
  let value;
  if (text === null) return { kind: "invalid", problem: "checkpoint file is too large" };
  try {
    value = JSON.parse(text);
  } catch {
    return { kind: "invalid", problem: "malformed JSON" };
  }
  if (!isObject(value)) return { kind: "invalid", problem: "malformed JSON (not an object)" };
  if (value.schemaVersion === 2) {
    const problem = validateV2(value);
    return problem === null ? { kind: "v2", state: value } : { kind: "invalid", problem };
  }
  if (value.schemaVersion === 1) {
    const problem = validateV1Identity(value);
    return problem === null ? { kind: "v1", state: value } : { kind: "invalid", problem };
  }
  return { kind: "unknown", problem: "unknown schemaVersion" };
}

// Reads and classifies the checkpoint; returns null when it does not exist.
// An unreadable or unrecognized file raises, except for `clear`, which handles it itself.
function load(loc, identity, { tolerant = false } = {}) {
  const existing = readExisting(loc);
  if (existing === null) return null;
  const info = inspect(existing.text);
  if (info.kind === "invalid" || info.kind === "unknown") {
    if (tolerant) return info;
    throw fail(2, loc.label, info.problem, "inspect it, or remove it with: checkpoint.mjs clear <path> <workflow> <target> --force");
  }
  const { state } = info;
  if (loc.base !== `${state.workflow}-${state.target}.json` ||
      (identity.workflow !== undefined && (state.workflow !== identity.workflow || state.target !== identity.target))) {
    throw fail(2, loc.label, "checkpoint records a different workflow or target than requested", "it is never reset silently; clear cannot remove it either, so remove the file by hand if it is stale");
  }
  return info;
}

// ---------------------------------------------------------------------------
// Writing.
// ---------------------------------------------------------------------------
function assertRootStillContained(loc) {
  plainDirectory(loc.cenci, loc.label);
  if (!plainDirectory(loc.root, loc.label) || fs.realpathSync(loc.root) !== loc.root) {
    throw fail(2, loc.label, "path is outside the checkpoint root", "the state directory changed while writing");
  }
}

function writeAtomic(loc, state) {
  const body = `${JSON.stringify(state, null, 2)}\n`;
  const temp = path.join(loc.root, `.${loc.base}.tmp-${process.pid}-${randomBytes(6).toString("hex")}`);
  let fd;
  try {
    fd = fs.openSync(temp, fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_NOFOLLOW, 0o600);
    writeAll(fd, body);
    fs.fsyncSync(fd);
    fs.closeSync(fd);
    fd = undefined;
    assertRootStillContained(loc);
    fs.renameSync(temp, loc.file);
  } catch (err) {
    if (fd !== undefined) {
      try { fs.closeSync(fd); } catch { /* the original error is reported */ }
    }
    try { fs.unlinkSync(temp); } catch { /* the temp file may not exist yet */ }
    if (err instanceof Failure) throw err;
    throw fail(1, loc.label, `write failed (${err.code ?? "unknown"})`, "the previous checkpoint is unchanged");
  }
}

// ---------------------------------------------------------------------------
// State construction.
// ---------------------------------------------------------------------------
const nowIso = () => new Date().toISOString();

function phaseIndex(workflow, phase) {
  return phase === null ? -1 : WORKFLOW_PHASES[workflow].indexOf(phase);
}

function mergeFallbacks(current, additions) {
  const merged = current.map((entry) => ({ role: entry.role, worker: entry.worker }));
  for (const entry of additions) {
    const existing = merged.find((candidate) => candidate.role === entry.role);
    if (existing) existing.worker = entry.worker;
    else merged.push({ role: entry.role, worker: entry.worker });
  }
  return merged;
}

// Applies metadata flags and the gate rule; `phase` is the phase the state will carry.
function withMetadata(base, meta, phase, loc) {
  const next = { ...base };
  if (meta.planPath !== undefined) {
    next.planPath = meta.planPath;
    next.planDigest = meta.planDigest;
  }
  if (meta.worktree !== undefined) next.worktree = meta.worktree;
  if (meta.goal !== undefined) next.goal = meta.goal;
  next.agentFallbacks = mergeFallbacks(base.agentFallbacks, meta.fallbacks);
  if (meta.gate !== undefined) {
    const wanted = phaseIndex(base.workflow, meta.gate);
    if (wanted < phaseIndex(base.workflow, base.lastCompletedGate)) {
      throw fail(2, loc.label, "lastCompletedGate cannot move backwards", "pass a gate at or after the recorded one");
    }
    if (wanted > phaseIndex(base.workflow, phase)) {
      throw fail(2, loc.label, "lastCompletedGate cannot be later than the phase", "pass a gate at or before the new phase");
    }
    next.lastCompletedGate = meta.gate;
  }
  return next;
}

function ordered(state) {
  const result = {};
  for (const key of V2_KEYS) result[key] = state[key];
  return result;
}

function freshState(identity, meta, loc) {
  const base = {
    schemaVersion: 2, workflow: identity.workflow, target: identity.target, phase: "planned", status: "planned",
    planPath: null, planDigest: null, worktree: null, lastCompletedGate: null, agentFallbacks: [], goal: null,
    updatedAt: nowIso(),
  };
  return ordered(withMetadata(base, meta, "planned", loc));
}

function upgradeV1(v1, meta, loc) {
  const cannot = (problem) => fail(2, loc.label, `legacy v1 checkpoint cannot be upgraded (${problem})`, "remove it with: checkpoint.mjs clear <path> <workflow> <target>, then init again");
  const phases = hasOwn(WORKFLOW_PHASES, v1.workflow) ? WORKFLOW_PHASES[v1.workflow] : null;
  if (phases === null) throw cannot("workflow is not in the phase table");
  if (!phases.includes(v1.phase)) throw cannot("phase is not in the phase table");
  if (!STATUSES.includes(v1.status)) throw cannot("status is not recognized");
  const gate = v1.lastCompletedGate ?? null;
  if (gate !== null && !phases.includes(gate)) throw cannot("last completed gate is not in the phase table");
  const relativize = (value, name) => {
    if (value === null || value === undefined) return null;
    if (typeof value !== "string") throw cannot(`${name} is not a path`);
    if (path.isAbsolute(value)) {
      const inside = path.relative(loc.main, realpathAllowMissing(path.normalize(value), loc.label));
      if (inside === "" || inside === ".." || inside.startsWith(`..${path.sep}`) || path.isAbsolute(inside)) {
        throw cannot(`${name} is outside the checkout`);
      }
      value = inside.split(path.sep).join("/");
    }
    if (!validRelativePath(value)) throw cannot(`${name} is not a repo-relative path`);
    return value;
  };
  const fallbacks = Array.isArray(v1.agentFallbacks) ? v1.agentFallbacks : null;
  if (fallbacks === null || !fallbacks.every((entry) => isObject(entry) && NAME_RE.test(entry.role ?? "") && NAME_RE.test(entry.worker ?? ""))) {
    throw cannot("agent fallbacks are not a list of {role, worker}");
  }
  const base = {
    schemaVersion: 2, workflow: v1.workflow, target: v1.target, phase: v1.phase, status: v1.status,
    planPath: relativize(v1.planPath, "plan path"), planDigest: null, worktree: relativize(v1.worktree, "worktree"),
    lastCompletedGate: gate, agentFallbacks: fallbacks, goal: null, updatedAt: nowIso(),
  };
  const upgraded = ordered(withMetadata(base, meta, v1.phase, loc));
  const problem = validateV2(upgraded);
  if (problem !== null) throw cannot(problem);
  return upgraded;
}

function transition(state, command, phaseArg, meta, loc) {
  if (state.status === "complete") {
    throw fail(2, loc.label, "checkpoint is complete (terminal)", "only clear resets it: checkpoint.mjs clear <path> <workflow> <target>");
  }
  const phase = phaseArg ?? state.phase;
  const gate = phaseIndex(state.workflow, state.lastCompletedGate);
  const target = phaseIndex(state.workflow, phase);
  if (command === "complete") {
    if (target < gate) {
      throw fail(2, loc.label, `phase ${phase} must not come before the last completed gate`, "complete at or after the recorded gate");
    }
  } else {
    if (phase === "planned") throw fail(2, loc.label, "advance/block cannot target the planned phase", "only init sets planned");
    if (target <= gate) {
      throw fail(2, loc.label, `phase ${phase} must come after the last completed gate`, "choose a later phase of the workflow");
    }
  }
  const status = command === "advance" ? "running" : command === "block" ? "needs-input" : "complete";
  const next = withMetadata({ ...state, phase, status }, meta, phase, loc);
  next.updatedAt = nowIso();
  return ordered(next);
}

// ---------------------------------------------------------------------------
// Commands.
// ---------------------------------------------------------------------------
const out = (value) => process.stdout.write(`${JSON.stringify(value)}\n`);
const missingState = (loc) => fail(3, loc.label, "no checkpoint found", "run init first");
const NEEDS_FORCE = "re-run with --force to remove it";

function run(argv) {
  const { positionals, flags } = parseArgs(argv);
  const [command, rawPath, ...rest] = positionals;
  if (command === undefined || !COMMANDS.includes(command)) throw usageError(`unknown command${command === undefined ? "" : ` "${printable(command)}"`}`);
  if (rawPath === undefined) throw usageError(`${command} requires ${command === "digest" ? "<plan-path>" : "<path>"}`);

  if (command === "digest") {
    if (rest.length > 0) throw usageError("too many arguments");
    validateMetadata(command, flags, undefined); // digest takes no flags
    const result = readArtifact(resolveMainCheckout(rawPath), rawPath, rawPath);
    process.stdout.write(`${result.digest}\n`);
    return;
  }

  const identity = identityFrom(rest, command);
  const meta = validateMetadata(command, flags, identity.workflow);
  const loc = locate(rawPath, rawPath, identity);

  if (command === "verify") {
    const info = load(loc, identity);
    if (info === null) throw fail(3, loc.label, "no checkpoint found", "no approved plan was persisted — plan in Plan mode, then persist");
    if (info.kind === "v1") throw fail(2, loc.label, "legacy v1 checkpoint records no approved plan", "plan in Plan mode, then persist");
    const { state } = info;
    if (state.status === "complete") throw fail(2, loc.label, "checkpoint is complete (terminal)", "plan in Plan mode, then persist a new plan");
    if (state.planPath === null || state.planDigest === null) {
      throw fail(2, loc.label, "checkpoint records no approved plan", "plan in Plan mode, then persist");
    }
    const artifact = readArtifact(loc.main, state.planPath, loc.label);
    if (artifact.digest !== state.planDigest) {
      throw fail(2, loc.label, "plan artifact changed since approval (digest mismatch)", "plan in Plan mode, then persist again");
    }
    // A null gate means only the persist step (phase planned) is done, so work starts after it.
    const nextPhase = WORKFLOW_PHASES[state.workflow][Math.max(phaseIndex(state.workflow, state.lastCompletedGate), 0) + 1] ?? null;
    out({
      ...state,
      resolved: {
        mainCheckout: loc.main,
        planPath: artifact.absolute,
        worktree: state.worktree === null ? null : path.join(loc.main, state.worktree),
      },
      nextPhase,
    });
    return;
  }

  if (command === "get") {
    const info = load(loc, identity);
    if (info === null) throw missingState(loc);
    out(info.kind === "v1" ? { ...info.state, legacy: true } : info.state);
    return;
  }

  if (command === "clear") {
    const info = load(loc, identity, { tolerant: true });
    if (info === null) return;
    const unreadable = info.kind === "invalid" || info.kind === "unknown";
    if (unreadable && !flags.force) throw fail(2, loc.label, info.problem, NEEDS_FORCE);
    try {
      fs.unlinkSync(loc.file);
    } catch (err) {
      if (err.code !== "ENOENT") throw err; // vanished since the load: missing is success
      return;
    }
    if (unreadable) {
      process.stderr.write(`checkpoint: ${printable(loc.label)}: removed unreadable checkpoint (${info.problem}) because of --force\n`);
    }
    return;
  }

  if (command === "init") {
    const info = load(loc, identity);
    if (info === null) {
      const state = freshState(identity, meta, loc); // validated before anything is created
      ensureRoot(loc);
      writeAtomic(loc, state);
      out(state);
      return;
    }
    if (info.kind === "v1") {
      const upgraded = upgradeV1(info.state, meta, loc);
      writeAtomic(loc, upgraded);
      out(upgraded);
      return;
    }
    const { state } = info;
    if (meta.planDigest !== undefined) {
      if (state.planDigest === null) {
        throw fail(2, loc.label, "existing checkpoint records no plan digest", "use advance with --plan-path and --plan-digest to record it");
      }
      if (state.planDigest !== meta.planDigest || state.planPath !== meta.planPath) {
        throw fail(2, loc.label, "plan path or digest differs from the existing checkpoint", "it is never reset silently; clear it first if the plan changed");
      }
    }
    out(state);
    return;
  }

  const info = load(loc, identity);
  if (info === null) throw missingState(loc);
  if (info.kind === "v1") {
    throw fail(2, loc.label, "legacy v1 checkpoint; run init to upgrade it", "checkpoint.mjs init <path> <workflow> <target> upgrades it in place");
  }
  const next = transition(info.state, command, identity.phase, meta, loc);
  writeAtomic(loc, next);
  out(next);
}

// A stdout failure (EPIPE) after the commit is one checkpoint: line, never a stack trace.
let stdoutReported = false;
process.stdout.on("error", (err) => {
  if (stdoutReported) return;
  stdoutReported = true;
  const code = typeof err?.code === "string" ? printable(err.code) : "unknown";
  process.stderr.write(`checkpoint: stdout: write failed (${code}); any checkpoint change was already committed before it\n`);
  process.exitCode = 1;
});

const ERRNO_RE = /^E[A-Z0-9]+$/;

try {
  run(process.argv.slice(2));
} catch (err) {
  if (err instanceof Failure) {
    process.stderr.write(`${err.message}\n`);
    process.exitCode = err.exitCode;
  } else if (typeof err?.code === "string" && ERRNO_RE.test(err.code)) {
    const syscall = typeof err.syscall === "string" ? ` ${printable(err.syscall)}` : "";
    process.stderr.write(`checkpoint: unexpected I/O failure (${err.code}${syscall}); no checkpoint state was reported\n`);
    process.exitCode = 1;
  } else {
    process.stderr.write("checkpoint: internal error; no checkpoint state was reported\n");
    process.exitCode = 1;
  }
}
