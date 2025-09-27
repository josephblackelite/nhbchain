package modules

const (
	codeInvalidParams = -32602
	codeServerError   = -32000
)

type ModuleError struct {
	HTTPStatus int
	Code       int
	Message    string
	Data       interface{}
}

func (e *ModuleError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}
