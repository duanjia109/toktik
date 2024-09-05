package models

import (
	"GuGoTik/src/storage/database"
	"gorm.io/gorm"
)

type RawVideo struct {
	ActorId   uint32
	VideoId   uint32 `gorm:"not null;primaryKey"`
	Title     string
	FileName  string
	CoverName string
	gorm.Model
}

// 使用 GORM ORM（对象关系映射）库自动迁移数据库表结构
func init() {
	if err := database.Client.AutoMigrate(&RawVideo{}); err != nil {
		panic(err)
	}
}
