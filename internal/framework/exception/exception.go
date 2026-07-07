package exception

import "fmt"

type AppError struct {
	Code    string
	Message string
	Err     error
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s (cause: %v)", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *AppError) Unwrap() error {
	return e.Err
}

type ClientError struct {
	AppError
}

func NewClientError(message string) *ClientError {
	return NewClientErrorWithCode(message, "A000001")
}

func NewClientErrorWithCode(message, code string) *ClientError {
	return &ClientError{AppError{Code: code, Message: message}}
}

type ServiceError struct {
	AppError
}

func NewServiceError(message string) *ServiceError {
	return NewServiceErrorWithCode(message, "B000001")
}

func NewServiceErrorWithCode(message, code string) *ServiceError {
	return &ServiceError{AppError{Code: code, Message: message}}
}

type RemoteError struct {
	AppError
}

func NewRemoteError(message string) *RemoteError {
	return NewRemoteErrorWithCode(message, "C000001")
}

func NewRemoteErrorWithCode(message, code string) *RemoteError {
	return &RemoteError{AppError{Code: code, Message: message}}
}
