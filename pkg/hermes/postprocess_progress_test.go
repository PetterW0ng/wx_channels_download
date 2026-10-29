package hermes

import "testing"

func TestPostprocessProgressSurvivesSnapshots(t *testing.T) {
	d := &HermesEngine{progress_cache: make(map[int]*progress_tracker)}
	d.init_tracker(1, map[int]int64{2: 100}, []ResourceJob{{ID: 2}})
	d.update_tracker(1, 2, 100, 500)
	percent := 25.0
	d.update_postprocess_progress(1, PostprocessProgress{Stage: "transcoding", Percent: &percent})
	first := d.CurrentProgress(1)
	if first.Downloaded != 100 || first.Speed != 0 || first.Resources[0].Speed != 0 || *first.Postprocess.Percent != 25 {
		t.Fatalf("invalid processing snapshot: %+v", first)
	}
	d.update_postprocess_progress(1, PostprocessProgress{Stage: "finalizing"})
	if first.Postprocess.Stage != "transcoding" || d.snapshot_progress(1).Postprocess.Stage != "finalizing" {
		t.Fatal("new stages must not mutate already queued snapshots")
	}
	d.delete_tracker(1)
	if d.CurrentProgress(1) != nil {
		t.Fatal("completed task retained processing state")
	}
}
