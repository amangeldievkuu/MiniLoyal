// Fails the build when the ESM bundle exceeds the gzipped budget. @line/liff is
// a peer dependency and not part of the bundle.
import { readFileSync } from 'node:fs';
import { gzipSync } from 'node:zlib';

const LIMIT = 15 * 1024;
const file = new URL('../dist/index.js', import.meta.url);
const raw = readFileSync(file);
const gzipped = gzipSync(raw, { level: 9 }).length;
const kb = (n) => (n / 1024).toFixed(1) + ' kB';
console.log(
  `@miniloyal/sdk dist/index.js: ${kb(raw.length)} raw, ${kb(gzipped)} gzipped (budget ${kb(LIMIT)})`,
);
if (gzipped > LIMIT) {
  console.error('bundle budget exceeded');
  process.exit(1);
}
