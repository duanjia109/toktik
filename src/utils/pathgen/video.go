package pathgen

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// GenerateRawVideoName 生成初始视频名称，此链接仅用于内部使用，暴露给用户的视频名称
// 该名称由输入的 actorId、title 和 videoId 经过 SHA-256 哈希计算得到，并转换为十六进制字符串。最终的文件名是十六进制字符串加上扩展名 .mp4。
func GenerateRawVideoName(actorId uint32, title string, videoId uint32) string {
	hash := sha256.Sum256([]byte("RAW" + strconv.FormatUint(uint64(actorId), 10) + title + strconv.FormatUint(uint64(videoId), 10)))
	return hex.EncodeToString(hash[:]) + ".mp4"
}

// GenerateFinalVideoName 最终暴露给用户的视频名称
// q:用这么多格式转换的目的？
func GenerateFinalVideoName(actorId uint32, title string, videoId uint32) string {
	hash := sha256.Sum256([]byte(strconv.FormatUint(uint64(actorId), 10) + title + strconv.FormatUint(uint64(videoId), 10)))
	return hex.EncodeToString(hash[:]) + ".mp4"
}

// GenerateCoverName 生成视频封面名称
func GenerateCoverName(actorId uint32, title string, videoId uint32) string {
	hash := sha256.Sum256([]byte(strconv.FormatUint(uint64(actorId), 10) + title + strconv.FormatUint(uint64(videoId), 10)))
	return hex.EncodeToString(hash[:]) + ".png"
}

// GenerateAudioName 生成音频链接，此链接仅用于内部使用，不暴露给用户
// 接受一个视频文件名作为参数，并返回一个基于该文件名生成的音频文件名。
func GenerateAudioName(videoFileName string) string {
	hash := sha256.Sum256([]byte("AUDIO_" + videoFileName))
	return hex.EncodeToString(hash[:]) + ".mp3"
}

// GenerateNameWatermark 生成用户名水印图片名
func GenerateNameWatermark(actorId uint32, Name string) string {
	hash := sha256.Sum256([]byte("Watermark" + strconv.FormatUint(uint64(actorId), 10) + Name))
	return hex.EncodeToString(hash[:]) + ".png"
}
