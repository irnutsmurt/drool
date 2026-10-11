// Lists the npm packages bundled into the web UI — package-lock.json entries
// that aren't development-only — with their license files, for
// THIRD_PARTY_NOTICES.md (packaging/release/relkit notices). Run in web/
// after `npm ci`: node npm-licenses.mjs . > web-packages.json
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

const dir = process.argv[2] ?? ".";
const lock = JSON.parse(readFileSync(join(dir, "package-lock.json"), "utf8"));
const out = [];
for (const [path, p] of Object.entries(lock.packages ?? {})) {
  if (!path || p.dev) continue;
  const pkgDir = join(dir, path);
  const files = existsSync(pkgDir)
    ? readdirSync(pkgDir).filter((f) => /^(licen[cs]e|copying|notice)([.-].*)?$/i.test(f)).sort()
    : [];
  out.push({
    name: path.replace(/^.*node_modules\//, ""),
    version: p.version ?? "",
    license: p.license ?? "",
    files: files.map((f) => ({ name: f, text: readFileSync(join(pkgDir, f), "utf8") })),
  });
}
out.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : a.version < b.version ? -1 : a.version > b.version ? 1 : 0));
process.stdout.write(JSON.stringify(out, null, 1) + "\n");
