package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsTaskPluginVideoModel(t *testing.T) {
	for _, testCase := range []struct {
		model string
		want  bool
	}{
		{model: "sora-2", want: true},
		{model: "sora-2-pro", want: true},
		{model: "wan3.0-video", want: true},
		{model: "wan3.0-video-prime", want: true},
		{model: "seedance-2.0", want: true},
		{model: "seedance-2.0-fast", want: true},
		{model: "seedance-2.0-mini", want: true},
		{model: "seedance-2.5", want: true},
		{model: "muse-video", want: true},
		{model: "muse-video-20s", want: true},
		{model: "muse-video-30s", want: true},
		{model: "muse-video-40s", want: true},
		{model: "muse-video-50s", want: true},
		{model: "muse-video-60s", want: true},
		{model: "muse-video-unknown", want: false},
		{model: "grok-imagine-video", want: false},
		{model: "MiniMax-H3", want: false},
	} {
		t.Run(testCase.model, func(t *testing.T) {
			assert.Equal(t, testCase.want, IsTaskPluginVideoModel(testCase.model))
		})
	}
}
