// Builds the Go code as WebAssembly into public/engine.wasm and copies the
// loader that belongs to the installed Go version.
import { execFileSync } from 'node:child_process';
import { copyFileSync, mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const web = join(dirname(fileURLToPath(import.meta.url)), '..');
const repository = join(web, '..');

execFileSync(
  'go',
  ['build', '-trimpath', '-ldflags=-s -w', '-o', join(web, 'public', 'engine.wasm'), './cmd/wasm'],
  { cwd: repository, env: { ...process.env, GOOS: 'js', GOARCH: 'wasm' }, stdio: 'inherit' },
);

const goRoot = execFileSync('go', ['env', 'GOROOT'], { encoding: 'utf8' }).trim();
mkdirSync(join(web, 'src', 'generated'), { recursive: true });
copyFileSync(join(goRoot, 'lib', 'wasm', 'wasm_exec.js'), join(web, 'src', 'generated', 'wasm_exec.js'));
