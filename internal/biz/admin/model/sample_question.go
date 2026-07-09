package model

import "go-base-agent/internal/framework/db"

// SampleQuestion 对应 t_sample_question 表（示例问题）。
type SampleQuestion struct {
	db.BaseModel

	Title       string `gorm:"column:title;type:varchar(64)" json:"title"`
	Description string `gorm:"column:description;type:varchar(255)" json:"description"`
	Question    string `gorm:"column:question;type:varchar(255);not null" json:"question"`
}

func (SampleQuestion) TableName() string {
	return "t_sample_question"
}
