#!/usr/bin/env node
import { spawnSync } from 'node:child_process';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const OFFICIAL_UPSTREAM = 'https://github.com/QuantumNous/new-api.git';
const DEVELOPMENT_BRANCH = 'feat/custom-development';

function gitResult(args, cwd = REPO_ROOT) {
  const result = spawnSync('git', args, {
    cwd,
    encoding: 'utf8',
    windowsHide: true,
  });

  if (result.error) {
    throw new Error(`Cannot run Git: ${result.error.message}`);
  }

  return {
    status: result.status ?? 1,
    stdout: result.stdout ?? '',
    stderr: result.stderr ?? '',
  };
}

function git(args, cwd = REPO_ROOT) {
  const result = spawnSync('git', args, {
    cwd,
    stdio: 'inherit',
    windowsHide: true,
  });

  if (result.error) {
    throw new Error(`Cannot run Git: ${result.error.message}`);
  }
  if (result.status !== 0) {
    throw new Error(`Git command failed: git ${args.join(' ')}`);
  }
}

function output(text) {
  process.stdout.write(`${text}\n`);
}

function requireRemote(name) {
  const result = gitResult(['remote', 'get-url', name]);
  if (result.status !== 0) {
    throw new Error(`Missing Git remote '${name}'. Add it, then retry.`);
  }
  return result.stdout.trim();
}

function isOfficialRemote(url) {
  return /^(https:\/\/github\.com\/quantumnous\/new-api(?:\.git)?|git@github\.com:quantumnous\/new-api(?:\.git)?|ssh:\/\/git@github\.com\/quantumnous\/new-api(?:\.git)?)$/i.test(url);
}

function requireForkOrigin() {
  const fetchUrl = requireRemote('origin');
  const pushResult = gitResult(['remote', 'get-url', '--push', 'origin']);
  const pushUrl = pushResult.status === 0 ? pushResult.stdout.trim() : fetchUrl;
  if (isOfficialRemote(fetchUrl) || isOfficialRemote(pushUrl)) {
    throw new Error("'origin' must use your own fork for both fetch and push; it must not point to the official repository.");
  }
}

function requireOfficialUpstream() {
  const configured = gitResult(['config', '--local', '--get', 'remote.upstream.url']);
  const upstream = configured.status === 0 ? configured.stdout.trim() : requireRemote('upstream');
  if (!isOfficialRemote(upstream)) {
    throw new Error("'upstream' does not point to the official repository. Review it before changing this remote.");
  }
}

function requireCleanWorktree() {
  const result = gitResult(['status', '--porcelain', '--untracked-files=normal']);
  if (result.status !== 0) {
    throw new Error('Cannot read Git status. Run this command from inside the repository.');
  }
  if (result.stdout.trim()) {
    throw new Error('Working tree is not clean. Commit or stash your changes before continuing.');
  }
}

function currentBranch() {
  const result = gitResult(['branch', '--show-current']);
  if (result.status !== 0 || !result.stdout.trim()) {
    throw new Error('Could not determine the current branch. Check out a branch first.');
  }
  return result.stdout.trim();
}

function branchExists(ref) {
  return gitResult(['show-ref', '--quiet', '--verify', ref]).status === 0;
}

function isAncestor(older, newer) {
  return gitResult(['merge-base', '--is-ancestor', older, newer]).status === 0;
}

function configureSafeDefaults() {
  git(['config', '--local', 'remote.pushDefault', 'origin']);
  git(['config', '--local', 'remote.upstream.pushurl', 'disabled://push-to-upstream-disabled']);
  git(['config', '--local', 'push.default', 'simple']);
  git(['config', '--local', 'branch.main.pushRemote', 'origin']);
  git(['config', '--local', 'branch.dev.pushRemote', 'origin']);
  git(['config', '--local', `branch.${DEVELOPMENT_BRANCH}.pushRemote`, 'origin']);
  git(['config', '--local', 'pull.ff', 'only']);
  git(['config', '--local', 'rerere.enabled', 'true']);
}

function setup(targetBranch = DEVELOPMENT_BRANCH) {
  if (targetBranch !== 'dev' && targetBranch !== DEVELOPMENT_BRANCH) {
    throw new Error(`Setup target must be '${DEVELOPMENT_BRANCH}' or the legacy 'dev' branch.`);
  }
  requireCleanWorktree();
  requireForkOrigin();

  const startingBranch = currentBranch();
  if (!['main', 'dev', DEVELOPMENT_BRANCH].includes(startingBranch)) {
    throw new Error(`Run setup from a clean main, dev, or ${DEVELOPMENT_BRANCH} branch.`);
  }

  const upstream = gitResult(['remote', 'get-url', 'upstream']);
  if (upstream.status !== 0) {
    git(['remote', 'add', 'upstream', OFFICIAL_UPSTREAM]);
    output('Added the official upstream remote.');
  }
  requireOfficialUpstream();
  git(['fetch', '--no-tags', 'origin']);
  git(['fetch', '--no-tags', 'upstream', 'main']);

  if (branchExists(`refs/heads/${targetBranch}`)) {
    if (startingBranch !== targetBranch) {
      git(['switch', targetBranch]);
    }
  } else if (branchExists(`refs/remotes/origin/${targetBranch}`)) {
    git(['switch', '--track', '-c', targetBranch, `origin/${targetBranch}`]);
  } else {
    let base = 'upstream/main';
    if (targetBranch !== 'dev' && branchExists('refs/heads/dev')) {
      base = 'dev';
    } else if (targetBranch !== 'dev' && branchExists('refs/remotes/origin/dev')) {
      base = 'origin/dev';
    }
    if (!branchExists(`refs/heads/${base}`) && !branchExists(`refs/remotes/${base}`)) {
      throw new Error(`Could not find setup base '${base}'.`);
    }
    git(['switch', '--no-track', '-c', targetBranch, base]);
  }

  if (branchExists(`refs/remotes/origin/${targetBranch}`)) {
    git(['branch', `--set-upstream-to=origin/${targetBranch}`, targetBranch]);
    output(`Ready on '${targetBranch}' tracking 'origin/${targetBranch}'.`);
  } else {
    output(`Ready on '${targetBranch}'. Push it once with: git push -u origin ${targetBranch}`);
  }
  configureSafeDefaults();
  output('Local Git safety defaults: push to origin, fast-forward-only pull, rerere enabled.');
}

function startFeature(slug) {
  if (!slug || !/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(slug)) {
    throw new Error('Usage: node fork/maintain.mjs feature <short-name>');
  }
  requireCleanWorktree();
  requireForkOrigin();
  if (currentBranch() !== 'dev') {
    throw new Error("Create feature branches from 'dev'. Switch to dev and update it first.");
  }

  const branch = `feat/${slug}`;
  const checked = gitResult(['check-ref-format', '--branch', branch]);
  if (checked.status !== 0 || branchExists(`refs/heads/${branch}`)) {
    throw new Error(`Branch '${branch}' is invalid or already exists.`);
  }

  git(['switch', '-c', branch, 'dev']);
  output(`Created '${branch}' from dev. Push it with: git push -u origin ${branch}`);
  output("Open a pull request from this branch into 'dev' when it is ready.");
}

function syncUpstream() {
  requireCleanWorktree();
  requireForkOrigin();
  requireOfficialUpstream();
  const targetBranch = currentBranch();
  if (targetBranch !== 'dev' && targetBranch !== DEVELOPMENT_BRANCH) {
    throw new Error(`Run upstream sync from '${DEVELOPMENT_BRANCH}' (or legacy 'dev').`);
  }

  configureSafeDefaults();
  git(['fetch', '--no-tags', 'upstream', 'main']);
  git(['fetch', '--no-tags', 'origin', targetBranch]);

  const localHead = gitResult(['rev-parse', `refs/heads/${targetBranch}`]);
  const remoteHead = gitResult(['rev-parse', `refs/remotes/origin/${targetBranch}`]);
  const upstreamMain = gitResult(['rev-parse', 'refs/remotes/upstream/main']);
  if (localHead.status !== 0 || remoteHead.status !== 0 || upstreamMain.status !== 0) {
    throw new Error(`Could not find the development branch or upstream/main. Push with 'git push -u origin ${targetBranch}', then retry.`);
  }
  if (localHead.stdout.trim() !== remoteHead.stdout.trim()) {
    throw new Error(`Local ${targetBranch} differs from origin/${targetBranch}. Review local and remote commits, pull with --ff-only or push as appropriate, then retry.`);
  }
  if (isAncestor('upstream/main', targetBranch)) {
    output(`'${targetBranch}' already contains the latest fetched upstream/main. No sync branch was created.`);
    return;
  }

  const timestamp = new Date().toISOString().replace(/[-:.T]/g, '');
  const branch = `sync/upstream-${timestamp}`;
  if (branchExists(`refs/heads/${branch}`)) {
    throw new Error(`Branch '${branch}' already exists. Retry in a moment.`);
  }

  git(['switch', '-c', branch, targetBranch]);
  const merge = spawnSync('git', ['merge', '--no-ff', '--no-commit', 'upstream/main'], {
    cwd: REPO_ROOT,
    stdio: 'inherit',
    windowsHide: true,
  });
  if (merge.error) {
    throw new Error(`Cannot run Git merge: ${merge.error.message}`);
  }
  if (merge.status !== 0) {
    const conflicts = gitResult(['diff', '--name-only', '--diff-filter=U']);
    output(conflicts.stdout.trim() ? 'Upstream merge has conflicts. This sync branch is intentionally left in place.' : 'Git merge failed. Inspect the Git error above; this sync branch is left in place.');
    output('Resolve the reported issue, run the relevant tests, then git add and git commit the merge.');
    output(`If a merge is in progress, cancel it with git merge --abort, then git switch ${targetBranch}.`);
    process.exitCode = 2;
    return;
  }

  const mergeHead = gitResult(['rev-parse', '--verify', 'MERGE_HEAD']);
  if (mergeHead.status !== 0) {
    git(['switch', targetBranch]);
    output(`'${targetBranch}' already contains upstream/main. The unused sync branch was left in place.`);
    return;
  }

  output(`Prepared '${branch}' from '${targetBranch}' with upstream/main; no commit or push was made.`);
  output(`After tests, commit this merge, switch to ${targetBranch}, and run git merge --ff-only ${branch}.`);
  output(`If using a PR, target '${targetBranch}' and preserve the upstream history with a merge commit (do not squash or rebase).`);
}

function showStatus() {
  const branch = currentBranch();
  const status = gitResult(['status', '--short', '--branch']);
  output(`Branch: ${branch}`);
  output(`origin: ${gitResult(['remote', 'get-url', 'origin']).status === 0 ? 'configured' : 'missing'}`);
  output(`upstream: ${gitResult(['remote', 'get-url', 'upstream']).status === 0 ? 'configured' : 'missing'}`);
  if (status.status === 0) {
    output(status.stdout.trim() || 'Working tree: clean');
  }
  if (branchExists('refs/remotes/upstream/main')) {
    const relation = gitResult(['rev-list', '--left-right', '--count', `${branch}...upstream/main`]);
    if (relation.status === 0) {
      const [ahead, behind] = relation.stdout.trim().split(/\s+/);
      output(`Compared with upstream/main: ${ahead} local-only, ${behind} upstream-only commits.`);
    }
  }
}

function printUsage() {
  output('Fork maintenance commands:');
  output('  node fork/maintain.mjs setup');
  output('  node fork/maintain.mjs status');
  output('  node fork/maintain.mjs sync');
  output('');
  output(`Daily development stays on ${DEVELOPMENT_BRANCH}; no new feature branch is needed.`);
  output('Sync never commits or pushes for you. Legacy setup dev / feature <short-name> commands remain available.');
}

try {
  const [command, ...args] = process.argv.slice(2);
  switch (command) {
    case 'setup':
      setup(args[0]);
      break;
    case 'status':
      showStatus();
      break;
    case 'feature':
      startFeature(args[0]);
      break;
    case 'sync':
      syncUpstream();
      break;
    default:
      printUsage();
      if (command) process.exitCode = 1;
  }
} catch (error) {
  process.stderr.write(`Error: ${error.message}\n`);
  process.exitCode = 1;
}
