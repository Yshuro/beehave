# The review loop of the remediation method: scoped fixes and scoped reviews until a candidate, then the full checks of that candidate once.
# A candidate goes through the gate on the base, the checks before review, the checks of the caller and a full review. A finding returns it to the scoped loop.
# A project that wants another policy copies this file into its workflow folder and loads it with load("//review_fix.star", "review_fix").

FORBID = ["bulk-staging", "local-delete", "remote-delete", "opaque-command", "git-author", "git-bypass", "env-override", "config-read", "zsh-expansion", "laptop-copy"]

# A reviewer is read-only: it must not fix, commit or push. The kind git-write refuses every git command that changes a repository.
REVIEW_FORBID = FORBID + ["git-write"]

# The most scoped reviews of one candidate. After the last one the candidate goes to the full checks with its findings still open.
MAX_INNER = 3

# The first agent of a candidate is its first scoped fix. A candidate with no findings due, the first one after a clean implementer, starts at a scoped
# review of the diff of the implementer instead, under its own name. Each candidate makes one of these two calls once, and no other call has these names.
FIRST_FIX = "fix-scoped-1"
FIRST_REVIEW = "scoped-review-implement"

def candidates_spent():
    """The candidates that the other runs of the item started. A candidate counts when its first agent starts, so a run that stops before the scoped review still counts it."""
    spent = 0
    for call in [FIRST_FIX, FIRST_REVIEW]:
        spent += earlier_calls(call = call, name = "spent-" + call).count
    return spent

def restart_steps(item):
    """The sentence that ends a stop text: a plain resume replays the recorded stop, so the item starts again with a resume, a cancel and a run."""
    return "A plain beehave resume replays this stop. To start the item again, run beehave resume %s, then beehave cancel %s, then beehave run %s." % (item.id, item.id, item.id)

def capped(step, name, item):
    """Stops the item when a cap ended the agent step. The output `stop` is "turns" or "budget", and the work of a capped run may be half done."""
    if step.stop != "":
        fail("REFUSE: the %s step stopped at the %s cap before it finished. Raise the cap or split the item. %s" % (name, step.stop, restart_steps(item)))

def kept_history(step, name, item):
    """Stops the item when an agent step rewrote history (amend, squash, reset). The reviewed commits must stay, and a rewrite can reach the remote only through a force-push."""
    if step.rewrote:
        fail("REFUSE: the %s step rewrote the history of the item folder. A step adds commits and never amends, squashes or resets. Check the folder. %s" % (name, restart_steps(item)))

DECISIONS = ["keep", "replace", "delete"]

# The fixes on one mechanism after which the next fix of it is a design step that decides whether the mechanism should exist.
DESIGN_AFTER = 2

# The last fix round that resumes the session of the implementer. Later rounds start a fresh agent on the step fix_late, or on fix when there is no fix_late.
FIX_ESCALATE_AFTER = 3

# The steps of the loop that need a model, and the optional steps. design is needed only when the loop has a design prompt.
STEPS_NEEDED = ["implement", "review", "fix"]
STEPS_OPTIONAL = ["fix_late", "design"]

def checked_steps(steps, design_prompt):
    """Returns the steps with a runner in each entry. The library holds no model: a missing or blank model of a needed step is a refusal that names the step."""
    if type(steps) != "dict":
        fail("REFUSE: review_fix got steps %r, of type %s. Pass a dict from a step name to {\"model\": <model>, \"runner\": <runner>}." % (steps, type(steps)))

    needed = STEPS_NEEDED + (["design"] if design_prompt != "" else [])
    for name in needed:
        if name not in steps:
            fail("REFUSE: review_fix has no entry for the %s step. Add steps[%r] = {\"model\": <model>}." % (name, name))

    result = {}
    for name in steps:
        if name not in STEPS_NEEDED + STEPS_OPTIONAL:
            fail("REFUSE: review_fix has no step %s. The steps are: %s." % (name, ", ".join(STEPS_NEEDED + STEPS_OPTIONAL)))

        entry = steps[name]
        model = entry.get("model") if type(entry) == "dict" else None
        if type(model) != "string" or model.strip() == "":
            fail("REFUSE: the %s step of review_fix has no model. Set steps[%r][\"model\"] to the name of a model." % (name, name))

        runner = entry.get("runner", "")
        if type(runner) != "string":
            fail("REFUSE: the runner of the %s step of review_fix is %r, of type %s. Write a runner name, or an empty text for the pool." % (name, runner, type(runner)))

        result[name] = {"model": model, "runner": runner}

    return result

def stopped_before(item, name, head, guard):
    """Asks the forge about the pull request of the item right before an agent of the loop starts.
    The head is the head of the worktree that the agent works on. A pull request that is merged, closed, held, a draft,
    on another base, without its branch or at another head is a stop: it returns the result of the call, and the loop starts no agent."""
    if not guard:
        return None

    check = preflight(name = name, id = item.id, head = head, **guard)
    return None if check.ok else check

# A gate with no failed step and an unknown step gave no verdict about the code: it is void, not red. The loop reruns a void gate
# void_reruns times at once (default VOID_RERUNS). Each rerun is a new gate step, so the engine records each, and a replay never reads an old void as the answer.
# Then it asks the owner up to void_asks times (default VOID_ASKS); an answer reruns the gate. The wait is the way back: a resume would replay the recorded void.
VOID_RERUNS = 2
VOID_ASKS = 3

# A void gate with a host cause is a lost host job that the engine proved. The loop reruns it with no cost to the item, and stops the item
# when the engine counts HOST_LOSS_LIMIT host losses in the tree of the run. The count is the host_losses field of the gate result; the loop keeps none of its own.
HOST_LOSS_LIMIT = 5

def is_void(check):
    return not check.passed and not [s for s in check.steps if s["status"] == "fail"] and [s for s in check.steps if s["status"] == "unknown"] != []

def void_names(check):
    names = [s["name"] for s in check.steps if s["status"] == "unknown"]
    return ", ".join(names) if names else "unknown"

def verdict_of(loop, review):
    """The verdict of a review. With follow_ups = "forbid", a review that leaves any finding open in its output `open` has the verdict changes, unless the agent wrote reject."""
    if loop.follow_ups == "forbid" and review.verdict != "reject" and review.open != "":
        return "changes"
    return review.verdict

def labelled(loop, kind, cap):
    """The label of the next agent call of a kind: the kind and the number of the call against the cap, for example fix 2/6."""
    loop.calls[kind] += 1
    return "%s %d/%d" % (kind, loop.calls[kind], cap)

def ended(loop, head, gate = None, review = None, stopped = False, exhausted = False, rejected = False, design = None, held = ""):
    """The result of the loop. head is the commit that the last step of the loop judged. gate is the last gate of a candidate, and it is None when the loop ended before the first one.
    verdict is the verdict of the last full review, and it is empty when the loop ended before one.
    host_losses is the highest count that a gate of the loop read, and host_lost is true when HOST_LOSS_LIMIT stopped the reruns."""
    verdict = verdict_of(loop, review) if review != None else ""
    return struct(head = head, stopped = stopped, exhausted = exhausted, rejected = rejected, design = design, held = held, gate = gate, review = review, verdict = verdict, host_losses = loop.host["losses"], host_lost = loop.host["lost"])

def sent_back(findings, mechanism = "", gate = None, review = None, due = True):
    """The result of a step that sends the head back to a fix: the findings and the mechanism that they are about. due is false only for a head that needs no fix."""
    return struct(end = None, due = due, findings = findings, mechanism = mechanism, gate = gate, review = review)

def is_host_void(check):
    return is_void(check) and len(check.host_loss) > 0

def note_losses(loop, check):
    """Keeps the highest host_losses that a gate of the loop read."""
    loop.host["losses"] = max(loop.host["losses"], check.host_losses)

def check_gate(loop):
    """Runs the gate of the candidate on the base and reruns it while it is void. Returns the first result that is not void, or stops the item after the bound.
    A void gate with a host cause reruns as candidate-gate-host-<k> and takes no plain attempt. The loop returns it once its host_losses reach HOST_LOSS_LIMIT.
    The highest host_losses of every gate that ran goes to loop.host."""
    check = gate(name = "candidate-gate", base = loop.base)
    note_losses(loop, check)
    hosts = 0
    attempt = 0
    for _ in range(HOST_LOSS_LIMIT + loop.void_reruns + loop.void_asks + 1):
        if not is_void(check):
            return check

        if is_host_void(check):
            if check.host_losses >= HOST_LOSS_LIMIT:
                return check

            hosts += 1
            check = gate(name = "candidate-gate-host-%d" % hosts, base = loop.base)
            note_losses(loop, check)
            continue

        attempt += 1
        if attempt > loop.void_reruns + loop.void_asks:
            break

        if attempt > loop.void_reruns:
            wait(
                "The gate gave no result for %s (steps with no result: %s). %s Fix the cause, then answer to run the gate again." % (loop.item.id, void_names(check), check.report),
                name = "gate-void-%d" % attempt,
            )

        check = gate(name = "candidate-gate-void-%d" % attempt, base = loop.base)
        note_losses(loop, check)

    # A gate that is still void after the reruns and the asks gave no verdict about the code. No fix agent reads its report.
    if is_void(check):
        fail("REFUSE: the gate gave no result for %s after %d reruns and %d questions (steps with no result: %s). %s" % (loop.item.id, loop.void_reruns, loop.void_asks, void_names(check), check.report))

    return check

# A closing keyword of GitHub (the nine, in any case) followed by an issue: "#N", "owner/repo#N" or an issue URL. A plain "#N" closes nothing.
# https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/linking-a-pull-request-to-an-issue
# git reads it as an extended regular expression, one line of a message at a time: git-log, options --grep, -E and -i.
REPO_NAME = "[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+"
CLOSING_KEYWORD = "(^|[^[:alnum:]_])(close[sd]?|fix(es|ed)?|resolve[sd]?):?[[:space:]]+((" + REPO_NAME + ")?#[0-9]+|https?://github[.]com/" + REPO_NAME + "/issues/[0-9]+)"

def secret_findings(code):
    """The finding of the secret scan of the gate: none for a clean exit, one for another exit or for a log that holds no exit code."""
    if code == None:
        return ["the log of the gate job has no exit code of the secret scan, so the scan proves nothing"]
    if code != 0:
        return ["the secret scan of the head exited with code %d. Remove the secret from every commit of the branch" % code]
    return []

def revert_findings(check):
    """The findings of the revert run of the gate, for a head with new tests: a test that passed or was skipped on the base code, or a run that proves nothing."""
    checks = check.checks
    if not checks["complete"] or checks["new"] == 0 or check.revert == "pass":
        return []

    findings = ["the new test %s passes or is skipped on the base code. A test must fail without the change" % test for test in checks["revert_passed"]]
    if checks["revert_passed_count"] > len(checks["revert_passed"]):
        findings.append("%d new tests pass or are skipped on the base code in all, and the log names the first %d" % (checks["revert_passed_count"], len(checks["revert_passed"])))
    if not findings:
        findings.append("the revert run proves nothing: " + (checks.get("revert_problem") or "the log of the gate job has no end marker of the revert run"))
    return findings

def gate_findings(check):
    """The findings that the checks of the gate job give: the new tests, the revert run and the secret scan. A cut log proves nothing."""
    checks = check.checks
    findings = []
    if not checks["complete"]:
        findings.append("the log of the gate job has no end marker, so the new-tests check is cut and proves nothing" + (": " + checks["problem"] if checks.get("problem") else ""))
    elif checks["new"] == 0:
        findings.append("the head adds no new test. Add a test that fails without the change")

    findings += ["a new test failed a check: " + reason for reason in checks["failed"]]
    if checks["failed_count"] > len(checks["failed"]):
        findings.append("%d new tests failed a check in all, and the log names the first %d" % (checks["failed_count"], len(checks["failed"])))

    return findings + revert_findings(check) + secret_findings(checks.get("secrets_rc"))

def git_step(name, run, ok = [0]):
    """Runs git in the item folder in a command step. An exit code outside ok is an error of git and stops the item, never a finding."""
    done = command(run = ["git"] + run, name = name)
    if done.rc not in ok:
        fail("REFUSE: before_review could not run git %s (exit %d): %s" % (" ".join(run), done.rc, done.stderr.strip()))
    return done

def paths_outside(allow, base, head, name):
    """The paths that the head changed since the merge base and that no glob of allow covers, sorted. git reads each glob as a pathspec with glob magic."""
    if not allow:
        return []

    excluded = [":(exclude,glob)" + (glob[2:] if glob.startswith("./") else glob) for glob in allow]
    done = git_step(name, ["diff", "--name-only", "--no-renames", "-z", base + "..." + head, "--", "."] + excluded)
    return sorted([path for path in done.stdout.split("\x00") if path])

def before_review(check, base, allow = [], name = "before-review"):
    """Judges the head of a gate result before review. Returns struct(ok, findings): ok is true when there is no finding.
    It reads the new tests, the revert run and the secret scan from check, the result of gate(base = ...). A gate with no base gives a cut log, which is a finding.
    With git it checks that the head holds the base, that the changed paths are inside allow (no allow, every path), and that no commit closes an issue.
    base is a git ref. The steps are named name-ancestor, name-paths and name-keywords."""
    head = check.head
    findings = gate_findings(check)

    descends = git_step(name + "-ancestor", ["merge-base", "--is-ancestor", base, head], ok = [0, 1])
    if descends.rc == 1:
        return struct(ok = False, findings = findings + ["the head %s is not a descendant of the base %s. Rebase the work on the base" % (head, base)])

    outside = paths_outside(allow, base, head, name + "-paths")
    if outside:
        findings.append("the head changed %d path(s) outside allow [%s]: %s" % (len(outside), ", ".join(allow), ", ".join(outside)))

    closing = git_step(name + "-keywords", ["log", "-E", "-i", "--grep=" + CLOSING_KEYWORD, "--format=%h %s", base + ".." + head])
    findings += ["the commit %s has a closing keyword. Only the integration pull request may close an issue. Remove the keyword" % line for line in closing.stdout.splitlines()]

    return struct(ok = len(findings) == 0, findings = findings)

def check_before_review(loop, check):
    """Runs the checks before review on the head that the gate passed. Returns an empty text when the head is clean, or the findings as text."""
    judged = before_review(check, base = "origin/" + loop.base, allow = loop.allow, name = "candidate-before-review")
    if judged.ok:
        return ""

    return "The checks before review refused the head:\n" + "\n".join(["- " + finding for finding in judged.findings])

def with_violations(findings, *violations):
    """Puts the lines of the violations before the findings, latest first."""
    for lines in violations:
        if lines != "":
            findings = lines + "\n\n" + findings
    return findings

def review_args(loop, head, violations, scope, outputs):
    """The arguments of a fresh read-only review of the head, other than its name. scope tells the review what it judges."""
    return dict(
        label = labelled(loop, "review", loop.review_cap),
        prompt = loop.review_prompt,
        model = loop.steps["review"]["model"],
        runner = loop.steps["review"]["runner"],
        pin = head,
        outputs = (["verdict", "mechanism"] if loop.design_prompt != "" else ["verdict"]) + (["open"] if loop.follow_ups == "forbid" else []) + outputs,
        forbid_commands = REVIEW_FORBID + loop.extra,
        protect_paths = loop.protect,
        output_tries = 2,
        edits = False,
        inputs = {"item": loop.item, "violations": violations, "facts": loop.facts, "scope": scope},
    )

def mechanism_of(loop, review):
    """The file that the findings of the review are about: the text before the first colon. Without a design round it is empty."""
    return review.mechanism.split(":")[0].strip() if loop.design_prompt != "" else ""

def check_review(loop, check, inner, outer):
    """Runs the full review of the candidate that passed every other check. Returns the end of the loop, or the findings for the next fix."""
    lane = inner.lane
    if stopped_before(loop.item, "before-review-%d" % outer, check.head, loop.guard) != None:
        return struct(end = ended(loop, check.head, stopped = True, gate = check))

    review = agent(name = "candidate-review", **review_args(loop, check.head, lane.violations, "the whole pull request", loop.review_outputs))
    capped(review, "review", loop.item)

    # A reject ends the loop with no fix round. The caller holds the item with the text of the review as the reason.
    if review.verdict == "reject":
        return struct(end = ended(loop, check.head, rejected = True, gate = check, review = review))

    # The hook sees every full review at once, so a bad round ends the loop before the next fix lane starts.
    held = loop.review_check(review) if loop.review_check != None else ""
    if held != "":
        return struct(end = ended(loop, check.head, held = held, gate = check, review = review))

    # A read-only step that changed the folder is a violation of this round, the last scoped review included: the approve of that review is not clean.
    if verdict_of(loop, review) == "approve" and lane.violations == "" and review.violations == "" and inner.breach == "":
        return struct(end = ended(loop, check.head, gate = check, review = review))

    return sent_back(with_violations(review.text, inner.breach, review.violations, lane.violations), mechanism_of(loop, review), check, review)

def check_candidate(loop, inner, outer):
    """Runs the full checks of the candidate once, in order: the gate on the base, the checks before review, each check of the caller, then the full review.
    The first entry with findings sends the candidate back, and the entries after it do not run. Findings that the last scoped review left open go with them."""
    check = check_gate(loop)

    # A host report is no finding of the code: a fix agent never reads it. The item stops, and the caller decides what the loss costs.
    if is_host_void(check):
        loop.host["lost"] = True
        return struct(end = ended(loop, check.head, gate = check))

    if not check.passed:
        return sent_back(with_violations(check.report, inner.open), gate = check)

    findings = check_before_review(loop, check)
    if findings != "":
        return sent_back(with_violations(findings, inner.open), gate = check)

    for entry in loop.checks:
        findings = entry(check)
        if findings != "":
            return sent_back(with_violations(findings, inner.open), gate = check)

    return check_review(loop, check, inner, outer)

def design_round(loop, pending, fixes, decided, outer, lane):
    """Runs the design step when the mechanism of the findings had design_after fixes. Returns the end of the loop for a replace or a delete, or the findings for the fix."""
    mechanism = pending.mechanism
    if mechanism == "" or fixes.get(mechanism, 0) < loop.design_after or mechanism in decided:
        return pending

    if stopped_before(loop.item, "before-design-%d" % outer, lane.head_sha, loop.guard) != None:
        return struct(end = ended(loop, lane.head_sha, stopped = True, gate = pending.gate, review = pending.review))

    design = agent(
        name = "design",
        label = "design",
        prompt = loop.design_prompt,
        model = loop.steps["design"]["model"],
        runner = loop.steps["design"]["runner"],
        outputs = ["decision", "reason"],
        forbid_commands = REVIEW_FORBID + loop.extra,
        protect_paths = loop.protect,
        output_tries = 2,
        edits = False,
        inputs = {"item": loop.item, "mechanism": mechanism, "fixes": fixes[mechanism], "findings": pending.findings, "facts": loop.facts},
    )
    capped(design, "design", loop.item)

    if design.violations != "":
        fail("REFUSE: the design step changed the item folder, and it is read-only. %s Check the folder. %s" % (design.violations, restart_steps(loop.item)))

    if design.decision not in DECISIONS:
        fail("REFUSE: the design step wrote the decision %r. Use one of: %s. %s" % (design.decision, ", ".join(DECISIONS), restart_steps(loop.item)))

    if design.decision != "keep":
        return struct(end = ended(loop, lane.head_sha, gate = pending.gate, review = pending.review, design =struct(mechanism = mechanism, decision = design.decision, reason = design.reason)))

    decided[mechanism] = True
    return sent_back("Design decision for %s: keep and patch. %s\n\n%s" % (mechanism, design.reason, pending.findings), mechanism, pending.gate, pending.review)

def scoped_fix(loop, pending, fixes, decided, outer, inner, lane):
    """Runs the design round when it is due, then one scoped fix of the findings. Returns the end of the loop, or the fix lane."""
    pending = design_round(loop, pending, fixes, decided, outer, lane)
    if pending.end != None:
        return pending

    if stopped_before(loop.item, "before-fix-%d" % outer, lane.head_sha, loop.guard) != None:
        return struct(end = ended(loop, lane.head_sha, stopped = True, gate = pending.gate, review = pending.review))

    # The fixes after candidate k are fix round k, and the fixes inside candidate 1 are round 1. The rounds up to escalate_after run on the step fix, and they resume the session of the implementer
    # when both steps name the same runner: a session belongs to one login. Later rounds start a fresh agent on the step fix_late, or on fix when the project names no fix_late.
    round = max(outer - 1, 1)
    early = round <= loop.escalate_after
    chosen = loop.steps["fix"] if early else loop.steps.get("fix_late", loop.steps["fix"])
    fix = agent(
        name = "fix-scoped-%d" % inner,
        label = labelled(loop, "fix", loop.fix_cap),
        prompt = loop.fix_prompt,
        model = chosen["model"],
        runner = chosen["runner"],
        resume = loop.impl.session if early and loop.steps["fix"]["runner"] == loop.steps["implement"]["runner"] else "",
        forbid_commands = FORBID + loop.extra,
        protect_paths = loop.protect,
        inputs = {"item": loop.item, "round": round, "findings": pending.findings, "facts": loop.facts},
        **loop.scope
    )
    capped(fix, "fix", loop.item)
    kept_history(fix, "fix", loop.item)

    if pending.mechanism != "":
        fixes[pending.mechanism] = fixes.get(pending.mechanism, 0) + 1

    return struct(end = None, lane = fix)

def fix_until_candidate(loop, pending, fixes, decided, outer, lane):
    """The scoped loop: a fix of the open findings, then a scoped review of the head, up to MAX_INNER times. It runs no gate.
    Returns the end of the loop, or the lane whose head is the candidate. open holds the findings that the last scoped review left, and breach its read-only violations."""
    for inner in range(1, MAX_INNER + 1):
        # Only the first candidate after a clean implementer has no findings due. Its first review judges the diff of the implementer, as no fix ran.
        first = not pending.due
        if pending.due:
            fixed = scoped_fix(loop, pending, fixes, decided, outer, inner, lane)
            if fixed.end != None:
                return fixed
            lane = fixed.lane

        if stopped_before(loop.item, "before-scoped-review-%d" % outer, lane.head_sha, loop.guard) != None:
            return struct(end = ended(loop, lane.head_sha, stopped = True, gate = pending.gate, review = pending.review))

        name = FIRST_REVIEW if first else "scoped-review-%d" % inner
        scope = "the diff of the implementer" if first else "the diff of the last fix"
        review = agent(name = name, **review_args(loop, lane.head_sha, lane.violations, scope, []))
        capped(review, "review", loop.item)
        if review.verdict == "reject":
            return struct(end = ended(loop, lane.head_sha, rejected = True, gate = pending.gate, review = review))

        if verdict_of(loop, review) == "approve" and lane.violations == "" and review.violations == "":
            return struct(end = None, lane = lane, open = "", breach = "")

        pending = sent_back(with_violations(review.text, review.violations, lane.violations), mechanism_of(loop, review), pending.gate, review)

    # The last scoped review asked for changes. Its findings go to the full checks, and its violations keep the full review from a clean approve.
    return struct(end = None, lane = lane, open = pending.findings, breach = review.violations)

def review_fix(item, impl, review_prompt, fix_prompt, base, steps, rounds = 6, follow_ups = "allow", protect = [], design_prompt = "", guard = {}, allow = [], review_outputs = [], facts = {}, review_check = None, checks = [], design_after = DESIGN_AFTER, void_reruns = VOID_RERUNS, void_asks = VOID_ASKS, escalate_after = FIX_ESCALATE_AFTER):
    """Runs the loop and returns a struct with head, stopped, exhausted, rejected, design, held, gate, review, verdict, host_losses and host_lost. docs/design/durable-loop.md describes the loop.

    steps is a dict from a step name to {"model": text, "runner": text}. The entries implement, review and fix are required, design is required with a design_prompt, and fix_late is optional.
    The library holds no model. An empty runner means the pool pick. design_after, void_reruns, void_asks and escalate_after are the limits of the loop, and the constants of the same names are their defaults.
    base is the base branch of the item. The gate of each candidate runs on it, and the checks before review compare the head with origin/<base>.
    rounds caps the candidates. With 0 the loop runs no agent and ends exhausted at the head of the implementer. checks lists functions of one argument, the passed gate, that run after the checks before review and before the full review.
    Each returns an empty text for a clean head, or the findings as text, which send the candidate back to a fix.
    review_check is a function of one argument, the full review, that runs after each full review whose verdict is not reject. A reason ends the loop: held holds it.
    guard holds the settings of the preflight call before every agent of the loop: base, hold and own. A stop ends the loop with stopped set.
    protect lists the directories that no command of a review or a fix may write in. allow lists the globs that a fix may change.
    design_prompt turns on the design round: each review then writes the output `mechanism`. review_outputs lists more outputs of the full review.
    facts is the value of the facts() builtin, passed to each agent.
    follow_ups is "allow" or "forbid". With "forbid", each review writes the output `open`, one text with a line for each open finding and an empty text for none,
    and a review with an open finding has the verdict changes unless its verdict is reject. The result field verdict holds the verdict of the last full review with that rule applied.
    """
    if follow_ups not in ["allow", "forbid"]:
        fail("REFUSE: review_fix got follow_ups %r. Use \"allow\" or \"forbid\"." % (follow_ups,))

    kept_history(impl, "implement", item)

    loop = struct(
        item = item,
        impl = impl,
        base = base,
        review_prompt = review_prompt,
        fix_prompt = fix_prompt,
        follow_ups = follow_ups,
        protect = protect,
        extra = ["protected-path"] if protect else [],
        scope = {"allow_paths": allow} if allow else {},
        allow = allow,
        design_prompt = design_prompt,
        steps = checked_steps(steps, design_prompt),
        design_after = design_after,
        void_reruns = void_reruns,
        void_asks = void_asks,
        escalate_after = escalate_after,
        guard = guard,
        review_outputs = review_outputs,
        facts = facts,
        review_check = review_check,
        checks = checks,
        host = {"losses": 0, "lost": False},
        calls = {"fix": 0, "review": 0},
        review_cap = rounds * (MAX_INNER + 1),
        fix_cap = rounds * MAX_INNER,
    )
    lane = impl
    pending = sent_back(impl.violations, due = impl.violations != "")
    fixes = {}
    decided = {}

    for outer in range(1, rounds + 1):
        inner = fix_until_candidate(loop, pending, fixes, decided, outer, lane)
        if inner.end != None:
            return inner.end

        lane = inner.lane
        pending = check_candidate(loop, inner, outer)
        if pending.end != None:
            return pending.end

    return ended(loop, lane.head_sha, exhausted = True, gate = pending.gate, review = pending.review)
