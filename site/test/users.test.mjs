// How clones become users (#323, #1245): the machine the census reads from an old device ID
// (census.mjs, machine()), and how the site joins clones into users (src/repos.js, users()).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { machine } from '../census/census.mjs';
import { users, JOIN } from '../src/repos.js';

test("an old device ID names its machine; a new one, and a hostname strangers share, don't", () => {
  assert.equal(machine('laptop-gjlpfunp-ccf2'), 'laptop-gjlpfunp');
  assert.equal(machine('aadityas-macbook-pro-2-5f0e'), 'aadityas-macbook-pro-2');
  assert.equal(machine('black-forest-0a1b'), 'black-forest');
  // New IDs (#330) carry no hostname: six or eight hex digits after device-.
  for (const id of ['device-1a2b3c', 'device-1a2b3c4d']) assert.equal(machine(id), null, id);
  // Assigned by a network or a cloud machine, or a default many machines have.
  for (const id of ['wifi-10-44-12-7-ab12', 'ip-10-0-0-1-ab12', 'codespaces-3f2a1b-ab12', 'users-macbook-pro-ab12',
    'macbook-pro-ab12', 'macbook-air-2-ab12', 'ubuntu-ab12', 'localhost-ab12', 'device-ab12', 'desktop-ab12']) {
    assert.equal(machine(id), null, id);
  }
  // Windows makes up a hostname per machine (DESKTOP-7Q2K9FX), so that one is a machine's.
  assert.equal(machine('desktop-7q2k9fx-ab12'), 'desktop-7q2k9fx');
});

// A device's row as repoNumbers reads it.
const dev = (repo, device, more = {}) => ({ repo, device, person: null, account: null, machine: null, first: '2026-11-20', last: '2026-11-24', ...more });

test('clones on one machine are one user, across repos, from the first day of any to the last', () => {
  const rows = [
    dev('a', '1', { machine: 'm', first: '2026-11-02', last: '2026-11-10' }),
    dev('b', '2', { machine: 'm', first: '2026-11-05', last: '2026-11-24' }),
    dev('c', '3', { machine: 'm', person: 'p' }),
    dev('c', '4'),
  ];
  const u = users(rows);
  assert.equal(u.users.size, 2);
  assert.deepEqual([...u.users.values()].find(x => x.first === '2026-11-02'), { first: '2026-11-02', last: '2026-11-24' });
  // The machine's user has a git name among its clones; the lone clone has nothing.
  assert.deepEqual(u.by, { author: 0, account: 0, name: 1, machine: 0, device: 1 });
  // The old count is one per clone, but for a git name.
  assert.equal(users(rows, { join: {} }).users.size, 4);
});

test('a machine joins clones counted by an account to those counted by nothing', () => {
  const u = users([dev('a', '1', { machine: 'm', account: 'x' }), dev('b', '2', { machine: 'm' }), dev('c', '3', { account: 'x' })]);
  assert.equal(u.users.size, 1);
  assert.deepEqual(u.by, { author: 0, account: 1, name: 0, machine: 0, device: 0 });
});

test('a lone clone with a machine is tied to it, not counted one per clone', () => {
  assert.deepEqual(users([dev('a', '1', { machine: 'm' }), dev('a', '2')]).by, { author: 0, account: 0, name: 0, machine: 1, device: 1 });
});

test("a machine whose clones carry two accounts joins nothing, and the report hears of it", () => {
  const u = users([dev('a', '1', { machine: 'm', account: 'x' }), dev('b', '2', { machine: 'm', account: 'y' }), dev('c', '3', { machine: 'm' })]);
  assert.equal(u.users.size, 3);
  assert.deepEqual(u.refused, [{ kind: 'machine', key: 'm', devices: 3, repos: 3, accounts: 2 }]);
  // Its clones are counted as if it named no machine.
  assert.deepEqual(u.by, { author: 0, account: 2, name: 0, machine: 0, device: 1 });
});

test('a machine over the limits on clones or repos joins nothing', () => {
  const { devices, repos } = JOIN.machine;
  const spread = Array.from({ length: repos + 1 }, (_, i) => dev(`r${i}`, '1', { machine: 'm' }));
  assert.equal(users(spread).users.size, repos + 1);
  const crowd = Array.from({ length: devices + 1 }, (_, i) => dev('r', String(i), { machine: 'm' }));
  assert.equal(users(crowd).users.size, devices + 1);
  assert.equal(users(crowd.slice(1)).users.size, 1);
});

test('two machines that each pass cannot chain two accounts into one user through a shared git name', () => {
  const u = users([
    dev('a', '1', { machine: 'm1', account: 'x' }), dev('a', '2', { machine: 'm1', person: 'p' }),
    dev('b', '3', { machine: 'm2', account: 'y' }), dev('b', '4', { machine: 'm2', person: 'p' }),
  ]);
  // m1 joins x and p; m2 would then join y to x through p, so it is refused.
  assert.equal(u.users.size, 2);
  assert.deepEqual(u.refused.map(r => [r.key, r.accounts]), [['m2', 2]]);
});

test('a slug the old IDs may have cut short, and a Mac default without dashes, name no machine', () => {
  assert.equal(machine('acme-corp-workstation-00-ab12'), null);
  assert.equal(machine('acme-corp-workstation-0-ab12'), null);
  assert.equal(machine('acme-corp-workstation-ab12'), 'acme-corp-workstation');
  for (const id of ['macbookpro-ab12', 'macbookair-ab12', 'my-macbookpro-2-ab12']) assert.equal(machine(id), null, id);
});

// #1244: the commit identity a clone's fingerprint matched, in the same hash space as an account.
test('clones whose fingerprints match one commit identity are one user, across repos, and join an account matched by name', () => {
  const rows = [
    dev('a', '1', { author: 'x' }),
    dev('b', '2', { author: 'x', machine: 'm' }),
    dev('c', '3', { account: 'x' }), // the same account, matched by the owner's git name
    dev('c', '4', { machine: 'm' }),
    dev('d', '5', { author: 'y' }),
  ];
  const u = users(rows);
  assert.equal(u.users.size, 2);
  assert.deepEqual(u.by, { author: 2, account: 0, name: 0, machine: 0, device: 0 });
  // The old count never reads the fingerprint: one per clone, but for the account.
  assert.equal(users(rows, { join: {} }).users.size, 5);
});

test('a fingerprint wins over the account matched by name, and lends it to the git name in other repos', () => {
  const u = users([
    dev('a', '1', { author: 'x', account: 'z', person: 'p' }),
    dev('b', '2', { person: 'p' }), // the same git name, with no match of its own
    dev('c', '3', { account: 'z' }),
  ]);
  assert.equal(u.users.size, 2);
  assert.deepEqual(u.by, { author: 1, account: 1, name: 0, machine: 0, device: 0 });
});

test('a commit identity matched in more repos than JOIN allows joins nothing, and the report hears of it', () => {
  const { repos } = JOIN.author;
  const spread = Array.from({ length: repos + 1 }, (_, i) => dev(`r${i}`, '1', { author: 'x' }));
  const u = users(spread);
  assert.equal(u.users.size, repos + 1);
  assert.deepEqual(u.refused, [{ kind: 'author', key: 'x', devices: repos + 1, repos: repos + 1 }]);
  assert.deepEqual(u.by, { author: 0, account: 0, name: 0, machine: 0, device: repos + 1 });
  // A refused fingerprint leaves the clone to the account its name matched.
  assert.equal(users([...spread, dev('r0', '2', { account: 'z' }), dev('r1', '9', { author: 'x', account: 'z' })]).users.size, repos + 2);
  // Clones are not capped: one person's disposable boxes are a clone each.
  const boxes = Array.from({ length: 40 }, (_, i) => dev(`r${i % repos}`, String(i), { author: 'x' }));
  assert.equal(users(boxes).users.size, 1);
});
