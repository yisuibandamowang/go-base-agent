package convention

type Result[T any] struct {
	Code      string `json:"code"`
	Message   string `json:"message,omitempty"`
	Data      T      `json:"data,omitempty"`
	RequestID string `json:"requestId,omitempty"`
}

const SuccessCode = "0"

func Success[T any](data T) *Result[T] {
	return &Result[T]{Code: SuccessCode, Data: data}
}

func Failure(code, message string) *Result[any] {
	return &Result[any]{Code: code, Message: message}
}

func DefaultFailure() *Result[any] {
	return &Result[any]{Code: "B000001", Message: "系统执行出错"}
}
