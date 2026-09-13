package legacy

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// TaskAdaptor preserves the pre-plugin OpenAI-compatible video contract used
// by Grok-compatible channels. It deliberately has no plugin dependency.
type TaskAdaptor struct {
	taskcommon.BaseBilling
	apiKey  string
	baseURL string
}

type responseTask struct {
	ID       string `json:"id"`
	TaskID   string `json:"task_id,omitempty"`
	Status   string `json:"status"`
	Progress int    `json:"progress"`
	URL      string `json:"url,omitempty"`
	VideoURL string `json:"video_url,omitempty"`
	Error    *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

func parseH3Duration(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case float32:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		v = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(v), "s"))
		duration, _ := strconv.Atoi(v)
		return duration
	default:
		return 0
	}
}

// normalizeH3CreateBody applies the request shape expected by MiniMax-H3.
// The legacy adaptor sends directly to the upstream, so this logic replaces
// the former standalone compatibility proxy.
func normalizeH3CreateBody(body []byte) ([]byte, error) {
	var request map[string]any
	if err := common.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	if request["model"] != "MiniMax-H3" {
		return body, nil
	}

	duration := parseH3Duration(request["duration"])
	if duration <= 0 {
		duration = parseH3Duration(request["seconds"])
	}
	if duration <= 0 {
		duration = 4
	}
	if parseH3Duration(request["duration"]) != duration {
		request["duration"] = duration
	}
	if _, ok := request["resolution"].(string); !ok {
		request["resolution"] = "768P"
	}
	if _, ok := request["ratio"].(string); !ok {
		ratio, _ := request["aspect_ratio"].(string)
		if ratio == "" {
			ratio = "16:9"
		}
		request["ratio"] = ratio
	}
	if _, exists := request["content"]; !exists {
		prompt, _ := request["prompt"].(string)
		request["content"] = []map[string]string{{"type": "text", "text": prompt}}
	}
	return common.Marshal(request)
}

func parseH3TaskResult(task *model.Task, body []byte) (*relaycommon.TaskInfo, bool, error) {
	if task == nil || (task.Properties.UpstreamModelName != "MiniMax-H3" && task.Properties.OriginModelName != "MiniMax-H3") {
		return nil, false, nil
	}
	var response map[string]any
	if err := common.Unmarshal(body, &response); err != nil {
		return nil, false, err
	}
	rawTask, ok := response["task"].(map[string]any)
	if !ok {
		return nil, false, nil
	}

	status, _ := rawTask["status"].(string)
	switch status {
	case "succeeded":
		status = "completed"
	case "running":
		status = "in_progress"
	}
	result := &relaycommon.TaskInfo{}
	switch status {
	case "queued", "pending":
		result.Status = model.TaskStatusQueued
	case "processing", "in_progress":
		result.Status = model.TaskStatusInProgress
	case "completed", "success":
		result.Status = model.TaskStatusSuccess
	case "failed", "cancelled", "canceled":
		result.Status = model.TaskStatusFailure
	}
	if result.Status == "" {
		return result, true, nil
	}
	if result.Status == model.TaskStatusQueued {
		result.Progress = ""
	} else if result.Status == model.TaskStatusInProgress {
		result.Progress = "30%"
	} else {
		result.Progress = "100%"
	}
	if result.Status == model.TaskStatusFailure {
		if taskError, ok := rawTask["error"].(map[string]any); ok {
			result.Reason, _ = taskError["message"].(string)
		}
	}
	if videoURL, ok := response["video_url"].(string); ok {
		result.Url = videoURL
	}
	if content, ok := rawTask["content"].(map[string]any); ok {
		if videoURL, ok := content["url"].(string); ok && videoURL != "" {
			result.Url = videoURL
		}
	}
	return result, true, nil
}

func normalizeH3TaskResponse(task *model.Task, body []byte) ([]byte, bool, error) {
	if task == nil || (task.Properties.UpstreamModelName != "MiniMax-H3" && task.Properties.OriginModelName != "MiniMax-H3") {
		return body, false, nil
	}
	var response map[string]any
	if err := common.Unmarshal(body, &response); err != nil {
		return nil, false, err
	}
	rawTask, ok := response["task"].(map[string]any)
	if !ok {
		return body, false, nil
	}
	status, _ := rawTask["status"].(string)
	switch status {
	case "succeeded":
		status = "completed"
	case "running":
		status = "in_progress"
	}
	progress := 0
	if status == "completed" || status == "failed" || status == "cancelled" || status == "canceled" {
		progress = 100
	} else if status == "in_progress" || status == "processing" {
		progress = 30
	}
	normalized := map[string]any{
		"id":           task.TaskID,
		"task_id":      task.TaskID,
		"object":       "video",
		"model":        rawTask["model"],
		"status":       status,
		"progress":     progress,
		"created_at":   rawTask["created_at"],
		"completed_at": rawTask["updated_at"],
	}
	if videoURL, ok := response["video_url"].(string); ok && videoURL != "" {
		normalized["url"] = videoURL
		normalized["video_url"] = videoURL
	}
	if content, ok := rawTask["content"].(map[string]any); ok {
		if videoURL, ok := content["url"].(string); ok && videoURL != "" {
			normalized["url"] = videoURL
			normalized["video_url"] = videoURL
		}
	}
	if taskError, ok := rawTask["error"]; ok {
		normalized["error"] = taskError
	}
	result, err := common.Marshal(normalized)
	return result, true, err
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	a.apiKey = info.ApiKey
	a.baseURL = strings.TrimRight(info.ChannelBaseUrl, "/")
}

func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	return relaycommon.ValidateMultipartDirect(c, info)
}

// EstimateBilling applies per-second billing to legacy video models. The two
// fixed-duration MiniMax aliases retain their per-request pricing.
func (a *TaskAdaptor) EstimateBilling(c *gin.Context, info *relaycommon.RelayInfo) map[string]float64 {
	if info == nil {
		return nil
	}
	modelName := info.GetUpstreamModelName()
	if modelName == "" {
		modelName = info.OriginModelName
	}
	if modelName == "Minimax-H3-768p-933-10s" || modelName == "Minimax-H3-768p-933-15s" {
		return nil
	}
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return nil
	}

	seconds := parseH3Duration(req.Seconds)
	if seconds <= 0 {
		seconds = req.Duration
	}
	if seconds <= 0 {
		seconds = 4
	}
	return map[string]float64{"seconds": float64(seconds)}
}

func (a *TaskAdaptor) BuildRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if strings.TrimSpace(a.baseURL) == "" {
		return "", fmt.Errorf("channel base URL is empty")
	}
	return a.baseURL + "/v1/videos", nil
}

func (a *TaskAdaptor) BuildRequestHeader(c *gin.Context, req *http.Request, _ *relaycommon.RelayInfo) error {
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	if contentType := c.GetHeader("Content-Type"); contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, err
	}
	raw, err := storage.Bytes()
	if err != nil {
		return nil, err
	}
	contentType := c.GetHeader("Content-Type")
	if strings.HasPrefix(strings.ToLower(contentType), "application/json") {
		var body map[string]any
		if err := common.Unmarshal(raw, &body); err != nil {
			return bytes.NewReader(raw), nil
		}
		body["model"] = info.UpstreamModelName
		encoded, err := common.Marshal(body)
		if err != nil {
			return nil, err
		}
		if info.UpstreamModelName == "MiniMax-H3" {
			encoded, err = normalizeH3CreateBody(encoded)
			if err != nil {
				return nil, err
			}
		}
		return bytes.NewReader(encoded), nil
	}
	if strings.Contains(strings.ToLower(contentType), "multipart/form-data") {
		form, err := common.ParseMultipartFormReusable(c)
		if err != nil {
			return bytes.NewReader(raw), nil
		}
		defer form.RemoveAll()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		if err := writer.WriteField("model", info.UpstreamModelName); err != nil {
			return nil, err
		}
		for key, values := range form.Value {
			if key == "model" {
				continue
			}
			for _, value := range values {
				if err := writer.WriteField(key, value); err != nil {
					return nil, err
				}
			}
		}
		for field, headers := range form.File {
			for _, header := range headers {
				file, err := header.Open()
				if err != nil {
					return nil, err
				}
				partHeader := make(textproto.MIMEHeader)
				partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, header.Filename))
				if contentType := header.Header.Get("Content-Type"); contentType != "" {
					partHeader.Set("Content-Type", contentType)
				}
				part, err := writer.CreatePart(partHeader)
				if err == nil {
					_, err = io.Copy(part, file)
				}
				file.Close()
				if err != nil {
					return nil, err
				}
			}
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		c.Request.Header.Set("Content-Type", writer.FormDataContentType())
		return &body, nil
	}
	return bytes.NewReader(raw), nil
}

func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, body io.Reader) (*http.Response, error) {
	return channel.DoTaskApiRequest(a, c, info, body)
}

func (a *TaskAdaptor) ParseResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*channel.TaskSubmitResponse, *dto.TaskError) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusBadGateway)
	}
	var parsed responseTask
	if err := common.Unmarshal(body, &parsed); err != nil {
		return nil, service.TaskErrorWrapper(err, "unmarshal_response_body_failed", http.StatusBadGateway)
	}
	upstreamID := parsed.ID
	if upstreamID == "" {
		upstreamID = parsed.TaskID
	}
	if upstreamID == "" {
		return nil, service.TaskErrorWrapperLocal(fmt.Errorf("task_id is empty"), "invalid_response", http.StatusBadGateway)
	}
	var clientResponse map[string]any
	if err := common.Unmarshal(body, &clientResponse); err != nil {
		clientResponse = map[string]any{}
	}
	clientResponse["id"] = info.PublicTaskID
	clientResponse["task_id"] = info.PublicTaskID
	return &channel.TaskSubmitResponse{UpstreamTaskID: upstreamID, TaskData: body, ClientResponse: clientResponse}, nil
}

func (a *TaskAdaptor) FetchTask(_ string, key string, task *model.Task, proxy string) (*http.Response, error) {
	if task == nil {
		return nil, fmt.Errorf("task is nil")
	}
	taskID := task.GetUpstreamTaskID()
	if taskID == "" {
		taskID = task.TaskID
	}
	req, err := http.NewRequest(http.MethodGet, a.baseURL+"/v1/videos/"+taskID, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func (a *TaskAdaptor) ParseTaskResult(task *model.Task, _ *http.Response, body []byte) (*relaycommon.TaskInfo, error) {
	if result, handled, err := parseH3TaskResult(task, body); handled {
		return result, err
	}
	var parsed responseTask
	if err := common.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	result := &relaycommon.TaskInfo{}
	switch parsed.Status {
	case "queued", "pending":
		result.Status = model.TaskStatusQueued
	case "processing", "in_progress":
		result.Status = model.TaskStatusInProgress
	case "completed", "succeeded", "success":
		result.Status = model.TaskStatusSuccess
	case "failed", "cancelled", "canceled":
		result.Status = model.TaskStatusFailure
		if parsed.Error != nil {
			result.Reason = parsed.Error.Message
		}
	}
	if parsed.Progress > 0 && parsed.Progress < 100 {
		result.Progress = strconv.Itoa(parsed.Progress) + "%"
	}
	if parsed.URL != "" {
		result.Url = parsed.URL
	} else if parsed.VideoURL != "" {
		result.Url = parsed.VideoURL
	}
	return result, nil
}

func (a *TaskAdaptor) GetModelList() []string { return []string{common.GrokVideoModel} }
func (a *TaskAdaptor) GetChannelName() string { return "grok-legacy" }

func (a *TaskAdaptor) ConvertToOpenAIVideo(task *model.Task) ([]byte, error) {
	if task == nil {
		return nil, fmt.Errorf("task is nil")
	}
	if len(task.Data) > 0 {
		if normalized, changed, err := normalizeH3TaskResponse(task, task.Data); changed {
			if err != nil {
				return nil, err
			}
			return normalized, nil
		}
		var response map[string]any
		if err := common.Unmarshal(task.Data, &response); err == nil && response != nil {
			response["id"] = task.TaskID
			response["task_id"] = task.TaskID
			return common.Marshal(response)
		}
	}
	return common.Marshal(task.ToOpenAIVideo())
}
