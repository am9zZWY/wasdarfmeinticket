// Puts the built app (web/dist) on the branch that GitHub Pages serves, as
// a single commit without history: the branch only ever holds the current
// build. Nothing is pushed and the working tree is not touched.
//
//   npm run build && npm run branch && git push --force origin dist/app
import { execFileSync } from 'node:child_process';
import { existsSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const branch = 'dist/app';

const web = join(dirname(fileURLToPath(import.meta.url)), '..');
const output = join(web, 'dist');
if (!existsSync(join(output, 'index.html')) || !existsSync(join(output, 'engine.wasm'))) {
  console.error('web/dist is incomplete: run "npm run build" first');
  process.exit(1);
}

const git = (cwd, env, ...args) => execFileSync('git', args, { cwd, env: { ...process.env, ...env }, encoding: 'utf8' }).trim();
const gitDirectory = git(web, {}, 'rev-parse', '--absolute-git-dir');
const source = git(web, {}, 'rev-parse', '--short', 'HEAD');

// GitHub Pages would otherwise run the files through Jekyll.
writeFileSync(join(output, '.nojekyll'), '');

// A separate index keeps the files that are staged in the repository as
// they are.
const scratch = mkdtempSync(join(tmpdir(), 'wasdarfmeinticket-'));
try {
  const inOutput = { GIT_DIR: gitDirectory, GIT_WORK_TREE: output, GIT_INDEX_FILE: join(scratch, 'index') };
  git(output, inOutput, 'add', '--all', '--force', '.');
  const tree = git(output, inOutput, 'write-tree');
  const commit = git(output, inOutput, 'commit-tree', tree, '-m', `build: app built from ${source}`);
  git(web, {}, 'update-ref', `refs/heads/${branch}`, commit);
  console.log(`branch ${branch} is now ${commit.slice(0, 7)}, built from ${source}`);
} finally {
  rmSync(scratch, { recursive: true, force: true });
}
