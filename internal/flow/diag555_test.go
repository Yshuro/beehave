package flow

import (
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// diagDump is a temporary diagnostic of issue 555. It logs every workflow row.
func diagDump(t *testing.T, engine *Engine, label string) {
	t.Helper()

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterLoadInput(true), dbos.WithFilterLoadOutput(true))
	if err != nil {
		t.Logf("DIAG %s: list: %v", label, err)

		return
	}

	for _, r := range rows {
		t.Logf("DIAG %s: id=%s name=%s status=%s queue=%s part=%s created=%d parent=%s attempts=%d err=%v out=%v in=%v", label, r.ID, r.Name, r.Status, r.QueueName,
			r.QueuePartitionKey, r.CreatedAt.UnixMicro(), r.ParentWorkflowID, r.Attempts, r.Error, r.Output, r.Input)
	}
}
