import test from 'node:test';
import assert from 'node:assert/strict';
import {SSEDecoder, coerceField, redact} from './core.js';

test('SSEDecoder handles chunked UTF-8 event boundaries and CRLF', () => {
  const events = [];
  const parser = new SSEDecoder(value => events.push(value));
  parser.push('data: {"type":"del');
  parser.push('ta","text":"你好"}\r\n\r');
  parser.push('\n');
  parser.push('data: [DONE]\n\n', true);
  assert.deepEqual(events, [{type:'delta', text:'你好'}, {type:'done'}]);
});

test('coerceField validates bounded integers, choices and JSON', () => {
  assert.equal(coerceField({type:'integer', min:1, max:5}, '3'), 3);
  assert.throws(() => coerceField({type:'integer', min:1, max:5}, '6'));
  assert.equal(coerceField({type:'select', options:['a','b']}, 'a'), 'a');
  assert.deepEqual(coerceField({type:'object'}, '{"enabled":true}'), {enabled:true});
  assert.throws(() => coerceField({type:'object'}, '[]'));
});

test('scheduled message refresh contract is present', async () => {
  const app = await (await import('node:fs/promises')).readFile(new URL('./app.js', import.meta.url), 'utf8');
  const chat = await (await import('node:fs/promises')).readFile(new URL('./chat.js', import.meta.url), 'utf8');
  assert.match(app, /page==='sessions'/);
  assert.match(app, /chat\?\.refreshHistory/);
  assert.match(chat, /function refreshHistory/);
  assert.match(chat, /sessions\.history/);
});

test('redact masks sensitive fields while retaining configured status', () => {
  assert.deepEqual(redact({token:{configured:true,masked:'••••'}, note:'visible'}), {token:{configured:true,masked:'••••'},note:'visible'});
  assert.deepEqual(redact({api_key:'plaintext'}), {api_key:'••••••••'});
});
