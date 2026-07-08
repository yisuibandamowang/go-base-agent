package middleware

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const keyDB = "db"

// DB 注入 *gorm.DB 到 gin.Context。
func DB(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(keyDB, db)
		c.Next()
	}
}

// GetDB 从 gin.Context 获取 *gorm.DB。
func GetDB(c *gin.Context) *gorm.DB {
	v, _ := c.Get(keyDB)
	if v == nil {
		return nil
	}
	return v.(*gorm.DB)
}
