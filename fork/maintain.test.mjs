import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { after, before, test } from 'node:test';
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const sourceDir = dirname(fileURLToPath(import.meta.url));
const maintainerSource = resolve(sourceDir, 'maintain.mjs');
let fixtureRoot;
let repo;
let updater;
let manager;
let upstreamBare;
let originBare;

function run(command, args, cwd) {
  const result = spawnSync(command, args, { cwd, encoding: 'utf8', windowsHide: true });
  assert.equal(result.status, 0, `${command} ${args.join(' ')} failed:\n${result.stderr}`);
  return result.stdout.trim();
}

function git(args, cwd = repo) {
  return run('git', args, cwd);
}

function managerRun(args) {
  return spawnSync(process.execPath, [manager, ...args], {
    cwd: repo,
    encoding: 'utf8',
    windowsHide: true,
  });
}

function commit(cwd, message) {
  git(['add', '-A'], cwd);
  git(['commit', '-m', message], cwd);
}

before(() => {
  fixtureRoot = mkdtempSync(join(tmpdir(), 'new-api-fork-maintenance-'));
  repo = join(fixtureRoot, 'repo');
  updater = join(fixtureRoot, 'updater');
  upstreamBare = join(fixtureRoot, 'upstream.git');
  originBare = join(fixtureRoot, 'origin.git');
  manager = join(repo, 'fork', 'maintain.mjs');
  mkdirSync(join(repo, 'fork'), { recursive: true });
  copyFileSync(maintainerSource, manager);
  git(['init', '--bare', '--initial-branch=main', upstreamBare], fixtureRoot);
  git(['init', '--bare', '--initial-branch=main', originBare], fixtureRoot);
  git(['init', '-b', 'main', repo], fixtureRoot);
  git(['config', 'user.name', 'Fork Test'], repo);
  git(['config', 'user.email', 'fork-test@example.invalid'], repo);
  git(['remote', 'add', 'origin', originBare], repo);
  git(['remote', 'add', 'upstream', 'https://github.com/QuantumNous/new-api.git'], repo);
  const localUpstreamUrl = pathToFileURL(upstreamBare).href;
  git(['config', '--local', `url.${localUpstreamUrl}.insteadOf`, 'https://github.com/QuantumNous/new-api.git'], repo);

  writeFileSync(join(repo, 'shared.txt'), 'base\n');
  commit(repo, 'base');
  git(['push', '-u', 'origin', 'main'], repo);
  git(['push', '-u', 'upstream', 'main'], repo);
  git(['switch', '-c', 'dev'], repo);
  git(['push', '-u', 'origin', 'dev'], repo);

  git(['clone', upstreamBare, updater], fixtureRoot);
  git(['config', 'user.name', 'Upstream Test'], updater);
  git(['config', 'user.email', 'upstream-test@example.invalid'], updater);
});

after(() => {
  if (fixtureRoot) rmSync(fixtureRoot, { recursive: true, force: true });
});

test('creates feature branches from dev and refuses dirty worktrees', () => {
  const feature = managerRun(['feature', 'sample-feature']);
  assert.equal(feature.status, 0, feature.stderr);
  assert.equal(git(['branch', '--show-current']), 'feat/sample-feature');

  writeFileSync(join(repo, 'untracked.txt'), 'keep me\n');
  const dirty = managerRun(['feature', 'should-not-start']);
  assert.notEqual(dirty.status, 0);
  assert.match(dirty.stderr, /Working tree is not clean/);
  assert.equal(existsSync(join(repo, 'untracked.txt')), true);
  rmSync(join(repo, 'untracked.txt'));
  git(['switch', 'dev']);
});

test('configures repository-local fork safety defaults', () => {
  const result = managerRun(['setup', 'dev']);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(git(['branch', '--show-current']), 'dev');
  assert.equal(git(['config', '--local', 'branch.dev.remote']), 'origin');
  assert.equal(git(['config', '--local', 'branch.dev.merge']), 'refs/heads/dev');
  assert.equal(git(['config', '--local', 'remote.pushDefault']), 'origin');
  assert.equal(git(['config', '--local', 'push.default']), 'simple');
  assert.equal(git(['config', '--local', 'branch.dev.pushRemote']), 'origin');
  assert.equal(git(['config', '--local', 'branch.main.pushRemote']), 'origin');
  assert.equal(git(['config', '--local', 'remote.upstream.pushurl']), 'disabled://push-to-upstream-disabled');
  assert.equal(git(['config', '--local', 'pull.ff']), 'only');
  assert.equal(git(['config', '--local', 'rerere.enabled']), 'true');
});

test('resets an incorrectly configured dev to track the fork remote', () => {
  git(['config', '--local', 'branch.dev.remote', 'upstream']);
  git(['config', '--local', 'branch.dev.merge', 'refs/heads/main']);
  const result = managerRun(['setup', 'dev']);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(git(['config', '--local', 'branch.dev.remote']), 'origin');
  assert.equal(git(['config', '--local', 'branch.dev.merge']), 'refs/heads/dev');
});

test('creates dev from upstream without tracking upstream when no fork dev exists', () => {
  git(['switch', 'main']);
  git(['push', 'origin', '--delete', 'dev']);
  git(['update-ref', '-d', 'refs/remotes/origin/dev']);
  git(['branch', '-D', 'dev']);

  const result = managerRun(['setup', 'dev']);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(git(['branch', '--show-current']), 'dev');
  assert.equal(git(['rev-parse', 'dev']), git(['rev-parse', 'upstream/main']));
  const tracking = spawnSync('git', ['config', '--local', '--get', 'branch.dev.remote'], {
    cwd: repo,
    encoding: 'utf8',
    windowsHide: true,
  });
  assert.notEqual(tracking.status, 0, 'new dev must not track upstream/main');
  git(['push', '-u', 'origin', 'dev']);
});

test('still supports sync on the legacy dev branch', () => {
  const result = managerRun(['sync']);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /already contains the latest fetched upstream\/main/);
  assert.equal(git(['branch', '--show-current']), 'dev');
});

test('defaults to one persistent branch and protects unpublished or dirty work', () => {
  const preparedBase = git(['rev-parse', 'dev']);
  const result = managerRun(['setup']);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(git(['branch', '--show-current']), 'feat/custom-development');
  assert.equal(git(['rev-parse', 'HEAD']), preparedBase);
  git(['push', '-u', 'origin', 'feat/custom-development']);

  writeFileSync(join(repo, 'custom-only.txt'), 'keep all custom features\n');
  const dirty = managerRun(['sync']);
  assert.equal(dirty.status, 1);
  assert.match(dirty.stderr, /Working tree is not clean/);
  assert.equal(existsSync(join(repo, 'custom-only.txt')), true);
  commit(repo, 'add custom feature');

  const unpublished = managerRun(['sync']);
  assert.equal(unpublished.status, 1, unpublished.stderr);
  assert.match(unpublished.stderr, /differs from origin\/feat\/custom-development/);
  assert.equal(git(['branch', '--show-current']), 'feat/custom-development');
  git(['push', 'origin', 'feat/custom-development']);

  const resumed = managerRun(['setup']);
  assert.equal(resumed.status, 0, resumed.stderr);
  assert.equal(git(['config', '--local', 'branch.feat/custom-development.remote']), 'origin');
  assert.equal(git(['config', '--local', 'branch.feat/custom-development.merge']), 'refs/heads/feat/custom-development');
});

test('syncs the fixed branch, preserves custom history, detects repeats and conflicts', () => {
  const developmentBranch = 'feat/custom-development';
  const customHead = git(['rev-parse', developmentBranch]);
  writeFileSync(join(updater, 'upstream-only.txt'), 'official addition\n');
  commit(updater, 'official change');
  git(['push', 'origin', 'main'], updater);

  const successfulSync = managerRun(['sync']);
  assert.equal(successfulSync.status, 0, successfulSync.stderr);
  const syncBranch = git(['branch', '--show-current']);
  assert.match(syncBranch, /^sync\/upstream-/);
  assert.equal(existsSync(join(repo, '.git', 'MERGE_HEAD')), true);
  assert.equal(existsSync(join(repo, 'upstream-only.txt')), true);
  assert.equal(existsSync(join(repo, 'custom-only.txt')), true);
  assert.equal(git(['rev-parse', developmentBranch]), customHead);
  const upstreamPush = spawnSync('git', ['push', '--dry-run', 'upstream', 'HEAD:refs/heads/fork-test-protection'], {
    cwd: repo,
    encoding: 'utf8',
    windowsHide: true,
  });
  assert.notEqual(upstreamPush.status, 0, 'the official upstream push must be blocked locally');
  assert.match(upstreamPush.stderr, /disabled/i);

  commit(repo, 'chore: sync upstream main');
  git(['merge-base', '--is-ancestor', 'upstream/main', 'HEAD']);
  git(['merge-base', '--is-ancestor', customHead, 'HEAD']);
  git(['switch', developmentBranch]);
  git(['merge', '--ff-only', syncBranch]);
  git(['push', 'origin', developmentBranch]);

  const repeatSync = managerRun(['sync']);
  assert.equal(repeatSync.status, 0, repeatSync.stderr);
  assert.match(repeatSync.stdout, /already contains the latest fetched upstream\/main/);
  assert.equal(git(['branch', '--show-current']), developmentBranch);

  writeFileSync(join(repo, 'shared.txt'), 'fork version\n');
  commit(repo, 'fork change');
  git(['push', 'origin', developmentBranch]);
  const beforeConflict = git(['rev-parse', developmentBranch]);
  writeFileSync(join(updater, 'shared.txt'), 'official version\n');
  commit(updater, 'conflicting official change');
  git(['push', 'origin', 'main'], updater);

  const conflictingSync = managerRun(['sync']);
  assert.equal(conflictingSync.status, 2, conflictingSync.stderr);
  assert.match(git(['status', '--short']), /UU shared\.txt/);
  assert.equal(existsSync(join(repo, '.git', 'MERGE_HEAD')), true);
  assert.equal(git(['rev-parse', developmentBranch]), beforeConflict);
  git(['merge', '--abort']);
  git(['switch', developmentBranch]);
});
