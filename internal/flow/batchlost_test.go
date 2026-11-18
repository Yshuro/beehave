package flow

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/batch"
	"github.com/tobocop2/beehave/internal/model"
)

// lostBatchRig is a batch rig whose build host is gpu-b, with a clock that moves a minute at each read of the proof and a host that proves each loss.
func lostBatchRig(t *testing.T, lose int) (*batchRig, *upHosts) {
	t.Helper()

	rig := newBatchRig(t)
	clock := newReadingClock()
	hosts := &upHosts{started: true, proofs: []model.LostProof{{Kind: model.LostStopped}}, afterProof: func() { clock.advance(time.Minute) }}
	rig.h.hosts, rig.h.reading, rig.h.facts = hosts, clock.read, lostFacts(time.Hour)
	rig.gate.lose = lose

	return rig, hosts
}

// lostBatchBody starts the host gpu-b and lands items 1 and 3 there, then reports the gate count and the loss fields of the result.
func (r *batchRig) lostBatchBody() string {
	return "host_up(name = \"start\", host = \"gpu-b\")\n" + r.call("1 3", "") +
		`command(name = "losses", run = ["losses", str(out.gates), str(out.host_losses)] + [l["kind"] + ":" + l["job"] + ":" + l["host"] + ":" + l["proof"] + ":" + l["head_or_commit"] for l in out.host_loss])
`
}

func TestBatchGate_ALostJobWithAProofRerunsOnTheSameCombinedCommitAndLands(t *testing.T) {
	t.Parallel()

	rig, hosts := lostBatchRig(t, 1)
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(rig.lostBatchBody())))

	require.NoError(t, err)
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed(), "the rerun passed, so the batch lands")
	require.Len(t, rig.host.checkouts, 2, "the gate ran once more after the proof")
	assert.Equal(t, rig.host.checkouts[0], rig.host.checkouts[1], "the rerun gates the same combined commit")
	assert.Equal(t, []GateJob{{ID: "job-1"}, {ID: "job-2"}}, rig.gate.waitedOn(), "each gate waits on the job that its first step recorded")
	assert.Equal(t, 2, rig.gate.submitted(), "the lost job and the rerun")
	assert.Equal(t, []string{"gpu-b/job-1"}, hosts.proved, "the proof of the lost job is read on the host it ran on")
	assert.Equal(t, rig.repo.must(t, "rev-parse", rig.host.checkouts[0]+"^{tree}"), rig.merger.given[len(rig.merger.given)-1].GatedTree, "the members land on the tree of the union")

	ran := rig.h.commands.ran()
	require.Len(t, ran, 2)
	assert.Equal(t, "losses 1 1 batch_gate:job-1:gpu-b:stopped:"+rig.host.checkouts[0], ran[1], "one gate run, one host_loss record and host_losses = 1")
	assert.Contains(t, stepNamesOf(t, engine, testRun), "batch:b.gate#1/host-loss", "the item run records the loss as its own step")
}

func TestBatchGate_ASecondProvenLossInOneCallEndsTheCallWithAHostCauseAndBlamesNoMember(t *testing.T) {
	t.Parallel()

	rig, hosts := lostBatchRig(t, 2)
	hosts.proofs = []model.LostProof{{Kind: model.LostStopped}, {Kind: model.LostGone}}
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(rig.lostBatchBody())))

	require.ErrorIs(t, err, batch.ErrGateVoid)
	assert.Contains(t, err.Error(), "the gate job job-1 was lost on gpu-b and the proof is stopped", "the error names the host and the first proof")
	assert.Contains(t, err.Error(), "the gate job job-2 was lost on gpu-b and the proof is gone", "the error names the second proof")
	assert.Contains(t, err.Error(), "No member is blamed")
	assert.Empty(t, rig.merger.landed(), "no member lands")
	assert.Empty(t, rig.h.commands.ran(), "the call has no result")
	assert.Equal(t, 2, rig.gate.submitted(), "the second loss ends the call: no third gate")
	assert.Equal(t, []string{"gpu-b/job-1", "gpu-b/job-2"}, hosts.proved)
	assert.Subset(t, stepNamesOf(t, engine, testRun), []string{"batch:b.gate#1/host-loss", "batch:b.gate#1@2/host-loss"}, "each loss is a step of the item run")
	assert.EqualValues(t, 2, statusJSONOf(t, rig.h, testRun)[hostLossesKey], "the count is read from the status")
}

func TestBatchGate_TheHostJobIsRecordedBeforeTheGateWaits(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.hosts = &upHosts{started: true}
	rig.gate.started = make(chan struct{}, 1)
	file := writeFlow(t, bodyFlow("host_up(name = \"start\", host = \"gpu-b\")\n"+rig.call("1 3", "")))

	stopCtx, stop := context.WithCancel(t.Context())
	first, err := NewEngine(stopCtx, rig.h.opts, rig.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	ended := runInBackground2(first, file)

	<-rig.gate.started
	stop()
	require.NoError(t, first.Shutdown())
	<-ended

	require.Equal(t, 1, rig.gate.submitted(), "the first engine started the host job")

	next := rig.engine(t)
	outcome := <-runInBackground2(next, file)

	require.NoError(t, outcome.err)
	assert.Equal(t, 1, rig.gate.submitted(), "the restart waits on the recorded job and submits none")
	assert.Equal(t, []GateJob{{ID: "job-1"}, {ID: "job-1"}}, rig.gate.waitedOn(), "both waits are on the one recorded job")
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed())
}

// heldBatchRig is a lost batch rig whose host gives no proof until the test proves it, with a wait limit of two minutes.
func heldBatchRig(t *testing.T) (*batchRig, *upHosts) {
	t.Helper()

	rig, hosts := lostBatchRig(t, 1)
	hosts.proofs = nil
	rig.h.facts = lostFacts(2 * time.Minute)

	return rig, hosts
}

func TestBatchGate_NoProofAtTheLimitAsksTheOwnerAndRerunsAfterTheProof(t *testing.T) {
	t.Parallel()

	rig, hosts := heldBatchRig(t)
	engine := rig.engine(t)
	client := rig.h.client(t, testVersion)

	done := runInBackground2(engine, writeFlow(t, bodyFlow(rig.lostBatchBody())))
	waitForTheLostJob(t, rig.h, testItem)

	hosts.prove(model.LostProof{Kind: model.LostEnded})
	eventually(t, func() bool { return client.Answer(testItem, "CONTINUE") == nil })

	outcome := <-done

	require.NoError(t, outcome.err)
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed(), "the proof after the answer reruns the gate, and the batch lands")
	assert.Equal(t, 2, rig.gate.submitted(), "no gate ran on the commit before the proof")
	assert.Contains(t, stepNamesOf(t, engine, testRun), "batch:b.gate#1/lost-ask.0", "the read of the proof after the answer is a step of the item run")
	assert.Equal(t, "losses 1 1 batch_gate:job-1:gpu-b:ended:"+rig.host.checkouts[0], rig.h.commands.ran()[1])
}

func TestBatchGate_AReadOfTheProofAfterTheAnswerThatFailsFailsTheCallAndRerunsNothing(t *testing.T) {
	t.Parallel()

	rig, hosts := heldBatchRig(t)
	engine := rig.engine(t)
	client := rig.h.client(t, testVersion)

	done := runInBackground2(engine, writeFlow(t, bodyFlow(rig.lostBatchBody())))
	waitForTheLostJob(t, rig.h, testItem)

	hosts.mu.Lock()
	hosts.proofErr = errFake
	hosts.mu.Unlock()
	eventually(t, func() bool { return client.Answer(testItem, "CONTINUE") == nil })

	outcome := <-done

	require.ErrorContains(t, outcome.err, "the batch gate lost its job: ")
	require.ErrorIs(t, outcome.err, errFake)
	assert.Equal(t, 1, rig.gate.submitted(), "no gate runs on the commit without a proof")
	assert.Empty(t, rig.merger.landed())
}

func TestBatchGate_AReadOfTheProofThatFailsInTheTimedWaitFailsTheCallAndRecordsNoLoss(t *testing.T) {
	t.Parallel()

	rig, hosts := lostBatchRig(t, 1)
	hosts.proofErr = errFake
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(rig.lostBatchBody())))

	require.ErrorContains(t, err, "read the proof that the lost job job-1 on host gpu-b can no longer run: ")
	require.ErrorIs(t, err, errFake)
	assert.Equal(t, 1, rig.gate.submitted(), "no gate runs on the commit without a proof")
	assert.Empty(t, rig.merger.landed())
	assert.NotContains(t, stepNamesOf(t, engine, testRun), "batch:b.gate#1/host-loss", "a wait that failed records no loss")
}

func TestBatchGate_ACanceledWaitForTheProofReadsTheProofOnceAndNeverCancelsTheJob(t *testing.T) {
	t.Parallel()

	rig, hosts := heldBatchRig(t)
	rig.h.lostPoll = time.Hour
	engine := rig.engine(t)

	done := runInBackground2(engine, writeFlow(t, bodyFlow(rig.lostBatchBody())))

	// The first look gives no proof, and the wait sleeps for an hour before the next.
	eventually(t, func() bool {
		ids := exclusiveRunsOf(t, engine, kindBatchGate)
		if len(ids) != 1 {
			return false
		}

		steps, err := dbos.GetWorkflowSteps(engine.ctx, ids[0])

		return err == nil && slices.ContainsFunc(steps, func(s dbos.StepInfo) bool { return s.StepName == "DBOS.sleep" })
	})
	require.Equal(t, 1, hosts.proofReads())

	require.NoError(t, dbos.CancelWorkflow(engine.ctx, testRun, dbos.WithCancelChildren()))
	eventually(t, func() bool {
		rig.h.tick(t)

		return hosts.proofReads() == 2
	})

	require.Error(t, (<-done).err, "the canceled run has no result")
	assert.Equal(t, 2, hosts.proofReads(), "the canceled wait reads the proof once more, and never again")
	assert.Equal(t, []string{"gpu-b/job-1", "gpu-b/job-1"}, hosts.proved)
	assert.Zero(t, hosts.teardownCount(), "the wait never banks or stops the host")
	assert.Equal(t, 1, rig.gate.submitted(), "the wait submits no job")
	assert.Empty(t, rig.merger.landed())
}

func TestBatchGate_AProgramGateWhoseCheckoutFailsFailsTheCall(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.host.checkErr = errFake

	_, err := rig.walk(t, rig.call("1 3", `gate_run = ["check-all"], `))

	require.ErrorIs(t, err, errFake)
	assert.Empty(t, rig.h.commands.ran(), "the program does not run on a commit that is not checked out")
}

func TestBatchResult_ALossRecordThatAWorkflowCannotHoldFailsTheResult(t *testing.T) {
	t.Parallel()

	_, err := batchResult(batch.Result{}, batch.Report{}, nil, []hostLoss{{Loss: func() {}}})

	require.ErrorContains(t, err, "the host loss records of batch_merge: ")
}

func TestBatchGate_AGateRunProgramIsOneStepAndSubmitsNoHostJob(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.hosts = &upHosts{started: true}
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow("host_up(name = \"start\", host = \"gpu-b\")\n"+rig.call("1 3", `gate_run = ["check-all"], `))))

	require.NoError(t, err)
	assert.Zero(t, rig.gate.submitted(), "a program gate has no host job")
	assert.Equal(t, []string{kindBatchGate}, batchGateSteps(t, engine), "a program gate is one step")
}

// batchGateSteps lists the step names of the one exclusive run of a batch gate.
func batchGateSteps(t *testing.T, engine *Engine) []string {
	t.Helper()

	ids := exclusiveRunsOf(t, engine, kindBatchGate)
	require.Len(t, ids, 1, "one batch gate ran")

	return stepNamesOf(t, engine, ids[0])
}

// exclusiveRunsOf lists the ids of the runs of one kind that recorded a step: the exclusive run of a call on this machine, or the host job of a call on a build host.
func exclusiveRunsOf(t *testing.T, engine *Engine, kind string) []string {
	t.Helper()

	var ids []string

	for _, name := range []string{exclusiveName, hostJobName} {
		rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(name), dbos.WithFilterLoadInput(true))
		require.NoError(t, err)

		for _, row := range rows {
			var in ExclusiveInput

			text, isText := row.Input.(string)
			require.True(t, isText)
			require.NoError(t, json.Unmarshal([]byte(text), &in))

			if in.Kind == kind && (name == hostJobName || in.HostKey == "") {
				ids = append(ids, row.ID)
			}
		}
	}

	return ids
}
