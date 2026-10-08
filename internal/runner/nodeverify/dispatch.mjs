// ytif node-verify dispatcher: reads {files, checks} on stdin, calls each
// check in order, and appends one line to the results file as a check
// starts and one as it ends.
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

const modules = new Map();
for (const [i, c] of req.Checks.entries()) {
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
  const start = process.hrtime.bigint();
  let outcome = "pass";
  try {
    if (!modules.has(c.Module)) modules.set(c.Module, import(pathToFileURL(c.Module).href));
    const fn = (await modules.get(c.Module))[c.Name];
    if (controller.signal.aborted) throw new Error("interrupted before the check started");
    if (typeof fn !== "function") throw new Error(`${c.Name} is not an exported function`);
    await fn({ files: [...(req.Files ?? [])], signal: controller.signal }, out);
  } catch (err) {
    outcome = "fail";
    if (text !== "" && !text.endsWith("\n")) text += "\n";
    text += failure(err);
  }
  emit({
    I: i,
    Outcome: outcome,
    ElapsedNS: Number(process.hrtime.bigint() - start),
    EndNS: Date.now() * 1e6,
    Output: text,
  });
}
