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
