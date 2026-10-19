import { spawnSync } from "node:child_process"
import { resolve } from "node:path"

// Routes Kilo bash calls through `ytif guard`, so the refusal rule lives
// only in ytif. Copy this file into .kilo/plugin/. Targets the current
// OpenCode-based Kilo default {id, server} plugin API, with the "bash" tool
// and its {command, workdir?} arguments.

// YTIF starts ytif. A project that pins ytif as a Go tool uses
// ["go", "tool", "ytif"]; the guard then runs in the session directory,
// inside the module.
const YTIF = ["ytif"]

// sessionDir prefers the event's session directory over the plugin load
// location. An explicit worktree or directory on the event wins, then a
// client session lookup, then the plugin load directory.
async function sessionDir(ctx: any, input: any, fallback: string): Promise<string> {
  const explicit = input?.worktree ?? input?.directory
  if (typeof explicit === "string" && explicit) return explicit
  const sessionID = input?.sessionID ?? input?.sessionId
  if (typeof sessionID === "string" && sessionID) {
    try {
      const res = await ctx?.client?.session?.get({ path: { id: sessionID } })
      const dir = (res?.data ?? res)?.directory
      if (typeof dir === "string" && dir) return dir
    } catch {}
  }
  return fallback
}

// server is the Kilo entrypoint: Kilo calls it with the plugin context and
// uses the returned tool.execute.before map. A throw there skips the tool
// body, as in OpenCode v1.
const server = async (ctx: any) => {
  const loadDir = ctx?.directory ?? ctx?.worktree ?? process.cwd()
  return {
    "tool.execute.before": async (input: any, output: any) => {
      if (input?.tool !== "bash") return
      const args = (output?.args ?? {}) as Record<string, unknown>
      if (typeof args.command !== "string") return
      const session = resolve(await sessionDir(ctx, input, loadDir))
      // The session is the module context `go tool ytif` runs in; cwd is
      // the effective shell directory forwarded to the guard.
      const cwd = typeof args.workdir === "string" ? resolve(session, args.workdir) : session
      const { reason, problem } = guard(args.command, cwd, session)
      if (problem) {
        try {
          await ctx?.client?.tui?.showToast({ body: { title: "YTiF", message: problem, variant: "warning" } })
        } catch {}
      }
      if (reason) throw new Error(reason)
    },
  }
}

export default {
  id: "ytif-guard",
  server,
}

// guard returns the refusal reason, if any, and a problem when the guard
// did not run or reported an error. A guard that did not run allows the
// command, as a missing hook command does in other hosts.
function guard(command: string, cwd: string, directory: string): { reason?: string; problem?: string } {
  const result = spawnSync(YTIF[0], [...YTIF.slice(1), "guard"], {
    cwd: directory,
    input: JSON.stringify({ cwd, tool_input: { command } }),
    encoding: "utf8",
  })
  if (result.error || result.status !== 0) {
    const cause = result.error?.message ?? result.stderr?.trim() ?? ""
    return { problem: `(YTiF) guard did not run (${cause || `exit status ${result.status}`}); the command was allowed` }
  }
  return { reason: denyReason(result.stdout), problem: result.stderr?.trim() || undefined }
}

function denyReason(stdout: string | null): string | undefined {
  if (!stdout?.trim()) return undefined
  try {
    const decision = JSON.parse(stdout)?.hookSpecificOutput
    return decision?.permissionDecision === "deny" ? decision.permissionDecisionReason : undefined
  } catch {
    return undefined
  }
}
