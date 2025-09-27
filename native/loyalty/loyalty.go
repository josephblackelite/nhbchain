package loyalty

import nativecommon "nhbchain/native/common"

// Engine represents the native loyalty and rewards module.
type Engine struct {
	pauses nativecommon.PauseView
}

// NewEngine creates a new loyalty engine instance.
func NewEngine() *Engine { return &Engine{} }

// SetPauses configures the pause view to guard reward processing.
func (e *Engine) SetPauses(p nativecommon.PauseView) {
	if e == nil {
		return
	}
	e.pauses = p
}

// OnTransactionSuccess evaluates and applies applicable loyalty rewards for the
// supplied transaction context. Base rewards are processed first followed by
// program-specific rewards when the state implementation supports them.
func (e *Engine) OnTransactionSuccess(st BaseRewardState, ctx *BaseRewardContext) {
	if st == nil || ctx == nil {
		return
	}
	if err := nativecommon.Guard(e.pauses, moduleName); err != nil {
		return
	}
	e.ApplyBaseReward(st, ctx)

	if programState, ok := st.(ProgramRewardState); ok {
		programCtx := &ProgramRewardContext{BaseRewardContext: ctx}
		e.ApplyProgramReward(programState, programCtx)
	}
}
