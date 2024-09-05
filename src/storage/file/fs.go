package file

import (
	"GuGoTik/src/constant/config"
	"GuGoTik/src/extra/tracing"
	"GuGoTik/src/utils/logging"
	"context"
	"github.com/sirupsen/logrus"
	"io"
	"net/url"
	"os"
	"path"
)

type FSStorage struct {
}

// GetLocalPath
//
//	@Description: 返回fileName的文件系统路径
//	@receiver f
//	@param ctx
//	@param fileName
//	@return string  file的文件系统路径
func (f FSStorage) GetLocalPath(ctx context.Context, fileName string) string {
	_, span := tracing.Tracer.Start(ctx, "FSStorage-GetLocalPath")
	defer span.End()
	logging.SetSpanWithHostname(span)
	return path.Join(config.EnvCfg.FileSystemStartPath, fileName)
}

// Upload
// 计算filename的文件系统路径，并把文件写入该路径
//
//	@Description: q:写入到filepath：FileSystemStartPath, fileName
//	@receiver f
//	@param ctx
//	@param fileName  文件名称
//	@param content  要写入的数据读取对象
//	@return output
//	@return err
func (f FSStorage) Upload(ctx context.Context, fileName string, content io.Reader) (output *PutObjectOutput, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "FSStorage-Upload")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("FSStorage.Upload").WithContext(ctx)

	logger = logger.WithFields(logrus.Fields{
		"file_name": fileName,
	})
	logger.Debugf("Process start")

	//step：读取文件
	all, err := io.ReadAll(content)
	if err != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Debug("Failed reading content")
		return nil, err
	}

	//config.EnvCfg.FileSystemStartPath: 环境变量FS_PATH=/usr/share/nginx/html/
	//FS_BASEURL=http://192.168.124.33:8066/
	//step：计算路径
	filePath := path.Join(config.EnvCfg.FileSystemStartPath, fileName) //q：FileSystemStartPath是什么？

	dir := path.Dir(filePath)
	//step：创建文件夹
	err = os.MkdirAll(dir, os.FileMode(0755))

	if err != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Debug("Failed creating directory before writing file")
		return nil, err
	}

	//step：写入文件内容到文件系统
	err = os.WriteFile(filePath, all, os.FileMode(0755))

	if err != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Debug("Failed writing content to file")
		return nil, err
	}

	return &PutObjectOutput{}, nil
}

// 又是返回path，q:和之前的有什么不一样？
func (f FSStorage) GetLink(ctx context.Context, fileName string) (string, error) {
	_, span := tracing.Tracer.Start(ctx, "FSStorage-GetLink")
	defer span.End()
	logging.SetSpanWithHostname(span)
	//eg:FS_BASEURL=http://192.168.124.33:8066/
	return url.JoinPath(config.EnvCfg.FileSystemBaseUrl, fileName) //q：FileSystemBaseUrl是什么？
}

// IsFileExist
//
//	@Description: 判断文件是否存在文件系统中
//	@receiver f
//	@param ctx
//	@param fileName  文件名
//	@return bool  true为存在
//	@return error
func (f FSStorage) IsFileExist(ctx context.Context, fileName string) (bool, error) {
	_, span := tracing.Tracer.Start(ctx, "FSStorage-IsFileExist")
	defer span.End()
	logging.SetSpanWithHostname(span)
	filePath := path.Join(config.EnvCfg.FileSystemStartPath, fileName)
	_, err := os.Stat(filePath)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}

	return false, err
}
