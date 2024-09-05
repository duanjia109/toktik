package file

import (
	"GuGoTik/src/constant/config"
	"context"
	"fmt"
	"io"
)

var client storageProvider //client对外不公开，只用作内部调用

type storageProvider interface {
	Upload(ctx context.Context, fileName string, content io.Reader) (*PutObjectOutput, error)
	GetLink(ctx context.Context, fileName string) (string, error)
	GetLocalPath(ctx context.Context, fileName string) string
	IsFileExist(ctx context.Context, fileName string) (bool, error)
}

type PutObjectOutput struct{}

func init() {
	//note：本项目用的是fs类型存储
	switch config.EnvCfg.StorageType { // Append more type here to provide more file action ability
	case "fs":
		client = FSStorage{}
	}
}

// Upload
// note：这是对外提供的接口
// 把content中的内容写入到"FileSystemStartPath + fileName"路径中
//
//	@Description:
//	@param ctx
//	@param fileName
//	@param content
//	@return *PutObjectOutput
//	@return error
func Upload(ctx context.Context, fileName string, content io.Reader) (*PutObjectOutput, error) {
	return client.Upload(ctx, fileName, content)
}

// GetLocalPath
//
//	@Description: 返回路径 FileSystemStartPath + fileName
//	@param ctx
//	@param fileName
//	@return string
func GetLocalPath(ctx context.Context, fileName string) string {
	return client.GetLocalPath(ctx, fileName)
}

// GetLink
// 返回视频访问链接
//
//	@Description: 根据fileName和userId返回访问视频访问链接："FileSystemBaseUrl+filename?user_id=userId"
//	@param ctx
//	@param fileName
//	@param userId
//	@return link
//	@return err
func GetLink(ctx context.Context, fileName string, userId uint32) (link string, err error) {
	originLink, err := client.GetLink(ctx, fileName)
	link = fmt.Sprintf("%s?user_id=%d", originLink, userId) //Link格式
	return
}

func IsFileExist(ctx context.Context, fileName string) (bool, error) {
	return client.IsFileExist(ctx, fileName)
}
