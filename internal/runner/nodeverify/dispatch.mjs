// ytif node-verify dispatcher: reads one homogeneous process group on
// stdin, calls each check in order, and appends one line to the results
// file as a check starts and one as it ends. The process environment is
// fixed before startup; every check in the group shares it. The dispatcher
// snapshots the projected ytif-owned environment at startup and restores
// it before each call, including before import, so a verifier that mutates
// NX_BASE, NX_HEAD or YTIF_SCOPE cannot contaminate the next call.
// BASE_SHA stays host-owned and is never touched.
import { appendFileSync, readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

const results = process.argv[2];
const req = JSON.parse(readFileSync(0, "utf8"));
const emit = (event) => appendFileSync(results, JSON.stringify(event) + "\n");

// An interrupted gate aborts the running check and starts no other. A
// check settled through microtasks never lets the signal handler run, so
// each check starts only after a turn of the event loop.
const controller = new AbortController();
process.on("SIGINT", () => controller.abort());
const turn = () => new Promise((resolve) => setImmediate(resolve));

// failure renders a thrown value as its message, so a path:line: message
// finding stays one, followed by the stack frames outside this dispatcher.
function failure(err) {
  if (!(err instanceof Error)) return String(err);
  const frames = String(err.stack ?? "")
    .split("\n")
    .filter((line) => /^\s+at /.test(line) && !line.includes(import.meta.url));
  return [err.message, ...frames].join("\n");
}

// The request carries the only variables the dispatcher restores per call
// from check.RestoreKeys. BASE_SHA stays host-owned.
const ytifEnvKeys = [...(req.EnvKeys ?? [])];
const savedEnv = new Map(ytifEnvKeys.map((k) => [k, process.env[k]]));
const restoreEnv = () => {
  for (const k of ytifEnvKeys) {
    const v = savedEnv.get(k);
    if (v === undefined) delete process.env[k];
    else process.env[k] = v;
  }
};

const files = [...(req.Files ?? [])];
const gate = req.Gate ?? "";
const profile = req.Profile ?? "";
const comparisonRaw = req.Comparison ?? null;
const groupComparison =
  comparisonRaw == null
    ? null
    : {
        base: comparisonRaw.base ?? "",
        head: comparisonRaw.head ?? "",
        kind: comparisonRaw.kind ?? "",
      };

const modules = new Map();
for (const [i, c] of (req.Checks ?? []).entries()) {
  await turn();
  if (controller.signal.aborted) break;
  emit({ I: i, Start: true });
  let text = "";
  const out = {
    write(s) {
      text += String(s);
      return true;
    },
  };
  let reportedScope = "";
  let reportedTargets = undefined;
  const report = (r) => {
    if (r == null || typeof r !== "object") throw new Error("report needs {scope, targets?}");
    if ("scope" in r) reportedScope = r.scope ?? "";
    if ("targets" in r) {
      const t = r.targets ?? undefined;
      if (t !== undefined && !(Number.isInteger(t) && t >= 0)) throw new Error("report targets needs a nonnegative integer");
      reportedTargets = t;
    }
  };
  const start = process.hrtime.bigint();
  let outcome = "pass";
  try {
    const modulePath = c.Module;
    const name = c.Name;
    const requested = c.Requested ?? "full";
    const resolved = c.Resolved ?? "full";
    const isFull = resolved === "full";
    restoreEnv();
    if (!modules.has(modulePath)) modules.set(modulePath, import(pathToFileURL(modulePath).href));
    const fn = (await modules.get(modulePath))[name];
    if (controller.signal.aborted) throw new Error("interrupted before the check started");
    if (typeof fn !== "function") throw new Error(`${name} is not an exported function`);
    restoreEnv();
    await fn(
      {
        files: [...files],
        signal: controller.signal,
        gate,
        profile,
        selection: {
          requested,
          resolved,
          decision: { run: c.Run ?? true, reason: c.Reason ?? "" },
          comparison: isFull || !groupComparison ? null : { ...groupComparison },
          changed: isFull ? null : c.Changed ?? null,
        },
        report,
      },
      out,
    );
  } catch (err) {
    outcome = "fail";
    if (text !== "" && !text.endsWith("\n")) text += "\n";
    text += failure(err);
  }
  // An abort is not a completed outcome, regardless of timestamp rounding.
  if (controller.signal.aborted) break;
  emit({
    I: i,
    Outcome: outcome,
    ElapsedNS: Number(process.hrtime.bigint() - start),
    EndNS: Date.now() * 1e6,
    Output: text,
    Scope: reportedScope || undefined,
    Targets: reportedTargets,
  });
}
