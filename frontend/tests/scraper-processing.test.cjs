const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../src/pages/scraper.processing.js'), 'utf8');
const modulePromise = import('data:text/javascript;base64,' + Buffer.from(source).toString('base64'));

test('codec display names identify the codec and preserve profile information', async () => {
  const { codec_label } = await modulePromise;
  assert.equal(codec_label('avc1.640028'), 'H.264 (avc1.640028)');
  assert.equal(codec_label('av01.0.08M.08'), 'AV1 (av01.0.08M.08)');
  assert.equal(codec_label('vp9'), 'VP9');
  assert.equal(codec_label('mp4a.40.2'), 'AAC (mp4a.40.2)');
  assert.equal(codec_label('opus'), 'Opus');
  assert.equal(codec_label(''), '未知');
  assert.equal(codec_label('future-codec'), 'future-codec');
});

test('preview distinguishes copying, audio conversion, video conversion and no merge', async () => {
  const { format_youtube_processing: format } = await modulePromise;
  const plan = { video_codec: 'avc1.640028', audio_codec: 'mp4a.40.2', stage: 'merging' };
  assert.match(format(plan).codec_text, /视频 H\.264.*音频 AAC/);
  assert.match(format(plan).processing_text, /预计直接合并/);
  assert.match(format({ ...plan, audio_codec: 'opus', stage: 'audio_transcoding' }).processing_text, /仅转音频/);
  assert.match(format({ ...plan, video_codec: 'vp9', stage: 'transcoding' }).processing_text, /视频需转码/);
  assert.match(format({ ...plan, stage: 'direct_download' }).processing_text, /无需合并/);
  assert.match(format({ ...plan, audio_codec: '', stage: 'video_only' }).codec_text, /无音轨/);
  assert.match(format({ stage: 'unknown' }).processing_text, /下载后检测/);
  assert.equal(format(plan, 'audio/mp4').processing_text, '');
  assert.match(format(plan, 'audio/mp4').codec_text, /^音频 AAC/);
  assert.deepEqual(format(undefined), { codec_text: '', processing_text: '' });
});

test('variant cards and resource preview receive the backend processing metadata', async () => {
  const { format_youtube_processing } = await modulePromise;
  const model = fs.readFileSync(path.join(__dirname, '../src/pages/scraper.model.js'), 'utf8')
    .replace(/^import .*;\n/gm, '').replace(/^export .*;\n/gm, '');
  const context = vm.createContext({
    window: { PLATFORM_NAMES: {}, __store: {}, DOWNLOAD_RESOURCE_SUFFIXES: {} },
    Timeless: { kit: {} }, format_youtube_processing,
  });
  vm.runInContext(model, context);
  const plan = { video_codec: 'avc1.640028', audio_codec: 'mp4a.40.2', stage: 'merging' };
  const metadata = JSON.stringify({ youtube_processing: plan });
  for (const variant of [
    { quality: '1080p', metadata },
    { Quality: '1080p', Metadata: metadata },
  ]) {
    const card = context.normalize_content_video_variant(variant, 0);
    assert.match(card.meta_text, /H\.264.*AAC/);
    assert.match(card.processing_text, /预计直接合并/);
  }
  const resource = context.normalize_download_resource({ Resource: { Name: 'video', Kind: 'video/mp4', Extra: metadata } }, 0, 'test');
  assert.match(resource.meta_text, /H\.264.*AAC/);
  assert.match(resource.processing_text, /预计直接合并/);
  const otherPlatform = context.normalize_content_video_variant({ codec: 'hevc', metadata: '{}' }, 0);
  assert.match(otherPlatform.meta_text, /hevc/);
  assert.equal(otherPlatform.processing_text, '');
});
