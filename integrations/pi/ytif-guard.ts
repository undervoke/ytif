import { spawnSync } from "node:child_process"
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent"

// Routes Pi bash calls through `ytif guard`, so the refusal rule lives only
// in ytif. Copy this file into .pi/extensions/ of a trusted project.
export default function (pi: ExtensionAPI) {
  pi.on("tool_call", async (event, ctx) => {
    if (event.toolName !== "bash") return undefined
    const command = event.input.command
    if (typeof command !== "string") return undefined
    const { reason, problem } = guard(command, ctx.cwd)
    if (problem) ctx.ui.notify(problem, "warning")
    return reason ? { block: true, reason } : undefined
  })
}

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
