const PROCESSING_LABELS = {
  preparing: "准备处理",
  merging: "合并中",
  transcoding: "视频转码中",
  audio_transcoding: "音频转换中",
  finalizing: "正在完成文件",
};

export function format_postprocess(progress) {
  if (!progress || !PROCESSING_LABELS[progress.stage]) return null;
  const known = typeof progress.percent === "number" && Number.isFinite(progress.percent);
  const percent = known ? Math.max(0, Math.min(99.9, progress.percent)) : null;
  const seconds = progress.eta_seconds;
  let remaining = "";
  if (typeof seconds === "number" && Number.isFinite(seconds) && seconds > 0) {
    const duration = seconds < 60
      ? `${Math.ceil(seconds)} 秒`
      : seconds < 3600
        ? `${Math.ceil(seconds / 60)} 分钟`
        : `${Math.floor(seconds / 3600)} 小时 ${Math.ceil((seconds % 3600) / 60)} 分钟`;
    remaining = `预计剩余 ${duration}`;
  }
  return {
    status_text: PROCESSING_LABELS[progress.stage] + (known ? ` ${percent.toFixed(1)}%` : ""),
    progress_text: known ? String(Math.floor(percent)) : "…",
    speed_text: remaining,
  };
}
