package loyalty

// Engine represents the native loyalty and rewards module.
type Engine struct{}

// NewEngine creates a new loyalty engine instance.
func NewEngine() *Engine { return &Engine{} }

// OnTransactionSuccess evaluates and applies applicable loyalty rewards for the
// supplied transaction context. Base rewards are processed first followed by
// program-specific rewards when the state implementation supports them.
func (e *Engine) OnTransactionSuccess(st BaseRewardState, ctx *BaseRewardContext) {
	if st == nil || ctx == nil {
		return
	}
	e.ApplyBaseReward(st, ctx)

	if programState, ok := st.(ProgramRewardState); ok {
		programCtx := &ProgramRewardContext{BaseRewardContext: ctx}
		e.ApplyProgramReward(programState, programCtx)
	}
}
