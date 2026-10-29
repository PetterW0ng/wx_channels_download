package youtubeadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"wx_channel/pkg/hermes"
)

type youtube_media_info struct {
	Codec       string
	PixelFormat string
	Duration    float64
}

// Probe the downloaded file rather than inferring codecs from its extension.
// Older installations with only ffmpeg retain the conservative transcode path;
// missing metadata never causes an incompatible stream to be copied.
var probe_youtube_media = func(ctx context.Context, ffmpeg_path, path, stream string) youtube_media_info {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	probe_path := filepath.Join(filepath.Dir(ffmpeg_path), "ffprobe"+filepath.Ext(ffmpeg_path))
	if resolved, err := exec.LookPath(probe_path); err == nil {
		probe_path = resolved
	} else if resolved, err := exec.LookPath("ffprobe"); err == nil {
		probe_path = resolved
	} else {
		return youtube_media_info{}
	}
	output, err := exec.CommandContext(ctx, probe_path, "-v", "error", "-select_streams", stream,
		"-show_entries", "stream=codec_name,pix_fmt,duration:format=duration", "-of", "json", path).Output()
	if err != nil {
		return youtube_media_info{}
	}
	var data struct {
		Streams []struct {
			Codec       string `json:"codec_name"`
			PixelFormat string `json:"pix_fmt"`
			Duration    string `json:"duration"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if json.Unmarshal(output, &data) != nil || len(data.Streams) == 0 {
		return youtube_media_info{}
	}
	duration := youtube_positive_number(data.Streams[0].Duration)
	if duration == 0 {
		duration = youtube_positive_number(data.Format.Duration)
	}
	return youtube_media_info{Codec: data.Streams[0].Codec, PixelFormat: data.Streams[0].PixelFormat, Duration: duration}
}

func youtube_codec_args(video, audio youtube_media_info) ([]string, string) {
	args := []string{"-c:v", "copy"}
	stage := "merging"
	if video.Codec != "h264" || (video.PixelFormat != "yuv420p" && video.PixelFormat != "yuvj420p") {
		args = []string{"-c:v", "libx264", "-pix_fmt", "yuv420p"}
		stage = "transcoding"
	}
	if audio.Codec == "aac" {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args, "-c:a", "aac")
		if stage == "merging" {
			stage = "audio_transcoding"
		}
	}
	return args, stage
}

func youtube_positive_number(value string) float64 {
	n, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
		return 0
	}
	return n
}

func youtube_processing_progress(stage string, duration float64, values map[string]string) hermes.PostprocessProgress {
	progress := hermes.PostprocessProgress{Stage: stage}
	if duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
		return progress
	}
	position := youtube_positive_number(values["out_time_us"]) / 1e6
	// FFmpeg can reach the end of the timeline before faststart has moved the
	// MP4 index. Completion belongs to the successful process exit + final rename.
	percent := math.Min(99.9, position/duration*100)
	progress.Percent = &percent
	speed := youtube_positive_number(strings.TrimSuffix(values["speed"], "x"))
	if speed > 0 && position > 0 && position < duration {
		remaining := (duration - position) / speed
		progress.ETASeconds = &remaining
	}
	return progress
}

var run_youtube_ffmpeg = func(ctx context.Context, ffmpeg_path string, report func(map[string]string), args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, ffmpeg_path, args...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	command.Stdout = &youtube_progress_writer{report: report, values: make(map[string]string)}
	err := command.Run()
	return stderr.Bytes(), err
}

// FFmpeg writes key=value records terminated by progress=continue/end. Writes
// may split a line or contain several records, so buffer until each newline.
type youtube_progress_writer struct {
	pending string
	values  map[string]string
	report  func(map[string]string)
}

func (w *youtube_progress_writer) Write(data []byte) (int, error) {
	w.pending += string(data)
	for {
		line, rest, found := strings.Cut(w.pending, "\n")
		if !found {
			break
		}
		w.pending = rest
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		w.values[key] = value
		if key == "progress" {
			if w.report != nil {
				w.report(w.values)
			}
			w.values = make(map[string]string)
		}
	}
	return len(data), nil
}
