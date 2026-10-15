package jsplugin

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func (a *TaskAdaptor) prepareSeedanceReferenceUsage(c *gin.Context, info *relaycommon.RelayInfo, usageContext map[string]any) error {
	if a.plugin.Meta.Key != "doubao" || !slices.Contains([]string{"seedance-2.0", "seedance-2.0-fast", "seedance-2.0-mini", "seedance-2.5"}, info.OriginModelName) {
		return nil
	}
	descriptor, err := a.buildSubmit(c, info)
	if err != nil {
		return err
	}
	if descriptor.BodyType == "multipart" && usageContext["inputVideoSeconds"] != nil {
		return errors.New("verified Seedance reference video billing requires public MP4/MOV/M4V URLs instead of multipart video uploads")
	}
	// Inspect the final upstream body, so only references actually forwarded by
	// this plugin contribute to the charge. Client duration hints are never used.
	body, _ := descriptor.Body.(map[string]any)
	urls, err := seedanceReferenceVideoURLs(body)
	if err != nil {
		return err
	}
	if len(urls) == 0 {
		return nil
	}
	if len(urls) > 3 {
		return errors.New("Seedance supports at most 3 reference videos")
	}
	probe := a.probeReferenceVideo
	if probe == nil {
		probe = service.ProbeReferenceVideoSeconds
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	var total float64
	for i, videoURL := range urls {
		duration, cached := a.referenceVideoDurations[videoURL]
		if !cached {
			duration, err = probe(ctx, videoURL)
			if err != nil {
				return fmt.Errorf("reference video %d duration verification failed; use a reachable public MP4/MOV/M4V with valid duration metadata", i+1)
			}
		}
		if math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 || duration > relaycommon.MaxTaskDurationSeconds || total+duration > relaycommon.MaxTaskDurationSeconds {
			return errors.New("verified reference video duration must total at most 3600 seconds")
		}
		if a.referenceVideoDurations == nil {
			a.referenceVideoDurations = make(map[string]float64)
		}
		a.referenceVideoDurations[videoURL] = duration
		total += duration
	}
	usageContext["inputVideoSeconds"] = total
	return nil
}

func seedanceReferenceVideoURLs(body map[string]any) ([]string, error) {
	var urls []string
	for _, content := range []any{body["content"], referenceVideoMetadataContent(body)} {
		items, _ := content.([]any)
		for _, item := range items {
			entry, _ := item.(map[string]any)
			_, hasVideo := entry["video_url"]
			if entry["type"] != "video_url" && !hasVideo {
				continue
			}
			value := entry["video_url"]
			if nested, ok := value.(map[string]any); ok {
				value = nested["url"]
			}
			videoURL, _ := value.(string)
			if strings.TrimSpace(videoURL) == "" {
				return nil, errors.New("reference video requires an explicit public URL")
			}
			urls = append(urls, videoURL)
		}
	}
	for _, key := range []string{"input_video", "video", "input_reference"} {
		videoURL, _ := body[key].(string)
		if strings.TrimSpace(videoURL) == "" {
			continue
		}
		if key == "input_reference" {
			base := strings.ToLower(strings.SplitN(strings.SplitN(videoURL, "?", 2)[0], "#", 2)[0])
			if !slices.ContainsFunc([]string{".mp4", ".mov", ".m4v", ".webm"}, func(ext string) bool { return strings.HasSuffix(base, ext) }) {
				continue
			}
		}
		urls = append(urls, videoURL)
	}
	return urls, nil
}

func referenceVideoMetadataContent(body map[string]any) any {
	metadata, _ := body["metadata"].(map[string]any)
	return metadata["content"]
}
