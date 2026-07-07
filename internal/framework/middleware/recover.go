package middleware

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"go-base-agent/internal/framework/convention"
	"go-base-agent/internal/framework/exception"
)

func Recover() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic recovered",
					"path", c.Request.URL.Path,
					"method", c.Request.Method,
					"panic", r,
					"stack", string(debug.Stack()),
				)

				var appErr *exception.AppError
				switch err := r.(type) {
				case *exception.ClientError:
					appErr = &err.AppError
				case *exception.ServiceError:
					appErr = &err.AppError
				case *exception.RemoteError:
					appErr = &err.AppError
				case error:
					if errors.As(err, &appErr) {
						break
					}
					appErr = &exception.AppError{
						Code:    "B000001",
						Message: "系统执行出错",
					}
				default:
					appErr = &exception.AppError{
						Code:    "B000001",
						Message: "系统执行出错",
					}
				}

				c.JSON(http.StatusOK, convention.Failure(appErr.Code, appErr.Message))
				c.Abort()
			}
		}()
		c.Next()
	}
}
