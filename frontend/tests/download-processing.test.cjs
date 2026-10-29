const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../src/pages/downloadv2.processing.js'), 'utf8');
const modulePromise = import('data:text/javascript;base64,' + Buffer.from(source).toString('base64'));

test('processing shows a separate percentage and ETA instead of zero download speed', async () => {
  const { format_postprocess } = await modulePromise;
  assert.deepEqual(format_postprocess({ stage: 'transcoding', percent: 65.3, eta_seconds: 125 }), {
    status_text: '视频转码中 65.3%', progress_text: '65', speed_text: '预计剩余 3 分钟',
  });
  assert.match(format_postprocess({ stage: 'audio_transcoding', percent: 2 }).status_text, /音频转换中/);
  assert.match(format_postprocess({ stage: 'merging', percent: 2 }).status_text, /合并中/);
});

test('unknown duration and finalization do not display a fake percentage or ETA', async () => {
  const { format_postprocess } = await modulePromise;
  assert.deepEqual(format_postprocess({ stage: 'finalizing', percent: null, eta_seconds: null }), {
    status_text: '正在完成文件', progress_text: '…', speed_text: '',
  });
  assert.equal(format_postprocess({ stage: 'merging', percent: 100 }).progress_text, '99');
  assert.equal(format_postprocess(null), null);
  assert.equal(format_postprocess({ stage: 'unknown' }), null);
});
