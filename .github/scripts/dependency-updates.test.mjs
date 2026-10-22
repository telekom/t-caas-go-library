// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, test } from 'node:test';
import { pathToFileURL } from 'node:url';

assert.ok(process.env.RENOVATE_PACKAGE_DIR, 'Set RENOVATE_PACKAGE_DIR to the pinned Renovate installation');
const load = relative => import(pathToFileURL(path.join(process.env.RENOVATE_PACKAGE_DIR, 'dist', relative)));
const { init } = await load('logger/index.js');
await init();
const { GlobalConfig } = await load('config/global.js');
const { extractPackageFile } = await load('modules/manager/custom/regex/index.js');
const { doAutoReplace } = await load('workers/repository/update/branch/auto-replace.js');
const { applyPackageRules } = await load('util/package-rules/index.js');
const config = JSON.parse(await fs.readFile('.github/renovate.json', 'utf8'));
const manager = config.customManagers[0];
const sandbox = await fs.mkdtemp(path.join(os.tmpdir(), 'dependency-updates-'));
GlobalConfig.set({ localDir: sandbox, cacheDir: sandbox, platform: 'local' });
after(() => fs.rm(sandbox, { recursive: true, force: true }));

test('extracts the complete version after a versioned Go module path', () => {
  const input = '# renovate: datasource=go depName=github.com/example/tool/v2\n' +
    'run: go install github.com/example/tool/v2/cmd/tool@v2.12.3\n';
  const result = extractPackageFile(input, 'fixture.yml', manager);
  assert.equal(result.deps.length, 1);
  assert.equal(result.deps[0].depName, 'github.com/example/tool/v2');
  assert.equal(result.deps[0].currentValue, 'v2.12.3');
});

test('keeps prereleases and envtest release extraction intact', () => {
  const input = '# renovate: datasource=github-releases depName=kubernetes-sigs/controller-tools ' +
    'extractVersion=^envtest-v(?<version>\\d+\\.\\d+\\.\\d+)$\nENVTEST_VERSION=1.37.0\n' +
    '# renovate: datasource=docker depName=ghcr.io/example/chart\nCHART_VERSION=0.5.0-rc.12\n';
  const { deps } = extractPackageFile(input, 'versions.env', manager);
  assert.equal(deps[0].currentValue, '1.37.0');
  assert.equal(new RegExp(deps[0].extractVersion).exec('envtest-v1.37.1').groups.version, '1.37.1');
  assert.equal(deps[1].currentValue, '0.5.0-rc.12');
});

test('the real updater changes an image tag and digest atomically, without altering other pins', async () => {
  const original = '# renovate: datasource=docker depName=alpine\n' +
    `IMAGE="alpine:3.22@sha256:${'a'.repeat(64)}"\nUNRELATED_VERSION=3.22\n`;
  const dep = extractPackageFile(original, 'fixture.env', manager).deps[0];
  const newDigest = `sha256:${'b'.repeat(64)}`;
  const updated = await doAutoReplace({
    ...manager, ...dep, manager: 'regex', depIndex: 0, packageFile: 'fixture.env',
    newValue: '3.24', newDigest,
  }, original, false);
  assert.equal(updated, original.replace(`alpine:3.22@${dep.currentDigest}`, `alpine:3.24@${newDigest}`));
  assert.equal(await fs.readFile(path.join(sandbox, 'fixture.env'), 'utf8'), updated);
});

test('package rules leave normal modules to Dependabot but enable Go patches and the OPA submodule', async () => {
  for (const [depType, packageName, datasource, enabled] of [
    ['require', 'k8s.io/client-go', 'go', false],
    ['golang', 'go', 'golang-version', true],
    ['indirect', 'github.com/open-policy-agent/frameworks/constraint', 'go', true],
  ]) {
    const resolved = await applyPackageRules({
      packageRules: config.packageRules, manager: 'gomod', depType,
      packageName, depName: packageName, datasource,
    }, 'dependency ownership test');
    assert.equal(resolved.enabled, enabled, `${depType} ${packageName}`);
    if (depType === 'golang') assert.equal(resolved.rangeStrategy, 'bump');
  }
});

test('chart version inputs remain tag-only instead of receiving image digests', async () => {
  const packageName = 'ghcr.io/telekom/charts/auth-operator';
  const resolved = await applyPackageRules({
    packageRules: config.packageRules, manager: 'regex',
    packageName, depName: packageName, datasource: 'docker',
  }, 'chart version input test');
  assert.equal(resolved.pinDigests, false);
  const original = `# renovate: datasource=docker depName=${packageName}\nAUTH_OPERATOR_VERSION=0.5.0-rc.12\n`;
  const dep = extractPackageFile(original, 'chart.env', manager).deps[0];
  const updated = await doAutoReplace({
    ...manager, ...dep, manager: 'regex', depIndex: 0, packageFile: 'chart.env', newValue: '0.5.1',
  }, original, false);
  assert.equal(updated, original.replace('0.5.0-rc.12', '0.5.1'));
});

test('the native Go datasource resolves the actual nested module, not the parent release', { timeout: 90000 }, async () => {
  const module = 'github.com/open-policy-agent/frameworks/constraint';
  const response = await fetch(`https://proxy.golang.org/${module}/@latest`, { signal: AbortSignal.timeout(30000) });
  assert.equal(response.status, 200);
  const latest = await response.json();
  const { getPkgReleases } = await load('modules/datasource/index.js');
  const result = await getPkgReleases({ datasource: 'go', packageName: module, versioning: 'semver' });
  assert.ok(result?.releases.some(release => release.version === latest.Version), 'Renovate must use the Go proxy module version');
});
