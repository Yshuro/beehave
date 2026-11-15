package model

// The names of the steps that the gate job of a candidate head runs apart. A gate list may not use them: the log of the job names a step by its name.
const (
	StepHeadGate  = "head-gate"
	StepSecrets   = "secret-scan"
	StepBaseSuite = "base-suite"
)

// ReservedGateSteps are the step names that the gate job of a candidate head takes.
func ReservedGateSteps() []string {
	return []string{StepHeadGate, StepSecrets, StepBaseSuite}
}

// GateSpec is one step of a gate list: a name and the command line that runs it.
type GateSpec struct {
	Name    string
	Command string
}
