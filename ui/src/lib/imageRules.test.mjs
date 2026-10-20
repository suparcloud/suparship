import { test } from 'node:test';
import assert from 'node:assert/strict';
import { seedImageRules, savedByTagKeyFromComponents, missingComponentImageBindings, imageRulesToComponentImages } from './imageRules.ts';

test('a checked shared repository exposes and repairs missing component bindings', () => {
  const discovered = ['web', 'worker'].map(component => ({component, name:'image', repository:'example/app', tagKey:'image.tag'}));
  const components = [{name:'web', images:[]}, {name:'worker', images:[{tagKey:'image.tag', tagPattern:'^[0-9a-f]{8}$', selectionStrategy:'NewestBuild'}]}];
  const rules = seedImageRules(discovered, savedByTagKeyFromComponents(components), true);
  assert.equal(rules['example/app'].watched, true);
  assert.deepEqual(missingComponentImageBindings(discovered, rules, components), ['web']);
  const patch = imageRulesToComponentImages(discovered, rules, components.map(c => c.name));
  assert.equal(patch.web[0].tagPattern, '^[0-9a-f]{8}$');
  assert.deepEqual(missingComponentImageBindings(discovered, rules, components.map(c => ({...c, images:patch[c.name]}))), []);
  rules['example/app'].watched = false;
  assert.deepEqual(missingComponentImageBindings(discovered, rules, components), []);
});

test('cross-environment discovery saves web even when validation only discovers workers', () => {
  const image = component => ({component, name:'image', repository:'example/app', tagKey:'image.tag'});
  const staging = [image('web'), image('worker')];
  const validation = [image('worker')];
  const discovered = [...staging, ...validation];
  const rules = {'example/app': {watched:true, tagPattern:'^[0-9a-f]{8}$', selectionStrategy:'NewestBuild'}};
  const patch = imageRulesToComponentImages(discovered, rules, ['web', 'worker']);
  assert.equal(patch.web.length, 1);
  assert.equal(patch.worker.length, 1);
  assert.equal(patch.web[0].tagKey, 'image.tag');
  rules['example/app'].watched = false;
  assert.deepEqual(imageRulesToComponentImages(discovered, rules, ['web', 'worker']), {web:[], worker:[]});
});
