package strings

// Exchange name
const (
	VideoExchange   = "video_exchange"
	EventExchange   = "event"
	MessageExchange = "message_exchange"
	AuditExchange   = "audit_exchange"
)

// Queue name
const (
	VideoPicker   = "video_picker"  //视频信息采集(封面/水印)协程
	VideoSummary  = "video_summary" //videoprocessor服务中创建视频的keyword和summary，并让magic发表评论，keywords还被发布到"event" - video.publish.action中用作后续的推荐算法
	MessageCommon = "message_common"
	MessageGPT    = "message_gpt"
	MessageES     = "message_es"
	AuditPicker   = "audit_picker" //
)

// Routing key
const (
	FavoriteActionEvent = "video.favorite.action" //produceFavorite
	VideoGetEvent       = "video.get.action"      //produceFeed
	VideoCommentEvent   = "video.comment.action"  //produceComment
	VideoPublishEvent   = "video.publish.action"  //produceKeywords

	MessageActionEvent    = "message.common" //发给用户的key
	MessageGptActionEvent = "message.gpt"    //发给magic user的key
	AuditPublishEvent     = "audit"
)

// Action Type
const (
	FavoriteIdActionLog = 1 // 用户点赞相关操作
	FollowIdActionLog   = 2 // 用户关注相关操作
)

// Action Name
const (
	FavoriteNameActionLog    = "favorite.action" // 用户点赞操作名称
	FavoriteUpActionSubLog   = "up"
	FavoriteDownActionSubLog = "down"

	FollowNameActionLog    = "follow.action" // 用户关注操作名称
	FollowUpActionSubLog   = "up"
	FollowDownActionSubLog = "down"
)

// Action Service Name
const (
	FavoriteServiceName = "FavoriteService"
	FollowServiceName   = "FollowService"
)
