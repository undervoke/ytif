import { spawnSync } from "node:child_process"
import { resolve } from "node:path"

// Routes shell calls through `ytif guard`, so the refusal rule lives only
// in ytif. Copy this file into .opencode/plugins/. One default object serves
// both generations: v1 calls server and names the shell tool "bash", v2
// calls setup and names it "shell".

// YTIF starts ytif. A project that pins ytif as a Go tool uses
// ["go", "tool", "ytif"]; the guard then runs in the session directory,
// inside the module.
const YTIF = ["ytif"]

// sessionDirV1 prefers the event's session directory over the plugin load
// location. An explicit worktree or directory on the event wins, then a
// client session lookup, then the plugin load directory.
async function sessionDirV1(ctx: any, input: any, fallback: string): Promise<string> {
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

// sessionDirV2 resolves each event's session: ctx.location is the plugin
// load location, not the session directory.
async function sessionDirV2(ctx: any, event: any, fallback: string): Promise<string> {
  try {
    const res = await ctx?.session?.get({ sessionID: event?.sessionID })
    const session = res?.data ?? res
    if (typeof session?.location?.directory === "string") {
      // directory is already absolute; subpath describes its project-relative suffix.
      return session.location.directory
    }
  } catch {}
  return fallback
}

// judge runs one shell call through the guard. session is the module context
// `go tool ytif` runs in; cwd is the effective shell directory forwarded to
// the guard.
function judge(session: string, args: Record<string, unknown>): { reason?: string; problem?: string } {
  if (typeof args.command !== "string") return {}
  const cwd = typeof args.workdir === "string" ? resolve(session, args.workdir) : session
  return guard(args.command, cwd, session)
}

const server = async (ctx: any) => {
  const loadDir = ctx?.directory ?? ctx?.worktree ?? process.cwd()
  return {
    "tool.execute.before": async (input: any, output: any) => {
      if (input?.tool !== "bash") return
      const session = resolve(await sessionDirV1(ctx, input, loadDir))
      const { reason, problem } = judge(session, (output?.args ?? {}) as Record<string, unknown>)
      if (problem) {
        try {
          await ctx?.client?.tui?.showToast({ body: { title: "YTiF", message: problem, variant: "warning" } })
        } catch {}
      }
      if (reason) throw new Error(reason)
    },
  }
}

const setup = async (ctx: any) => {
  // ctx.location is the plugin load location, not the session directory.
  const loadDir = ctx?.location?.directory ?? process.cwd()
  await ctx.tool.hook("execute.before", async (event: any) => {
    if (event?.tool !== "shell") return
    const session = await sessionDirV2(ctx, event, loadDir)
    const { reason, problem } = judge(session, (event?.input ?? {}) as Record<string, unknown>)
    if (problem) {
      try {
        console.warn(problem)
      } catch {}
    }
    if (reason) throw new Error(reason)
  })
}

export default {
  id: "ytif-guard",
  server,
  setup,
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
