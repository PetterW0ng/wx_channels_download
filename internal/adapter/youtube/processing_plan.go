package youtubeadapter

import (
	"strings"

	"wx_channel/pkg/scraper/youtube"
)

type youtube_processing_plan struct {
	VideoCodec string `json:"video_codec"`
	AudioCodec string `json:"audio_codec"`
	Stage      string `json:"stage"`
}

// This is a pre-download estimate, not a replacement for probing actual files.
// Keep the progressive/no-audio cases aligned with Postprocess's early return.
func youtube_download_processing(formats []youtube.VideoFormat) youtube_processing_plan {
	plan := youtube_processing_plan{Stage: "unknown"}
	var video, audio *youtube.VideoFormat
	for i := range formats {
		format := &formats[i]
		if format.HasVideo && video == nil {
			video = format
			plan.VideoCodec = strings.TrimSpace(format.VideoCodec)
		}
		if format.HasAudio && audio == nil {
			audio = format
			plan.AudioCodec = strings.TrimSpace(format.AudioCodec)
		}
	}
	if video == nil {
		return plan
	}
	if video.HasAudio {
		plan.Stage = "direct_download"
		return plan
	}
	if audio == nil {
		plan.Stage = "video_only"
		return plan
	}
	if plan.VideoCodec == "" || plan.AudioCodec == "" {
		return plan
	}
	switch {
	case !copyable_h264_codec(plan.VideoCodec):
		plan.Stage = "transcoding"
	case !aac_codec(plan.AudioCodec):
		plan.Stage = "audio_transcoding"
	default:
		plan.Stage = "merging"
	}
	return plan
}
