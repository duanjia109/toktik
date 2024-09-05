package main

import (
	"GuGoTik/src/constant/config"
	strings2 "GuGoTik/src/constant/strings"
	"GuGoTik/src/extra/tracing"
	"GuGoTik/src/models"
	"GuGoTik/src/rpc/comment"
	"GuGoTik/src/rpc/user"
	"GuGoTik/src/storage/database"
	"GuGoTik/src/storage/file"
	grpc2 "GuGoTik/src/utils/grpc"
	"GuGoTik/src/utils/logging"
	"GuGoTik/src/utils/pathgen"
	"GuGoTik/src/utils/rabbitmq"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/sashabaranov/go-openai"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm/clause"
	"net/http"
	url2 "net/url"
	"os/exec"
	"strings"
	"sync"
)

var (
	userClient    user.UserServiceClient
	commentClient comment.CommentServiceClient
	openaiClient  *openai.Client
	delayTime     = int32(2 * 60 * 1000) //2 minutes
	maxRetries    = int32(3)
)

var conn *amqp.Connection
var channel *amqp.Channel

func init() {
	cfg := openai.DefaultConfig(config.EnvCfg.ChatGPTAPIKEYS)

	url, err := url2.Parse(config.EnvCfg.ChatGptProxy)
	if err != nil {
		panic(err)
	}
	cfg.HTTPClient = &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(url),
		},
	}

	openaiClient = openai.NewClientWithConfig(cfg)
}

// main函数中调用了，做一些准备工作，如新建微服务客户端，交换机
func ConnectServiceClient() {

	//初始化几个服务客户端
	userRpcConn := grpc2.Connect(config.UserRpcServerName)
	userClient = user.NewUserServiceClient(userRpcConn)
	commentRpcConn := grpc2.Connect(config.CommentRpcServerName)
	commentClient = comment.NewCommentServiceClient(commentRpcConn)

	var err error

	conn, err = amqp.Dial(rabbitmq.BuildMQConnAddr())
	exitOnError(err)

	channel, err = conn.Channel()
	exitOnError(err)

	err = channel.ExchangeDeclare(
		strings2.EventExchange,
		"topic",
		true,
		false,
		false,
		false,
		nil,
	)
	exitOnError(err)
}

func CloseMQConn() {
	if err := conn.Close(); err != nil {
		panic(err)
	}

	if err := channel.Close(); err != nil {
		panic(err)
	}
}

// produceKeywords
//
//	@Description: 将event发布到 "event" - video.publish.action
//	@param ctx
//	@param event
func produceKeywords(ctx context.Context, event models.RecommendEvent) {
	ctx, span := tracing.Tracer.Start(ctx, "KeywordsEventPublisher")
	defer span.End()
	logger := logging.LogService("VideoSummaryService.KeywordsEventPublisher").WithContext(ctx)
	data, err := json.Marshal(event)
	if err != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Error when marshal the event model")
		logging.SetSpanError(span, err)
		return
	}

	err = channel.PublishWithContext(ctx,
		strings2.EventExchange,
		strings2.VideoPublishEvent,
		false,
		false,
		amqp.Publishing{
			ContentType: "text/plain",
			Body:        data,
		},
	)
	if err != nil { //q:发送失败怎么办？
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Error when publishing the event model")
		logging.SetSpanError(span, err)
		return
	}
}

// errorHandler If `requeue` is false, it will just `Nack` it. If `requeue` is true, it will try to re-publish it.

// 重新发布到 "video_exchange" - "video_summary"
//
// errorHandler
// 用于处理 RabbitMQ 消息处理过程中的错误
//
//	@Description:
//	@param channel  用于与 RabbitMQ 服务器通信的通道
//	@param d  RabbitMQ消息的交付信息，包括消息内容和属性
//	@param requeue  布尔值，指示是否应重新发布消息。
//	false：则丢弃消息并记录错误；
//	true：则检查重试次数。如果达到最大重试次数，则丢弃消息并记录错误；否则，增加重试次数并重新发布消息，发布到VideoSummary
//	@param logger  日志记录器，用于记录日志
//	@param span  用于分布式追踪的 span 对象，跟踪消息处理的整个生命周期
func errorHandler(channel *amqp.Channel, d amqp.Delivery, requeue bool, logger *logrus.Entry, span *trace.Span) {
	if !requeue { // Nack the message
		err := d.Nack(false, false)
		if err != nil {
			logger.WithFields(logrus.Fields{
				"err": err,
			}).Errorf("Error when nacking the video...")
			logging.SetSpanError(*span, err)
		}
	} else { // Re-publish the message
		curRetry, ok := d.Headers["x-retry"].(int32)
		if !ok {
			curRetry = 0
		}
		if curRetry >= maxRetries {
			logger.WithFields(logrus.Fields{
				"body": d.Body,
			}).Errorf("Maximum retries reached for message.")
			logging.SetSpanError(*span, errors.New("maximum retries reached for message"))
			err := d.Ack(false)
			if err != nil {
				logger.WithFields(logrus.Fields{
					"err": err,
				}).Errorf("Error when dealing with the video...")
			}
		} else {
			curRetry++
			headers := d.Headers
			headers["x-delay"] = delayTime
			headers["x-retry"] = curRetry

			err := d.Ack(false)
			if err != nil {
				logger.WithFields(logrus.Fields{
					"err": err,
				}).Errorf("Error when dealing with the video...")
			}

			logger.Debugf("Retrying %d times", curRetry)

			err = channel.PublishWithContext(context.Background(),
				strings2.VideoExchange,
				strings2.VideoSummary,
				false,
				false,
				amqp.Publishing{
					DeliveryMode: amqp.Persistent,
					ContentType:  "text/plain",
					Body:         d.Body,
					Headers:      headers,
				},
			)
			if err != nil {
				logger.WithFields(logrus.Fields{
					"err": err,
				}).Errorf("Error when re-publishing the video to queue...")
				logging.SetSpanError(*span, err)
			}
		}
	}
}

// SummaryConsume
//
// @Description:
//
//	1.从"video_summary"通道接收消息，
//
//	2.视频 - 音频 - 文本 - 总结、关键字等，更新到数据库中，对生成错误的情况进行mq的重新发布，
//	3.把keywors发布到"event" - video.publish.action中，用于后续的推荐算法
//	4.让magic user发布视频的kyewords和summary评论：视频总结，视频关键字
//	5.更新数据库，确认mq消息已成功消费
//
// @param channel
func SummaryConsume(channel *amqp.Channel) {
	//step：从"video_summary"队列接收消息
	msg, err := channel.Consume(strings2.VideoSummary, "", false, false, false, false, nil)
	if err != nil {
		panic(err)
	}

	for d := range msg {
		//解包 Otel Context
		ctx := rabbitmq.ExtractAMQPHeaders(context.Background(), d.Headers)
		ctx, span := tracing.Tracer.Start(ctx, "VideoSummaryService")
		logger := logging.LogService("VideoSummary").WithContext(ctx)

		var raw models.RawVideo
		if err := json.Unmarshal(d.Body, &raw); err != nil {
			logger.WithFields(logrus.Fields{
				"err": err,
			}).Errorf("Error when unmarshaling the prepare json body.")
			logging.SetSpanError(span, err)

			errorHandler(channel, d, false, logger, &span) //q: 解包失败为什么不重新发布？
			span.End()
			continue
		}
		logger.WithFields(logrus.Fields{
			"RawVideo": raw.VideoId,
		}).Debugf("Receive message of video %d", raw.VideoId)

		//step：Video -> Audio(声音的)
		audioFileName := pathgen.GenerateAudioName(raw.FileName) //获取音频文件名
		isAudioFileExist, _ := file.IsFileExist(ctx, audioFileName)
		if !isAudioFileExist { //不存在
			audioFileName, err = video2Audio(ctx, raw.FileName) //转化成audio
			if err != nil {
				logger.WithFields(logrus.Fields{
					"err":             err,
					"video_file_name": raw.FileName,
				}).Errorf("Failed to transform video to audio")
				logging.SetSpanError(span, err)

				errorHandler(channel, d, false, logger, &span) //q:false和true的使用场景到底什么区别？
				span.End()
				continue
			}

			//note：Save audio_file_name to db
			video := &models.Video{
				ID:            raw.VideoId,
				AudioFileName: audioFileName,
			}
			//在数据库中创建一个新的视频记录，如果记录的 id 已经存在，则更新 audio_file_name 字段
			//step:在数据库中更新"audio_file_name"字段
			result := database.Client.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "id"}},
				DoUpdates: clause.AssignmentColumns([]string{"audio_file_name"}),
			}).Create(&video)
			if result.Error != nil {
				logger.WithFields(logrus.Fields{
					"Err":           result.Error,
					"ID":            raw.VideoId,
					"AudioFileName": audioFileName,
				}).Errorf("Error when updating audio file name to database")
				logging.SetSpanError(span, result.Error)
			}
		} else { //video已存在
			logger.WithFields(logrus.Fields{
				"VideoId": raw.VideoId,
			}).Debugf("Video %d already exists audio", raw.VideoId)
		}

		//step： 音频 - 文本（副本）  -  同上
		transcriptExist, transcript, err := isTranscriptExist(raw.VideoId) //判断Transcript字段是否已经存在
		if err != nil {
			logger.WithFields(
				logrus.Fields{
					"err":     err,
					"VideoId": raw.VideoId,
				}).Errorf("Faild to get transcript of video %d from database", raw.VideoId)
		}
		if !transcriptExist { //如果不存在就转录
			transcript, err = speech2Text(ctx, audioFileName) //把音频转为文本，返回转化的文本
			if err != nil {
				logger.WithFields(logrus.Fields{
					"err":             err,
					"audio_file_name": audioFileName,
				}).Errorf("Failed to get transcript of an audio from ChatGPT")
				logging.SetSpanError(span, err)

				errorHandler(channel, d, true, logger, &span) //note:出现错误，重新发布消息
				span.End()
				continue
			}
		} else { //存在就不转录
			logger.WithFields(logrus.Fields{
				"VideoId": raw.VideoId,
			}).Debugf("Video %d already exists transcript", raw.VideoId)
		}

		var (
			summary  string
			keywords string
		)

		//step：Transcript -> Summary
		summaryChannel := make(chan string)
		summaryErrChannel := make(chan error)
		summaryExist, summary, err := isSummaryExist(raw.VideoId) //从数据库查找videoid是否存在summary字段
		if err != nil {
			logger.WithFields(
				logrus.Fields{
					"err":     err,
					"VideoId": raw.VideoId,
				}).Errorf("Faild to get summary of video %d from database", raw.VideoId)
		}
		if !summaryExist { //不存在则生成总结，由summaryChannel接收
			//q：没放进数据库？
			go text2Summary(ctx, transcript, &summaryChannel, &summaryErrChannel) //note:生成总结
		} else {
			logger.WithFields(logrus.Fields{
				"VideoId": raw.VideoId,
			}).Debugf("Video %d already exists summary", raw.VideoId)
		}

		//step：Transcript -> Keywords
		keywordsChannel := make(chan string)
		keywordsErrChannel := make(chan error)
		keywordsExist, keywords, err := isKeywordsExist(raw.VideoId) //判断是否已经存在keywords
		if err != nil {
			logger.WithFields(
				logrus.Fields{
					"err":     err,
					"VideoId": raw.VideoId,
				}).Errorf("Faild to get keywords of video %d from database", raw.VideoId)
		}
		if !keywordsExist { //不存在则生成
			go text2Keywords(ctx, transcript, &keywordsChannel, &keywordsErrChannel)
		} else {
			logger.WithFields(logrus.Fields{
				"VideoId": raw.VideoId,
			}).Debugf("Video %d already exists keywords", raw.VideoId)
		}

		summaryOrKeywordsErr := false //summary 和 keywords 任何一个生成出现问题都为true

		//有点像二次检查，检查在不存在的时候是否生成成功
		if !summaryExist {
			select {
			case summary = <-summaryChannel:
			case err = <-summaryErrChannel:
				logger.WithFields(logrus.Fields{
					"err":             err,
					"audio_file_name": audioFileName,
				}).Errorf("Failed to get summary of an audio from ChatGPT")
				logging.SetSpanError(span, err)
				summary = ""
				summaryOrKeywordsErr = true
			}
		}

		if !keywordsExist {
			select {
			case keywords = <-keywordsChannel:
			case err = <-keywordsErrChannel:
				logger.WithFields(logrus.Fields{
					"err":             err,
					"audio_file_name": audioFileName,
				}).Errorf("Failed to get keywords of an audio from ChatGPT")
				logging.SetSpanError(span, err)
				keywords = ""
				summaryOrKeywordsErr = true
			}
		}

		//step： 把keywords：发布到 "event" - video.publish.action
		//q: 这里发布到 video.publish.action 队列有什么用？ 用于后续的推荐视频算法里
		if !keywordsExist && keywords != "" { //不存在且不为空，代表没出错
			wg := sync.WaitGroup{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				produceKeywords(ctx, models.RecommendEvent{
					ActorId: raw.ActorId,
					VideoId: []uint32{raw.VideoId},
					Type:    3,
					Source:  config.VideoProcessorRpcServiceName,
					Title:   raw.Title,
					Tag:     strings.Split(keywords, " | "),
				})
			}()
			wg.Wait()
		}

		// step：magic user生成keywords和summary的评论
		isMagicUserExistRes := isMagicUserExist(ctx, logger, &span)
		if isMagicUserExistRes {
			logger.Debug("Magic user exist")
			//step：生成summary评论
			if !summaryExist && summary != "" { //在数据库表里不存在但生成成功了
				summaryCommentContent := "视频总结：" + summary
				logger.WithFields(logrus.Fields{
					"SummaryCommentContent": summaryCommentContent,
					"VideoId":               raw.VideoId,
				}).Debugf("Add summary comment to video")
				//note： 原来是让magic添加评论
				addMagicComment(raw.VideoId, summaryCommentContent, ctx, logger, &span)
			}

			//step：添加keywords 评论 by magic user
			if !keywordsExist && keywords != "" { //同上
				keywordsCommentContent := "视频关键词：" + keywords
				logger.WithFields(logrus.Fields{
					"KeywordsCommentContent": keywordsCommentContent,
					"VideoId":                raw.VideoId,
				}).Debugf("Add keywords comment to video")
				//note：对应的
				addMagicComment(raw.VideoId, keywordsCommentContent, ctx, logger, &span)
			}
		}

		//step : 更新数据库中的summary，keywords等信息
		video := &models.Video{
			ID:            raw.VideoId,
			AudioFileName: audioFileName,
			Transcript:    transcript,
			Summary:       summary,
			Keywords:      keywords,
		}
		result := database.Client.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"audio_file_name", "transcript", "summary", "keywords"}),
		}).Create(&video)
		if result.Error != nil {
			logger.WithFields(logrus.Fields{
				"Err":           result.Error,
				"ID":            raw.VideoId,
				"AudioFileName": audioFileName,
				"Transcript":    transcript,
				"Summary":       summary,
				"Keywords":      keywords,
			}).Errorf("Error when updating summary information to database")
			logging.SetSpanError(span, result.Error)
			errorHandler(channel, d, true, logger, &span) //note：插入数据库失败，处理错误情况，重新发布
			span.End()
			continue
		}

		// step：Cannot get summary or keywords from ChatGPT: resend to mq
		if summaryOrKeywordsErr { //q：检查结果吗？
			errorHandler(channel, d, true, logger, &span) //note:设置重新发布
			span.End()
			continue
		}

		span.End()
		err = d.Ack(false) //note：成功消费
		if err != nil {
			logger.WithFields(logrus.Fields{
				"err": err,
			}).Errorf("Error when dealing with the video...")
		}
	}
}

// video2Audio
// video 转 audio并上传
//
//	@Description: 调用 ffmpeg 进行 video 转 audio，结果上传到audioFileName文件中
//	@param ctx
//	@param videoFileName  原始video文件名
//	@return audioFileName  返回新文件名audioFileName
//	@return err
func video2Audio(ctx context.Context, videoFileName string) (audioFileName string, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "Video2Audio")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("VideoSummary.Video2Audio").WithContext(ctx)
	logger.WithFields(logrus.Fields{
		"video_file_name": videoFileName,
	}).Debugf("Transforming video to audio")

	videoFilePath := file.GetLocalPath(ctx, videoFileName)
	cmdArgs := []string{
		"-i", videoFilePath, "-q:a", "0", "-map", "a", "-f", "mp3", "-",
	}
	cmd := exec.Command("ffmpeg", cmdArgs...)
	var buf bytes.Buffer
	cmd.Stdout = &buf

	err = cmd.Run()
	if err != nil {
		logger.WithFields(logrus.Fields{
			"VideoFileName": videoFileName,
		}).Errorf("cmd %s failed with %s", "ffmpeg "+strings.Join(cmdArgs, " "), err)
		logging.SetSpanError(span, err)
		return
	}

	//生成声音文件名
	audioFileName = pathgen.GenerateAudioName(videoFileName)

	//把转换成的声音文件上传
	_, err = file.Upload(ctx, audioFileName, bytes.NewReader(buf.Bytes()))
	if err != nil {
		logger.WithFields(logrus.Fields{
			"VideoFileName": videoFileName,
			"AudioFileName": audioFileName,
		}).Errorf("Failed to upload audio file")
		logging.SetSpanError(span, err)
		return
	}
	return
}

// speech2Text
//
//	@Description: 音频转录，用openai的音频转录 API，将音频文件转录为文本
//	@param ctx
//	@param audioFileName  音频文件名
//	@return transcript  音频转录成的文本
//	@return err
func speech2Text(ctx context.Context, audioFileName string) (transcript string, err error) {
	ctx, span := tracing.Tracer.Start(ctx, "Speech2Text")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("VideoSummary.Speech2Text").WithContext(ctx)
	logger.WithFields(logrus.Fields{
		"AudioFileName": audioFileName,
	}).Debugf("Transforming audio to transcirpt")

	audioFilePath := file.GetLocalPath(ctx, audioFileName)

	req := openai.AudioRequest{
		Model:    openai.Whisper1,
		FilePath: audioFilePath,
	}
	//note： openai的音频转录 API，用于将音频文件转录为文本
	resp, err := openaiClient.CreateTranscription(ctx, req)
	if err != nil {
		logger.WithFields(logrus.Fields{
			"Err":           err,
			"AudioFileName": audioFileName,
		}).Errorf("Failed to get transcript from ChatGPT")
		logging.SetSpanError(span, err)
		return
	}

	//转换好的文本
	transcript = resp.Text
	logger.WithFields(logrus.Fields{
		"Transcript": transcript,
	}).Debugf("Successful to get transcript from ChatGPT")

	return
}

// text2Summary
//
//	@Description: 用gpt接口对文本进行总结，结果写入summaryChannel通道
//	@param ctx
//	@param transcript  要总结的文本
//	@param summaryChannel  总结之后的内容写入到的通道
//	@param errChannel  错误写入错误管道
func text2Summary(ctx context.Context, transcript string, summaryChannel *chan string, errChannel *chan error) {
	ctx, span := tracing.Tracer.Start(ctx, "Text2Summary")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("VideoSummary.Text2Summary").WithContext(ctx)
	logger.WithFields(logrus.Fields{
		"transcript": transcript,
	}).Debugf("Getting transcript summary form ChatGPT")

	req := openai.ChatCompletionRequest{
		Model: openai.GPT3Dot5Turbo,
		Messages: []openai.ChatCompletionMessage{
			{
				Role: openai.ChatMessageRoleSystem,
				Content: "You will be provided with a block of text which is the content of a video, " +
					"and your task is to give 2 Simplified Chinese sentences to summarize the video.",
			},
			{
				Role:    openai.ChatMessageRoleUser,
				Content: transcript,
			},
		},
	}
	resp, err := openaiClient.CreateChatCompletion(ctx, req)
	if err != nil {
		logger.WithFields(logrus.Fields{
			"Err":        err,
			"Transcript": transcript,
		}).Errorf("Failed to get summary from ChatGPT")
		logging.SetSpanError(span, err)
		*errChannel <- err
		return
	}

	summary := resp.Choices[0].Message.Content
	*summaryChannel <- summary

	logger.WithFields(logrus.Fields{
		"Summary": summary,
	}).Debugf("Successful to get summary from ChatGPT")
}

// text2Keywords
//
//	@Description: 生成关键词并将结果写入到管道中
//	@param ctx
//	@param transcript   要生成关键词的文本
//	@param keywordsChannel  结果输出管道
//	@param errChannel
func text2Keywords(ctx context.Context, transcript string, keywordsChannel *chan string, errChannel *chan error) {
	ctx, span := tracing.Tracer.Start(ctx, "Text2Keywords")
	defer span.End()
	logging.SetSpanWithHostname(span)
	logger := logging.LogService("VideoSummary.Text2Keywords").WithContext(ctx)
	logger.WithFields(logrus.Fields{
		"transcript": transcript,
	}).Debugf("Getting transcript keywords from ChatGPT")

	req := openai.ChatCompletionRequest{
		Model: openai.GPT3Dot5Turbo,
		Messages: []openai.ChatCompletionMessage{
			{
				Role: openai.ChatMessageRoleSystem,
				Content: "You will be provided with a block of text which is the content of a video, " +
					"and your task is to give 5 tags in Simplified Chinese to the video to attract audience. " +
					"For example, 美食 | 旅行 | 阅读",
			},
			{
				Role:    openai.ChatMessageRoleUser,
				Content: transcript,
			},
		},
	}
	resp, err := openaiClient.CreateChatCompletion(ctx, req)
	if err != nil {
		logger.WithFields(logrus.Fields{
			"Err":        err,
			"Transcript": transcript,
		}).Errorf("Failed to get keywords from ChatGPT")
		logging.SetSpanError(span, err)
		*errChannel <- err
		return
	}

	keywords := resp.Choices[0].Message.Content

	*keywordsChannel <- keywords

	logger.WithFields(logrus.Fields{
		"Keywords": keywords,
	}).Debugf("Successful to get keywords from ChatGPT")
}

// isTranscriptExist
// 在video表中观察找id为videoId的记录的Transcript字段
//
//	@Description:
//	@param videoId
//	@return res  是否查到
//	@return transcript  对应的video.Transcript值
//	@return err
func isTranscriptExist(videoId uint32) (res bool, transcript string, err error) {
	video := &models.Video{}
	//再数据库中查找videoId对应视频的transcript字段，填充到video对象中
	result := database.Client.Select("transcript").Where("id = ?", videoId).Find(video)
	err = result.Error
	if result.Error != nil {
		res = false
	} else {
		res = video.Transcript != ""
		transcript = video.Transcript
	}
	return
}

// isSummaryExist
//
//	@Description: 从数据库查找videoid是否存在summary字段
//	@param videoId
//	@return res  true为存在
//	@return summary  如果存在返回summary
//	@return err
func isSummaryExist(videoId uint32) (res bool, summary string, err error) {
	video := &models.Video{}
	result := database.Client.Select("summary").Where("id = ?", videoId).Find(video)
	err = result.Error
	if result.Error != nil {
		res = false
	} else {
		res = video.Summary != ""
		summary = video.Summary
	}
	return
}

// isKeywordsExist
//
//	@Description: 在video数据表中查找videoId的keywords字段是否存在
//	@param videoId
//	@return res  是否存在
//	@return keywords  值
//	@return err
func isKeywordsExist(videoId uint32) (res bool, keywords string, err error) {
	video := &models.Video{}
	result := database.Client.Select("keywords").Where("id = ?", videoId).Find(video)
	err = result.Error
	if result.Error != nil {
		res = false
	} else {
		res = video.Keywords != ""
		keywords = video.Keywords
	}
	return
}

func isMagicUserExist(ctx context.Context, logger *logrus.Entry, span *trace.Span) bool {
	isMagicUserExistRes, err := userClient.GetUserExistInformation(ctx, &user.UserExistRequest{
		UserId: config.EnvCfg.MagicUserId,
	})
	if err != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Failed to check if the magic user exists")
		logging.SetSpanError(*span, err)
		return false
	}

	if !isMagicUserExistRes.Existed {
		logger.Errorf("Magic user does not exist")
		logging.SetSpanError(*span, errors.New("magic user does not exist"))
	}

	return isMagicUserExistRes.Existed
}

// addMagicComment
//
//	@Description: 让MagicUser给videoId所属的视频添加评论
//	@param videoId  要添加评论的videoid
//	@param content  评论内容
//	@param ctx
//	@param logger
//	@param span
func addMagicComment(videoId uint32, content string, ctx context.Context, logger *logrus.Entry, span *trace.Span) {
	_, err := commentClient.ActionComment(ctx, &comment.ActionCommentRequest{
		ActorId:    config.EnvCfg.MagicUserId,
		VideoId:    videoId,
		ActionType: comment.ActionCommentType_ACTION_COMMENT_TYPE_ADD,
		Action:     &comment.ActionCommentRequest_CommentText{CommentText: content},
	})

	if err != nil {
		logger.WithFields(logrus.Fields{
			"err": err,
		}).Errorf("Failed to add magic comment")
		logging.SetSpanError(*span, err)
	}
}
