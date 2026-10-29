package api

import (
	"encoding/json"
	"strings"
	"testing"

	"wx_channel/internal/services"
	"wx_channel/pkg/hermes"
)

func TestDownloadTaskProcessingStatePatch(t *testing.T) {
	percent := 45.0
	record := services.DownloadTaskRecord{ID: 1, Status: 2, Progress: 100,
		Postprocess: &hermes.PostprocessProgress{Stage: "transcoding", Percent: &percent}}
	patch := download_task_ws_update_from_record(record)
	if patch.Postprocess == nil || *patch.Postprocess.Percent != 45 || patch.Progress != 100 {
		t.Fatal("processing progress must survive state patches independently of download progress")
	}
	record.Postprocess = nil
	data, err := json.Marshal(download_task_ws_update_from_record(record))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"postprocess":null`) {
		t.Fatal("terminal/restarted task patches must clear stale frontend processing state")
	}
}
