package handler

import "github.com/gin-gonic/gin"

type response struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

func success(c *gin.Context, data any) {
	c.JSON(200, response{Code: 0, Message: "ok", Data: data})
}

func failure(c *gin.Context, status, code int, message string) {
	c.AbortWithStatusJSON(status, response{Code: code, Message: message, Data: nil})
}
