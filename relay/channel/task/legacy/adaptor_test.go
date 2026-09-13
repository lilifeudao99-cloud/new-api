package legacy

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

func TestEstimateBillingUsesVideoDuration(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("task_request", relaycommon.TaskSubmitReq{Seconds: "7"})

	ratios := (&TaskAdaptor{}).EstimateBilling(c, &relaycommon.RelayInfo{
		OriginModelName: "MiniMax-H3",
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "MiniMax-H3"},
	})
	if ratios["seconds"] != 7 {
		t.Fatalf("expected seven-second billing ratio, got %#v", ratios)
	}
}

func TestEstimateBillingPrefersSecondsAndDefaultsToFour(t *testing.T) {
	tests := []struct {
		name    string
		request relaycommon.TaskSubmitReq
		want    float64
	}{
		{name: "duration", request: relaycommon.TaskSubmitReq{Duration: 5}, want: 5},
		{name: "seconds takes precedence", request: relaycommon.TaskSubmitReq{Seconds: "6", Duration: 5}, want: 6},
		{name: "default", request: relaycommon.TaskSubmitReq{}, want: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set("task_request", test.request)
			ratios := (&TaskAdaptor{}).EstimateBilling(c, &relaycommon.RelayInfo{
				OriginModelName: "MiniMax-H3",
				ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "MiniMax-H3"},
			})
			if ratios["seconds"] != test.want {
				t.Fatalf("expected seconds ratio %v, got %#v", test.want, ratios)
			}
		})
	}
}

func TestEstimateBillingLeavesFixedDurationH3ModelsPerRequest(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("task_request", relaycommon.TaskSubmitReq{Duration: 10})
	if ratios := (&TaskAdaptor{}).EstimateBilling(c, &relaycommon.RelayInfo{
		OriginModelName: "Minimax-H3-768p-933-10s",
		ChannelMeta:     &relaycommon.ChannelMeta{UpstreamModelName: "Minimax-H3-768p-933-10s"},
	}); ratios != nil {
		t.Fatalf("expected no duration multiplier for fixed-price model, got %#v", ratios)
	}
}

func TestEstimateBillingUsesDurationForOtherLegacyVideoModels(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("task_request", relaycommon.TaskSubmitReq{Duration: 8})
	ratio := (&TaskAdaptor{}).EstimateBilling(c, &relaycommon.RelayInfo{
		OriginModelName: "grok-video-3",
	})
	if ratio["seconds"] != 8 {
		t.Fatalf("expected eight-second billing ratio, got %#v", ratio)
	}
}

func TestNormalizeH3CreateBody(t *testing.T) {
	body, err := normalizeH3CreateBody([]byte(`{"model":"MiniMax-H3","prompt":"scene","seconds":"4s","aspect_ratio":"16:9"}`))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := common.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["duration"] != float64(4) || got["resolution"] != "768P" || got["ratio"] != "16:9" {
		t.Fatalf("unexpected normalized request: %#v", got)
	}
	content, ok := got["content"].([]any)
	if !ok || len(content) != 1 || content[0].(map[string]any)["text"] != "scene" {
		t.Fatalf("unexpected content: %#v", got["content"])
	}
}

func TestNormalizeH3CreateBodyIgnoresOtherModels(t *testing.T) {
	body := []byte(`{"model":"other-video","prompt":"scene"}`)
	got, err := normalizeH3CreateBody(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("body changed for non-H3 model: %s", got)
	}
}

func TestParseH3TaskResult(t *testing.T) {
	result, handled, err := parseH3TaskResult(&model.Task{Properties: model.Properties{UpstreamModelName: "MiniMax-H3"}}, []byte(`{"task":{"status":"succeeded","content":{"url":"https://example.com/video.mp4"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected H3 task response to be handled")
	}
	if result.Status != model.TaskStatusSuccess || result.Progress != "100%" || result.Url != "https://example.com/video.mp4" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestParseH3TaskResultIgnoresFlatResponse(t *testing.T) {
	result, handled, err := parseH3TaskResult(&model.Task{Properties: model.Properties{UpstreamModelName: "other-video"}}, []byte(`{"id":"task","status":"completed"}`))
	if err != nil {
		t.Fatal(err)
	}
	if handled || result != nil {
		t.Fatalf("flat response should use legacy parser, result=%+v handled=%v", result, handled)
	}
}

func TestNormalizeH3TaskResponse(t *testing.T) {
	task := &model.Task{TaskID: "public-task", Properties: model.Properties{UpstreamModelName: "MiniMax-H3"}}
	body, changed, err := normalizeH3TaskResponse(task, []byte(`{"task":{"model":"MiniMax-H3","status":"succeeded","created_at":10,"updated_at":20,"content":{"url":"https://example.com/video.mp4"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected H3 response to be normalized")
	}
	var got map[string]any
	if err := common.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != "public-task" || got["status"] != "completed" || got["progress"] != float64(100) || got["url"] != "https://example.com/video.mp4" {
		t.Fatalf("unexpected normalized response: %#v", got)
	}
}
