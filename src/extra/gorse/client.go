package gorse

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type GorseClient struct {
	entryPoint string
	apiKey     string
	httpClient http.Client
}

func NewGorseClient(entryPoint, apiKey string) *GorseClient {
	return &GorseClient{
		entryPoint: entryPoint, //服务器ip？
		apiKey:     apiKey,     // 服务请求密钥？"X-API-Key", c.apiKey
		/*在 Go 语言中，http.Client 结构体用于发起 HTTP 请求，而 Transport 字段是其核心组件之一。Transport 定义了客户端如何执行 HTTP 请求和处理响应。*/
		httpClient: http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)},
	}
}

func (c *GorseClient) InsertFeedback(ctx context.Context, feedbacks []Feedback) (RowAffected, error) {
	return request[RowAffected](ctx, c, "POST", c.entryPoint+"/api/feedback", feedbacks)
}

func (c *GorseClient) PutFeedback(ctx context.Context, feedbacks []Feedback) (RowAffected, error) {
	return request[RowAffected](ctx, c, "PUT", c.entryPoint+"/api/feedback", feedbacks)
}

func (c *GorseClient) GetFeedback(ctx context.Context, cursor string, n int) (Feedbacks, error) {
	return request[Feedbacks, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/feedback?cursor=%s&n=%d", cursor, n), nil)
}

func (c *GorseClient) GetFeedbacksWithType(ctx context.Context, feedbackType, cursor string, n int) (Feedbacks, error) {
	return request[Feedbacks, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/feedback/%s?cursor=%s&n=%d", feedbackType, cursor, n), nil)
}

func (c *GorseClient) GetFeedbackWithUserItem(ctx context.Context, userId, itemId string) ([]Feedback, error) {
	return request[[]Feedback, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/feedback/%s/%s", userId, itemId), nil)
}

func (c *GorseClient) GetFeedbackWithTypeUserItem(ctx context.Context, feedbackType, userId, itemId string) (Feedback, error) {
	return request[Feedback, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/feedback/%s/%s/%s", feedbackType, userId, itemId), nil)
}

func (c *GorseClient) DelFeedback(ctx context.Context, feedbackType, userId, itemId string) (Feedback, error) {
	return request[Feedback, any](ctx, c, "DELETE", c.entryPoint+fmt.Sprintf("/api/feedback/%s/%s/%s", feedbackType, userId, itemId), nil)
}

func (c *GorseClient) DelFeedbackWithUserItem(ctx context.Context, userId, itemId string) ([]Feedback, error) {
	return request[[]Feedback, any](ctx, c, "DELETE", c.entryPoint+fmt.Sprintf("/api/feedback/%s/%s", userId, itemId), nil)
}

func (c *GorseClient) GetItemFeedbacks(ctx context.Context, itemId string) ([]Feedback, error) {
	return request[[]Feedback, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/item/%s/feedback", itemId), nil)
}

func (c *GorseClient) GetItemFeedbacksWithType(ctx context.Context, itemId, feedbackType string) ([]Feedback, error) {
	return request[[]Feedback, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/item/%s/feedback/%s", itemId, feedbackType), nil)
}

func (c *GorseClient) GetUserFeedbacks(ctx context.Context, userId string) ([]Feedback, error) {
	return request[[]Feedback, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/user/%s/feedback", userId), nil)
}

func (c *GorseClient) GetUserFeedbacksWithType(ctx context.Context, userId, feedbackType string) ([]Feedback, error) {
	return request[[]Feedback, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/user/%s/feedback/%s", userId, feedbackType), nil)
}

// Deprecated: GetUserFeedbacksWithType instead
func (c *GorseClient) ListFeedbacks(ctx context.Context, feedbackType, userId string) ([]Feedback, error) {
	return request[[]Feedback, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/user/%s/feedback/%s", userId, feedbackType), nil)
}

func (c *GorseClient) GetItemLatest(ctx context.Context, userid string, n, offset int) ([]Score, error) {
	return request[[]Score, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/latest?user-id=%s&n=%d&offset=%d", userid, n, offset), nil)
}

func (c *GorseClient) GetItemLatestWithCategory(ctx context.Context, userid, category string, n, offset int) ([]Score, error) {
	return request[[]Score, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/latest?user-id=%s&category=%s&n=%d&offset=%d", userid, category, n, offset), nil)
}

func (c *GorseClient) GetItemPopular(ctx context.Context, userid string, n, offset int) ([]Score, error) {
	return request[[]Score, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/popular?user-id=%s&n=%d&offset=%d", userid, n, offset), nil)
}

func (c *GorseClient) GetItemPopularWithCategory(ctx context.Context, userid, category string, n, offset int) ([]Score, error) {
	return request[[]Score, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/popular/%s?user-id=%s&n=%d&offset=%d", category, userid, n, offset), nil)
}

// GetItemRecommend
//
//	使用GorseClient访问Gorse获取推荐视频id
//	@Description:通过请求接口url：c.entryPoint/api/recommend/%s?write-back-type=%s&write-back-delay=%s&n=%d&offset=%d%s
//	返回[]string类型的结果
//	@receiver c   访问接口的客户端封装，包含接口入口等信息
//	@param ctx
//	@param userId  请求url中的参数,用户id
//	@param categories  请求body中的&category参数列表
//	@param writeBackType  请求url中的参数
//	@param writeBackDelay 请求url中的参数
//	@param n  请求url中的参数
//	@param offset  请求url中的参数
//	@return []string  gorseClient访问接口的返回值
//	@return error  请求的err记录
func (c *GorseClient) GetItemRecommend(ctx context.Context, userId string, categories []string, writeBackType, writeBackDelay string, n, offset int) ([]string, error) {
	var queryCategories string //请求body
	if len(categories) > 0 {
		queryCategories = "&category=" + strings.Join(categories, "&category=") //用"&category="拼接起来
	}
	return request[[]string, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/recommend/%s?write-back-type=%s&write-back-delay=%s&n=%d&offset=%d%s", userId, writeBackType, writeBackDelay, n, offset, queryCategories), nil)
}

func (c *GorseClient) GetItemRecommendWithCategory(ctx context.Context, userId, category, writeBackType, writeBackDelay string, n, offset int) ([]string, error) {
	return request[[]string, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/recommend/%s/%s?write-back-type=%s&write-back-delay=%s&n=%d&offset=%d", userId, category, writeBackType, writeBackDelay, n, offset), nil)
}

// Deprecated: GetItemRecommendWithCategory instead
func (c *GorseClient) GetRecommend(ctx context.Context, userId, category string, n int) ([]string, error) {
	return request[[]string, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/recommend/%s/%s?n=%d", userId, category, n), nil)
}

func (c *GorseClient) SessionItemRecommend(ctx context.Context, feedbacks []Feedback, n, offset int) ([]Score, error) {
	return request[[]Score](ctx, c, "POST", c.entryPoint+fmt.Sprintf("/api/session/recommend?n=%d&offset=%d", n, offset), feedbacks)
}

func (c *GorseClient) SessionItemRecommendWithCategory(ctx context.Context, feedbacks []Feedback, category string, n, offset int) ([]Score, error) {
	return request[[]Score](ctx, c, "POST", c.entryPoint+fmt.Sprintf("/api/session/recommend/%s?n=%d&offset=%d", category, n, offset), feedbacks)
}

// Deprecated: SessionItemRecommend instead
func (c *GorseClient) SessionRecommend(ctx context.Context, feedbacks []Feedback, n int) ([]Score, error) {
	return request[[]Score](ctx, c, "POST", c.entryPoint+fmt.Sprintf("/api/session/recommend?n=%d", n), feedbacks)
}

func (c *GorseClient) GetUserNeighbors(ctx context.Context, userId string, n, offset int) ([]Score, error) {
	return request[[]Score, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/user/%s/neighbors?n=%d&offset=%d", userId, n, offset), nil)
}

func (c *GorseClient) GetItemNeighbors(ctx context.Context, itemId string, n, offset int) ([]Score, error) {
	return request[[]Score, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/item/%s/neighbors?n=%d&offset=%d", itemId, n, offset), nil)
}

func (c *GorseClient) GetItemNeighborsWithCategory(ctx context.Context, itemId, category string, n, offset int) ([]Score, error) {
	return request[[]Score, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/item/%s/neighbors/%s?n=%d&offset=%d", itemId, category, n, offset), nil)
}

// Deprecated: GetItemNeighbors instead
func (c *GorseClient) GetNeighbors(ctx context.Context, itemId string, n int) ([]Score, error) {
	return request[[]Score, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/item/%s/neighbors?n=%d", itemId, n), nil)
}

func (c *GorseClient) InsertUser(ctx context.Context, user User) (RowAffected, error) {
	return request[RowAffected](ctx, c, "POST", c.entryPoint+"/api/user", user)
}

// 用c中的 httpClient发送POST请求，url = c.entryPoint+"/api/users"，body = users
func (c *GorseClient) InsertUsers(ctx context.Context, users []User) (RowAffected, error) {
	return request[RowAffected, any](ctx, c, "POST", c.entryPoint+"/api/users", users)
}

func (c *GorseClient) UpdateUser(ctx context.Context, userId string, user UserPatch) (RowAffected, error) {
	return request[RowAffected](ctx, c, "PATCH", c.entryPoint+fmt.Sprintf("/api/user/%s", userId), user)
}

func (c *GorseClient) GetUser(ctx context.Context, userId string) (User, error) {
	return request[User, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/user/%s", userId), nil)
}

func (c *GorseClient) GetUsers(ctx context.Context, cursor string, n int) (Users, error) {
	return request[Users, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/users?cursor=%s&n=%d", cursor, n), nil)
}

func (c *GorseClient) DeleteUser(ctx context.Context, userId string) (RowAffected, error) {
	return request[RowAffected, any](ctx, c, "DELETE", c.entryPoint+fmt.Sprintf("/api/user/%s", userId), nil)
}

func (c *GorseClient) InsertItem(ctx context.Context, item Item) (RowAffected, error) {
	return request[RowAffected](ctx, c, "POST", c.entryPoint+"/api/item", item)
}

func (c *GorseClient) InsertItems(ctx context.Context, items []Item) (RowAffected, error) {
	return request[RowAffected](ctx, c, "POST", c.entryPoint+"/api/items", items)
}

func (c *GorseClient) UpdateItem(ctx context.Context, itemId string, item ItemPatch) (RowAffected, error) {
	return request[RowAffected](ctx, c, "PATCH", c.entryPoint+fmt.Sprintf("/api/item/%s", itemId), item)
}

func (c *GorseClient) GetItem(ctx context.Context, itemId string) (Item, error) {
	return request[Item, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/item/%s", itemId), nil)
}

func (c *GorseClient) GetItems(ctx context.Context, cursor string, n int) (Items, error) {
	return request[Items, any](ctx, c, "GET", c.entryPoint+fmt.Sprintf("/api/items?cursor=%s&n=%d", cursor, n), nil)
}

func (c *GorseClient) DeleteItem(ctx context.Context, itemId string) (RowAffected, error) {
	return request[RowAffected, any](ctx, c, "DELETE", c.entryPoint+fmt.Sprintf("/api/item/%s", itemId), nil)
}

func (c *GorseClient) PutItemCategory(ctx context.Context, itemId string, category string) (RowAffected, error) {
	return request[RowAffected, any](ctx, c, "PUT", c.entryPoint+fmt.Sprintf("/api/item/%s/category/%s", itemId, category), nil)
}

func (c *GorseClient) DelItemCategory(ctx context.Context, itemId string, category string) (RowAffected, error) {
	return request[RowAffected, any](ctx, c, "DELETE", c.entryPoint+fmt.Sprintf("/api/item/%s/category/%s", itemId, category), nil)
}

func (c *GorseClient) HealthLive(ctx context.Context) (Health, error) {
	return request[Health, any](ctx, c, "GET", c.entryPoint+"/api/health/live", nil)
}
func (c *GorseClient) HealthReady(ctx context.Context) (Health, error) {
	return request[Health, any](ctx, c, "GET", c.entryPoint+"/api/health/ready", nil)
}

// request[Response any, Body any]
// go语言泛型编程，这个函数是一个泛型函数，允许调用者指定请求和响应的数据类型。它封装了HTTP请求的创建、发送、接收和处理过程，使得在程序的其他部分可以方便地执行HTTP调用。
//
//	@Description:使用c.httpClient发送参数指定的请求，并return执行结果
//	@param ctx
//	@param c  从中提取一些信息
//	@param method  请求方法
//	@param url  请求url
//	@param body  请求body
//	@return result  Response类型的返回值
//	@return err
func request[Response any, Body any](ctx context.Context, c *GorseClient, method, url string, body Body) (result Response, err error) {
	bodyByte, marshalErr := json.Marshal(body) //json.Marshal 函数用于将一个数据结构（如结构体、切片、映射等）编码为 JSON 格式的字节数组。
	if marshalErr != nil {
		return result, marshalErr
	}
	var req *http.Request
	req, err = http.NewRequestWithContext(ctx, method, url, strings.NewReader(string(bodyByte)))
	if err != nil {
		return result, err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	//执行一个 HTTP 请求。它接受一个请求对象 req ，然后发起请求并返回一个响应对象 resp 以及可能出现的错误 err
	//通过检查 err 是否为 nil 可以确定请求执行是否成功，而 resp 则包含了请求得到的具体响应信息，如响应状态码、响应头、响应体等内容。
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	buf := new(strings.Builder)
	_, err = io.Copy(buf, resp.Body)
	if err != nil {
		return result, err
	}
	//请求没成功也要把resp.Body放到return里吗？
	if resp.StatusCode != http.StatusOK {
		return result, ErrorMessage(buf.String())
	}
	//将JSON数据（resp.Body）解码（即反序列化）到Go的值
	err = json.Unmarshal([]byte(buf.String()), &result)
	if err != nil {
		return result, err
	}
	return result, err
}
