---
name: afk
description: Enter an AFK (away-from-keyboard) posture for the rest of this agent session — when the work is genuinely complete and checks pass, call `this finish` directly instead of waiting for the operator's go-ahead. Manual invocation only — operator types `/afk`.
disable-model-invocation: true
---

# afk

Use this skill when the operator is stepping away and wants the session
to wrap itself up on success instead of blocking on a final approval.

Scope: **this session only**. The posture is not persisted anywhere —
launching a fresh `ticket agent` session starts in the normal "ask
before finishing" mode again.

## What changes for the rest of this session

The default agent-prompt instruction says:

> When the user gives the go-ahead to finish the ticket, run `this
> finish`.

While AFK is active, that step is replaced by:

> When you believe the ticket is genuinely done **and** all relevant
> checks pass, run `this finish` yourself. Do not wait for the operator
> to confirm.

Everything else stays the same — still write the concise summary, still
commit every changed file, still omit the `Co-authored-by` tag from
commit messages.

## When to still stop and ask (guardrail)

AFK is **not** a license to guess. Pause and ask the operator — do not
finish, do not press on — if any of the following applies:

1. **Ambiguous requirement.** The description (or follow-up discussion)
   leaves a real degree of freedom that materially changes the outcome,
   and you cannot resolve it from the stated requirements alone.
2. **Destructive or hard-to-reverse action needed.** Force-push,
   `git reset --hard` on shared branches, deleting data, dropping
   tables, mass file deletion, modifying CI/CD pipelines, anything
   visible to others (PRs, messages, external services), etc.
3. **Missing access.** A required credential, repository, tool, or
   permission isn't available and you can't make progress without it.
4. **Contradicting evidence.** What you're seeing (test results, file
   contents, command output) contradicts the description or your prior
   understanding in a way you cannot reconcile.

Residual check: if a reasonable engineer would pause here for a second
opinion, pause. AFK lowers the bar for finishing on success, **not** the
bar for asking when something is off.

## On the completion path

Before calling `this finish` autonomously:

- Re-read `description.md` and confirm each acceptance criterion is met.
- Confirm every changed file is committed.
- Confirm any tests / checks the ticket calls out are passing.
- Write the concise summary as usual.
- Then run `this finish`.

If any of the above doesn't hold, do **not** call `this finish` — fix
it, or fall back to the guardrail and ask.

## Reply on invocation

Acknowledge briefly that AFK is active for the rest of the session,
restate the guardrail in one line so the operator can verify intent,
then continue the task that's in progress (or wait for one if the
session was just started).
