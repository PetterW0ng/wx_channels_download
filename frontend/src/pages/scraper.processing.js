const processing_labels = {
  merging: "预计直接合并，无需转码",
  audio_transcoding: "预计仅转音频为 AAC，视频直接复制",
  transcoding: "预计视频需转码为 H.264，耗时较长",
  direct_download: "已含音视频，直接下载，无需合并",
  video_only: "无可用音轨，仅下载视频",
  unknown: "编码信息不足，下载后检测是否需要转码",
};

export function codec_label(value) {
  const raw = String(value || "").trim();
  if (!raw) return "未知";
  const codec = raw.toLowerCase();
  let name = "";
  if (codec === "h264" || /^avc[13]\./.test(codec)) name = "H.264";
  else if (codec === "av1" || codec.startsWith("av01.")) name = "AV1";
  else if (codec === "vp9" || codec.startsWith("vp09.")) name = "VP9";
  else if (codec === "aac" || /^mp4a\.40\.(2|5|29)$/.test(codec)) name = "AAC";
  else if (codec === "opus") name = "Opus";
  return name ? (name.toLowerCase() === codec ? name : `${name} (${raw})`) : raw;
}

export function format_youtube_processing(plan, kind = "video") {
  if (!plan || typeof plan !== "object") {
    return { codec_text: "", processing_text: "" };
  }
  if (kind.startsWith("audio")) {
    return { codec_text: `音频 ${codec_label(plan.audio_codec)}`, processing_text: "" };
  }
  const audio = plan.stage === "video_only" ? "无音轨" : codec_label(plan.audio_codec);
  return {
    codec_text: `视频 ${codec_label(plan.video_codec)} · 音频 ${audio}`,
    processing_text: processing_labels[plan.stage] || processing_labels.unknown,
  };
}
