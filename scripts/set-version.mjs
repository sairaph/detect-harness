import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const semver = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/;

function readJSON(relative) {
  return JSON.parse(fs.readFileSync(path.join(root, relative), "utf8"));
}

function writeJSON(relative, value) {
  fs.writeFileSync(path.join(root, relative), `${JSON.stringify(value, null, 2)}\n`);
}

function manifestVersions() {
  const python = fs.readFileSync(path.join(root, "wrappers/python/pyproject.toml"), "utf8");
  const rust = fs.readFileSync(path.join(root, "wrappers/rust/Cargo.toml"), "utf8");
  const rustLock = fs.readFileSync(path.join(root, "wrappers/rust/Cargo.lock"), "utf8");
  return {
    root: readJSON("package.json").version,
    node: readJSON("wrappers/node/package.json").version,
    nodeLock: readJSON("wrappers/node/package-lock.json").version,
    nodeLockPackage: readJSON("wrappers/node/package-lock.json").packages[""].version,
    python: python.match(/^version = "([^"]+)"$/m)?.[1],
    rust: rust.match(/^version = "([^"]+)"$/m)?.[1],
    rustLock: rustLock.match(/name = "detect-harness"\nversion = "([^"]+)"/)?.[1],
  };
}

function assertSynchronized() {
  const versions = manifestVersions();
  if (!semver.test(versions.root)) throw new Error(`invalid root version: ${versions.root}`);
  for (const [manifest, version] of Object.entries(versions)) {
    if (version !== versions.root) {
      throw new Error(`${manifest} version ${version ?? "missing"} does not match ${versions.root}`);
    }
  }
  process.stdout.write(`${versions.root}\n`);
}

function replaceVersion(relative, pattern, replacement) {
  const file = path.join(root, relative);
  const source = fs.readFileSync(file, "utf8");
  const output = source.replace(pattern, replacement);
  if (source === output) throw new Error(`could not update version in ${relative}`);
  fs.writeFileSync(file, output);
}

if (process.argv[2] === "--check") {
  assertSynchronized();
  process.exit(0);
}

const version = process.argv[2];
if (!version || !semver.test(version)) {
  throw new Error("usage: npm run version:set -- <semver>");
}

const project = readJSON("package.json");
project.version = version;
writeJSON("package.json", project);

const node = readJSON("wrappers/node/package.json");
node.version = version;
writeJSON("wrappers/node/package.json", node);

const nodeLock = readJSON("wrappers/node/package-lock.json");
nodeLock.version = version;
nodeLock.packages[""].version = version;
writeJSON("wrappers/node/package-lock.json", nodeLock);

replaceVersion("wrappers/python/pyproject.toml", /^version = "[^"]+"$/m, `version = "${version}"`);
replaceVersion("wrappers/rust/Cargo.toml", /^version = "[^"]+"$/m, `version = "${version}"`);
replaceVersion(
  "wrappers/rust/Cargo.lock",
  /(name = "detect-harness"\nversion = ")[^"]+("\n)/,
  `$1${version}$2`,
);

assertSynchronized();
