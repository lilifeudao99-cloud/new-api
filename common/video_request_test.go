package common

import "testing"

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
		{model: "grok-imagine-video", want: false},
		{model: "MiniMax-H3", want: false},
	} {
		t.Run(testCase.model, func(t *testing.T) {
			if got := IsTaskPluginVideoModel(testCase.model); got != testCase.want {
				t.Fatalf("IsTaskPluginVideoModel(%q) = %t, want %t", testCase.model, got, testCase.want)
			}
		})
	}
}
