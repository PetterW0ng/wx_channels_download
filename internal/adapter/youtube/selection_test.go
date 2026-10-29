package youtubeadapter

import (
	"reflect"
	"testing"

	"wx_channel/pkg/scraper/youtube"
)

func TestYouTubeDownloadFormatPreference(t *testing.T) {
	h264 := youtube.VideoFormat{ID: "h264", URL: "https://example.com/h264", HasVideo: true, Height: 1080, FPS: 30, VideoCodec: "avc1.640028", Bitrate: 2_000_000}
	av1 := h264
	av1.ID, av1.VideoCodec, av1.Bitrate = "av1", "av01.0.08M.08", 3_000_000
	vp9 := av1
	vp9.ID, vp9.VideoCodec = "vp9", "vp9"
	aac := youtube.VideoFormat{ID: "aac", URL: "https://example.com/aac", HasAudio: true, AudioCodec: "mp4a.40.2", Bitrate: 128_000}
	opus := aac
	opus.ID, opus.AudioCodec, opus.Bitrate = "opus", "opus", 160_000
	uhd := av1
	uhd.ID, uhd.Height = "4k", 2160
	fast := vp9
	fast.ID, fast.FPS = "60fps", 60
	original := opus
	original.ID, original.AudioTrackID, original.AudioIsOriginal = "original", "en-original", true
	dub := aac
	dub.ID, dub.AudioTrackID, dub.AudioIsDefault = "dub", "zh-dub", true
	legacy := opus
	legacy.ID = "legacy"
	progressive := h264
	progressive.ID, progressive.HasAudio, progressive.AudioCodec = "progressive", true, "mp4a.40.2"
	progressiveOpus := progressive
	progressiveOpus.ID, progressiveOpus.AudioCodec, progressiveOpus.Bitrate = "progressive-opus", "opus", 4_000_000
	unavailable := h264
	unavailable.NeedsSignature = true
	missingURL := h264
	missingURL.URL = ""
	unknown := h264
	unknown.ID, unknown.VideoCodec, unknown.Ext, unknown.Bitrate = "unknown-mp4", "", "mp4", 5_000_000
	high10 := h264
	high10.ID, high10.VideoCodec, high10.Bitrate = "high10", "avc1.6e0028", 6_000_000
	higherAAC := aac
	higherAAC.ID, higherAAC.Bitrate = "aac-high", 256_000

	for _, tc := range []struct {
		name    string
		formats []youtube.VideoFormat
		config  map[string]any
		want    []string
	}{
		{"copy compatible streams", []youtube.VideoFormat{av1, vp9, h264, opus, aac}, nil, []string{"h264", "aac"}},
		{"preserve resolution", []youtube.VideoFormat{h264, uhd, opus, aac}, nil, []string{"4k", "aac"}},
		{"preserve frame rate", []youtube.VideoFormat{h264, fast, aac}, nil, []string{"60fps", "aac"}},
		{"fallback without compatible codecs", []youtube.VideoFormat{vp9, opus}, nil, []string{"vp9", "opus"}},
		{"respect explicit key", []youtube.VideoFormat{h264, av1, opus, aac}, map[string]any{"video_variant_key": "format_av1"}, []string{"av1", "aac"}},
		{"respect explicit spec", []youtube.VideoFormat{h264, vp9, opus, aac}, map[string]any{"video_variant_spec": "vp9"}, []string{"vp9", "aac"}},
		{"original before AAC dub", []youtube.VideoFormat{h264, original, dub}, nil, []string{"h264", "original"}},
		{"legacy before AAC dub", []youtube.VideoFormat{h264, legacy, dub}, nil, []string{"h264", "legacy"}},
		{"progressive AAC preference", []youtube.VideoFormat{progressiveOpus, progressive, opus, aac}, nil, []string{"progressive"}},
		{"preserve progressive selection", []youtube.VideoFormat{progressive, uhd, aac}, nil, []string{"progressive"}},
		{"skip unresolved signature", []youtube.VideoFormat{unavailable, vp9, aac}, nil, []string{"vp9", "aac"}},
		{"skip missing URL", []youtube.VideoFormat{missingURL, vp9, aac}, nil, []string{"vp9", "aac"}},
		{"do not infer codec from MP4", []youtube.VideoFormat{unknown, h264, aac}, nil, []string{"h264", "aac"}},
		{"avoid high 10 profile", []youtube.VideoFormat{high10, h264, aac}, nil, []string{"h264", "aac"}},
		{"best bitrate within AAC", []youtube.VideoFormat{h264, higherAAC, aac, opus}, nil, []string{"h264", "aac-high"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Parser ordering must not change the selected codecs.
			for pass := 0; pass < 2; pass++ {
				info := &youtube.VideoInfo{ID: "test", Formats: tc.formats}
				got, err := select_download_formats(info, tc.config)
				if err != nil {
					t.Fatal(err)
				}
				if ids := format_ids(got); !reflect.DeepEqual(ids, tc.want) {
					t.Fatalf("got %v, want %v", ids, tc.want)
				}
				if tc.config == nil {
					detail := best_video_detail_format(info)
					if detail == nil || detail.ID != tc.want[0] {
						t.Fatalf("detail default differs from download: %+v", detail)
					}
				}
				for i, j := 0, len(tc.formats)-1; i < j; i, j = i+1, j-1 {
					tc.formats[i], tc.formats[j] = tc.formats[j], tc.formats[i]
				}
			}
		})
	}
}
