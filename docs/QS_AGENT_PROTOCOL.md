# QS campaign: how the work is split (2026-09-20)

The campaign is long and the transcript is the scarce resource. So the
session commander (this conversation) does not read logs, does not read
this repo's big documents, and does not watch rounds. It decides.

## The three roles

**Waiting costs nothing.** A round takes hours. Never poll it from the
conversation and never park a subagent on it. Start a background shell
that blocks until the round's log carries its terminal line; the harness
wakes the commander when that shell exits.

    setsid nohup bash -c 'until grep -qE "^[0-9:]{8} ROUND (DONE|.*ABORTED)" \
      /data/blockchain/wr-logs/rNNNN.log 2>/dev/null; do sleep 60; done' &

**Legwork goes to a subagent.** One step, one agent, cheap model. The
agent reads what it needs, runs the scripts, writes its findings into the
documents, commits, and returns a report in the fixed shape below. Its
tool output never reaches the commander.

**Judgement stays with the commander.** It reads the six-line report and
QS_QUEUE.md, rules the prediction confirmed or falsified, decides what
the next step is, and dispatches the next agent.

## The report contract

Every subagent returns EXACTLY these six lines and nothing else -- no
preamble, no log excerpts, no restating of the task:

    STEP: <queue id>
    RESULT: <the one number that decides the step, with its unit>
    BASELINE: <the number it is measured against>
    VERDICT: confirmed | falsified | aborted | inconclusive
    WROTE: <file:section it appended to, and the commit hash if it pushed>
    NEXT: <the one thing the commander must decide, in a single sentence>

A number the agent did not measure is `n/a`, never an estimate. If the
step failed before producing a number, RESULT is the failure in under
ten words. An agent that wants to say more puts it in the document and
names the section on the WROTE line: the commander reads that section
only if the VERDICT is surprising.

## Rules every agent inherits

- Commit messages entirely in English, no "claude", no trailers. Code
  comments English. Never touch /home/n42/src/n42/N42-gov5; work in
  /data/blockchain/gov5-work/wt-r27 and push with `git push origin HEAD:main`.
- Times America/New_York.
- The box is shared. Follow wr-logs/BOX-CLAIM-PROTOCOL.md, never kill
  another driver's processes, kill your own by exact PID.
- One variable per round, and the prediction is registered in
  QS_BLOCK_TIME_BUDGET.md BEFORE the round runs.
- Never conclude from the A legs; the B mean is the scored metric.
- Do not enable PQPrecompilesTime.

## The queue

QS_QUEUE.md (this directory) holds one row per step: id, what it changes,
which binary, the registered prediction, status, and the decided result.
The agent updates its own row; the commander sets the next row's status
to `next`.
