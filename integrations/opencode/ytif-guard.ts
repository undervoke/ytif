import { spawnSync } from "node:child_process"
import { resolve } from "node:path"
import type { Plugin } from "@opencode-ai/plugin"

// Routes OpenCode bash calls through `ytif guard`, so the refusal rule lives
// only in ytif. Copy this file into .opencode/plugins/.
export const YtifGuard: Plugin = async ({ client, directory }) => ({
  "tool.execute.before": async (input, output) => {
    if (input.tool !== "bash") return
    const args = output.args as Record<string, unknown>
    const command = args?.command
    if (typeof command !== "string") return
    // The bash tool runs in its workdir, resolved against the session directory.
    const cwd = typeof args.workdir === "string" ? resolve(directory, args.workdir) : directory
    const { reason, problem } = guard(command, cwd)
    if (problem) {
      await client.tui.showToast({ body: { title: "YTiF", message: problem, variant: "warning" } }).catch(() => {})
    }
    if (reason) throw new Error(reason)
  },
})

// guard returns the refusal reason, if any, and a problem when the guard
// did not run or reported an error. A guard that did not run allows the
// command, as a missing hook command does in other hosts.
function guard(command: string, cwd: string): { reason?: string; problem?: string } {
  const result = spawnSync("ytif", ["guard"], {
    cwd,
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
