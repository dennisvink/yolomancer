import test from 'node:test';
import assert from 'node:assert/strict';
import {validStage,progressFor} from './progress.mjs';
test('progress reports real stages, failures, and Fargate startup',()=>{
  assert.equal(validStage('build'),true);
  assert.equal(validStage('secret command output'),false);
  assert.equal(validStage('__proto__'),false);
  assert.equal(progressFor({},'build').step,5);
  assert.equal(progressFor({pullStartedAt:new Date()}).message,'Downloading workspace image and preinstalled tools');
  assert.equal(progressFor({},'failed').failed,true);
  assert.equal(progressFor({lastStatus:'STOPPED'},'build').failed,true);
  assert.equal(progressFor({},'ready').step,8);
});
test('reinstall stopping supersedes old bootstrap progress',()=>{
  const p=progressFor({},'ready','reinstall');
  assert.equal(p.indeterminate,true);
  assert.match(p.message,/Stopping old lab/);
});
