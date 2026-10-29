package youtubeadapter

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"wx_channel/pkg/hermes"
)

func TestYouTubeCodecSelection(t *testing.T) {
	for _, tc := range []struct {
		name, video, pixel, audio, stage string
		args                             []string
	}{
		{"compatible", "h264", "yuv420p", "aac", "merging", []string{"-c:v", "copy", "-c:a", "copy"}},
		{"opus", "h264", "yuv420p", "opus", "audio_transcoding", []string{"-c:v", "copy", "-c:a", "aac"}},
		{"vp9", "vp9", "yuv420p", "aac", "transcoding", []string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "copy"}},
		{"10bit h264", "h264", "yuv420p10le", "opus", "transcoding", []string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac"}},
		{"unknown", "", "", "", "transcoding", []string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args, stage := youtube_codec_args(youtube_media_info{Codec: tc.video, PixelFormat: tc.pixel}, youtube_media_info{Codec: tc.audio})
			if stage != tc.stage || !reflect.DeepEqual(args, tc.args) {
				t.Fatalf("got %s %v, want %s %v", stage, args, tc.stage, tc.args)
			}
		})
	}
}

func TestYouTubeProgressRecords(t *testing.T) {
	var records []hermes.PostprocessProgress
	w := &youtube_progress_writer{values: make(map[string]string), report: func(values map[string]string) {
		records = append(records, youtube_processing_progress("transcoding", 100, values))
	}}
	for _, chunk := range []string{"out_time_", "us=25000000\nspeed=0.5x\nprogress=cont", "inue\nout_time_us=100000000\nspeed=N/A\nprogress=end\n"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if len(records) != 2 || *records[0].Percent != 25 || *records[0].ETASeconds != 150 {
		t.Fatalf("invalid progress: %+v", records)
	}
	if *records[1].Percent != 99.9 || records[1].ETASeconds != nil {
		t.Fatal("timeline completion must not claim the file is finished")
	}
	unknown := youtube_processing_progress("transcoding", 0, map[string]string{"out_time_us": "NaN", "speed": "Inf"})
	if unknown.Percent != nil || unknown.ETASeconds != nil {
		t.Fatal("unknown duration should not fabricate estimates")
	}
}

func TestYouTubeMergeFailurePreservesSources(t *testing.T) {
	oldProbe, oldRun := probe_youtube_media, run_youtube_ffmpeg
	t.Cleanup(func() { probe_youtube_media, run_youtube_ffmpeg = oldProbe, oldRun })
	probe_youtube_media = func(context.Context, string, string, string) youtube_media_info { return youtube_media_info{} }
	dir := t.TempDir()
	video, audio := filepath.Join(dir, "video"), filepath.Join(dir, "audio")
	for _, path := range []string{video, audio} {
		if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run_youtube_ffmpeg = func(ctx context.Context, path string, report func(map[string]string), args ...string) ([]byte, error) {
		if err := os.WriteFile(args[len(args)-1], []byte("partial"), 0600); err != nil {
			t.Fatal(err)
		}
		return []byte("conversion failed"), errors.New("exit status 1")
	}
	if _, err := youtube_merge_media(context.Background(), "ffmpeg", video, audio, nil); err == nil || !strings.Contains(err.Error(), "conversion failed") {
		t.Fatalf("missing ffmpeg error: %v", err)
	}
	for _, path := range []string{video, audio} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "original" {
			t.Fatal("source changed after failure")
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, ".youtube-merge-*"))
	if len(files) != 0 {
		t.Fatal("partial output was not removed")
	}
}

func TestYouTubeMergeRealMedia(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	run := func(args ...string) string {
		t.Helper()
		data, err := exec.Command(ffmpeg, append([]string{"-v", "error", "-nostdin", "-y"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("ffmpeg: %v: %s", err, data)
		}
		return string(data)
	}
	for _, tc := range []struct{ name, videoCodec, audioCodec, videoExt, audioExt, stage string }{
		{"copy both", "libx264", "aac", ".mp4", ".m4a", "merging"},
		{"copy video", "libx264", "libopus", ".mp4", ".webm", "audio_transcoding"},
		{"transcode video", "libvpx-vp9", "libopus", ".webm", ".webm", "transcoding"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			video, audio := filepath.Join(dir, "video"+tc.videoExt), filepath.Join(dir, "audio"+tc.audioExt)
			run("-f", "lavfi", "-i", "testsrc2=size=64x64:rate=10", "-t", "1", "-c:v", tc.videoCodec, "-pix_fmt", "yuv420p", video)
			run("-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "1", "-c:a", tc.audioCodec, audio)
			var reports []hermes.PostprocessProgress
			merged, err := youtube_merge_media(context.Background(), ffmpeg, video, audio, func(p hermes.PostprocessProgress) { reports = append(reports, p) })
			if err != nil {
				t.Fatal(err)
			}
			v := probe_youtube_media(context.Background(), ffmpeg, merged, "v:0")
			a := probe_youtube_media(context.Background(), ffmpeg, merged, "a:0")
			if v.Codec != "h264" || v.PixelFormat != "yuv420p" || a.Codec != "aac" || v.Duration < 0.9 {
				t.Fatalf("incompatible output: video=%+v audio=%+v", v, a)
			}
			if len(reports) < 3 || reports[0].Stage != tc.stage || reports[len(reports)-1].Stage != "finalizing" {
				t.Fatalf("missing processing events: %+v", reports)
			}
			if reports[1].Percent == nil {
				t.Fatal("real ffmpeg did not produce percentage progress")
			}
			if tc.videoCodec == "libx264" {
				before := run("-i", video, "-map", "0:v:0", "-c", "copy", "-f", "hash", "-hash", "sha256", "-")
				after := run("-i", merged, "-map", "0:v:0", "-c", "copy", "-f", "hash", "-hash", "sha256", "-")
				if before != after {
					t.Fatal("compatible video packets were changed")
				}
			}
		})
	}
}
