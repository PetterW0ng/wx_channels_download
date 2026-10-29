package youtubeadapter

import (
	"encoding/json"
	"testing"

	"wx_channel/pkg/scraper/youtube"
)

func TestYouTubeProcessingEstimate(t *testing.T) {
	for _, tc := range []struct {
		name, video, audio, stage string
		progressive, noAudio      bool
	}{
		{"copy both", "avc1.640028", "mp4a.40.2", "merging", false, false},
		{"copy video", "avc1.640028", "opus", "audio_transcoding", false, false},
		{"transcode VP9", "vp9", "mp4a.40.2", "transcoding", false, false},
		{"transcode high 10", "avc1.6e0028", "mp4a.40.2", "transcoding", false, false},
		{"unknown video", "", "mp4a.40.2", "unknown", false, false},
		{"unknown audio", "avc1.640028", "", "unknown", false, false},
		{"progressive bypasses conversion", "vp9", "opus", "direct_download", true, false},
		{"no audio bypasses conversion", "vp9", "", "video_only", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			video := youtube.VideoFormat{ID: "v", URL: "https://example.com/video", HasVideo: true, VideoCodec: tc.video}
			formats := []youtube.VideoFormat{video}
			if tc.progressive {
				formats[0].HasAudio, formats[0].AudioCodec = true, tc.audio
			} else if !tc.noAudio {
				formats = append(formats, youtube.VideoFormat{ID: "a", URL: "https://example.com/audio", HasAudio: true, AudioCodec: tc.audio})
			}
			want := youtube_processing_plan{VideoCodec: tc.video, AudioCodec: tc.audio, Stage: tc.stage}
			if got := youtube_download_processing(formats); got != want {
				t.Fatalf("estimate: got %+v, want %+v", got, want)
			}
			info := &youtube.VideoInfo{ID: "test", Formats: formats}
			variants := youtube_video_variants(info, &formats[0])
			var metadata struct {
				Plan youtube_processing_plan `json:"youtube_processing"`
			}
			if err := json.Unmarshal([]byte(variants[0].Metadata), &metadata); err != nil {
				t.Fatal(err)
			}
			if metadata.Plan != want {
				t.Fatalf("variant estimate: got %+v, want %+v", metadata.Plan, want)
			}
			if err := json.Unmarshal([]byte(build_extra_json(info, formats)), &metadata); err != nil {
				t.Fatal(err)
			}
			if metadata.Plan != want {
				t.Fatalf("resource estimate: got %+v, want %+v", metadata.Plan, want)
			}
		})
	}
}

func TestYouTubeVariantEstimateUsesSelectedAudio(t *testing.T) {
	video := youtube.VideoFormat{ID: "v", URL: "https://example.com/v", HasVideo: true, VideoCodec: "avc1.640028"}
	opus := youtube.VideoFormat{ID: "opus", URL: "https://example.com/opus", HasAudio: true, AudioCodec: "opus", Bitrate: 160_000}
	aac := opus
	aac.ID, aac.AudioCodec, aac.Bitrate = "aac", "mp4a.40.2", 128_000
	for _, original := range []bool{false, true} {
		opus.AudioIsOriginal = original
		info := &youtube.VideoInfo{ID: "test", Formats: []youtube.VideoFormat{video, opus, aac}}
		selected, err := select_download_formats(info, nil)
		if err != nil {
			t.Fatal(err)
		}
		var metadata struct {
			Plan youtube_processing_plan `json:"youtube_processing"`
		}
		variants := youtube_video_variants(info, &video)
		if err := json.Unmarshal([]byte(variants[0].Metadata), &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.Plan != youtube_download_processing(selected) {
			t.Fatal("displayed audio does not match the selected download audio")
		}
	}
}
