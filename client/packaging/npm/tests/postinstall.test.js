'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const os = require('os');
const path = require('path');
const vm = require('vm');
const installer = require('../scripts/service');

test('existing unit preserves an explicit config path with spaces', () => {
  const unit = '[Service]\nExecStart="/old client/tunnel-client" -config "/home/user/old data/client.json"\n';
  assert.equal(installer.configFromUnit(unit), '/home/user/old data/client.json');
  assert.equal(installer.configFromUnit('ExecStart=/old/client -config /home/user/client.json'), '/home/user/client.json');
  assert.equal(installer.configFromUnit('ExecStart=/old/client -config relative.json'), '');
});

test('unit uses retained binary and escapes systemd expansion', () => {
  const unit = installer.buildServiceUnit({
    runtimeBinary: '/usr/local/lib/1cat-tunnel/releases/0.5.1/tunnel-client',
    configDir: '/home/test/100% $cache', configPath: '/home/test/100% $cache/client.json',
    user: { name: 'test', home: '/home/test' }, systemService: true,
  });
  assert.match(unit, /ExecStart="\/usr\/local\/lib\/1cat-tunnel\/releases\/0\.5\.1\/tunnel-client"/);
  assert.match(unit, /100%% \$\$cache/);
  assert.match(unit, /^WorkingDirectory=\/home\/test\/100%% \$cache$/m);
  assert.match(unit, /^ReadWritePaths="\/home\/test\/100%% \$cache"$/m);
  assert.throws(() => installer.systemdQuote('/tmp/a\nExecStart=bad'));
});

function fixture(t, failRestart, rejectUnit = false) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), '1cat-install-test-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const translate = p => path.join(root, String(p).replace(/^\//, ''));
  const calls = [];
  let restarts = 0;
  const virtualFS = {};
  for (const name of ['existsSync', 'readFileSync', 'writeFileSync', 'mkdirSync', 'chmodSync', 'chownSync', 'unlinkSync']) {
    virtualFS[name] = (p, ...args) => name === 'chownSync' ? undefined : fs[name](typeof p === 'number' ? p : translate(p), ...args);
  }
  for (const name of ['copyFileSync', 'renameSync']) virtualFS[name] = (a, b, ...args) => fs[name](translate(a), translate(b), ...args);
  virtualFS.readlinkSync = () => '/package/dist/tunnel-client-linux-amd64 (deleted)';
  virtualFS.constants = fs.constants;
  virtualFS.openSync = (p, ...args) => fs.openSync(translate(p), ...args);
  for (const name of ['fchmodSync', 'fsyncSync', 'closeSync']) virtualFS[name] = fs[name];
  virtualFS.lstatSync = p => { const stat = fs.lstatSync(translate(p)); return { uid: 0, mode: stat.isDirectory() ? 0o755 : 0o600, isSymbolicLink: () => stat.isSymbolicLink() }; };
  const mockProcess = { pid: 777, getuid: () => 0, platform: 'linux', env: {} };
  const context = {
    module: { exports: {} }, __dirname: '/package/scripts', console: { log() {}, warn() {} }, process: mockProcess,
    require(name) {
      if (name === 'fs') return virtualFS;
      if (name === 'crypto') return require('crypto');
      if (name === 'os') return { userInfo: () => ({ username: 'test' }), hostname: () => 'test-host' };
      if (name === 'path') return path.posix;
      if (name === '/package/package.json') return { version: '0.5.1' };
      if (name === 'child_process') return { spawnSync(command, args) {
        calls.push(args);
        if (command === '/bin/cat') return { status: 0, stdout: fs.readFileSync(translate(args[1]), 'utf8') };
        if (args.includes('/bin/cat')) return { status: 0, stdout: fs.readFileSync(translate(args[args.length - 1]), 'utf8') };
        if (args.includes('-status')) return { status: 0, stdout: '{"ok":true,"version":"1Cat-Tunnel-0.5.1"}' };
        if (command === 'systemd-analyze' && rejectUnit) return { status: 1, stderr: 'simulated invalid unit' };
        if (args.includes('MainPID')) return { status: 0, stdout: '123\n' };
        if (args.includes('restart')) {
          restarts += 1;
          if (failRestart && restarts === 1) return { status: 1, stderr: 'simulated failed service' };
        }
        return { status: 0, stdout: '' };
      } };
      throw new Error('Unexpected require: ' + name);
    },
  };
  function write(p, contents) { fs.mkdirSync(path.dirname(translate(p)), { recursive: true }); fs.writeFileSync(translate(p), contents); }
  write('/package/dist/tunnel-client-linux-amd64', 'new-client');
  write('/proc/123/exe', 'old-running-client');
  write('/etc/systemd/system/1cat-tunnel-client.service', '[Service]\nExecStart="/package/dist/tunnel-client-linux-amd64" -managed -config "/home/test/old data/client.json"\n');
  write('/home/test/old data/client.json', '{"token":"test-identity"}');
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../scripts/service.js'), 'utf8'), context);
  return { install: context.module.exports.installSystemdService, api: context.module.exports, process: mockProcess, calls, translate, write };
}

test('upgrade restarts an already active service and keeps the old executable', t => {
  const f = fixture(t, false);
  f.install({ name: 'test', home: '/home/test' }, { configDir: '/home/test/old data', configPath: '/home/test/old data/client.json' });
  assert.ok(f.calls.some(args => args[0] === 'restart'));
  assert.equal(fs.readFileSync(f.translate('/home/test/old data/client.json'), 'utf8'), '{"token":"test-identity"}');
  const releaseRoot = f.translate('/usr/local/lib/1cat-tunnel/releases');
  const previous = fs.readdirSync(releaseRoot).find(name => name.startsWith('rollback-'));
  assert.equal(fs.readFileSync(path.join(releaseRoot, previous, 'tunnel-client'), 'utf8'), 'old-running-client');
});

test('failed upgrade restores a unit pointing to the retained old executable', t => {
  const f = fixture(t, true);
  assert.throws(() => f.install({ name: 'test', home: '/home/test' }, { configDir: '/home/test/old data', configPath: '/home/test/old data/client.json' }), /simulated failed service/);
  const restored = fs.readFileSync(f.translate('/etc/systemd/system/1cat-tunnel-client.service'), 'utf8');
  assert.match(restored, /releases\/rollback-/);
  assert.match(restored, /-config "\/home\/test\/old data\/client.json"/);
  assert.equal(f.calls.filter(args => args[0] === 'restart').length, 2);
});

test('custom config directories do not relocate the user systemd unit', t => {
  const f = fixture(t, false);
  f.process.getuid = () => 1000;
  assert.equal(f.api.serviceUnitPath({ name: 'test', home: '/home/test' }, false), '/home/test/.config/systemd/user/1cat-tunnel-client.service');
  f.process.env.XDG_CONFIG_HOME = '/home/test/config-root';
  assert.equal(f.api.serviceUnitPath({ name: 'test', home: '/home/test' }, false), '/home/test/config-root/systemd/user/1cat-tunnel-client.service');
});

test('unrecognized or missing legacy config never gets silently replaced', t => {
  const f = fixture(t, false);
  const user = { name: 'test', home: '/home/test' };
  assert.equal(f.api.existingConfig(user, true), '/home/test/old data/client.json');
  f.write('/etc/systemd/system/1cat-tunnel-client.service', '[Service]\nExecStart=/old/client\n');
  assert.throws(() => f.api.existingConfig(user, true), /-config/);
  f.write('/etc/systemd/system/1cat-tunnel-client.service', '[Service]\nExecStart=/old/client -config /missing.json\n');
  assert.throws(() => f.api.existingConfig(user, true), /不存在/);
});

test('unit syntax preflight rejects a bad release before replacing the live service', t => {
  const f = fixture(t, false, true);
  const unitPath = f.translate('/etc/systemd/system/1cat-tunnel-client.service');
  const original = fs.readFileSync(unitPath, 'utf8');
  assert.throws(() => f.install({ name: 'test', home: '/home/test' }, { configDir: '/home/test/old data', configPath: '/home/test/old data/client.json' }), /simulated invalid unit/);
  assert.equal(fs.readFileSync(unitPath, 'utf8'), original);
  assert.equal(f.calls.filter(args => args.includes('restart')).length, 0);
});

test('root prepares customer config only after dropping to the service owner', t => {
  const f = fixture(t, false);
  const config = '/home/test/old data/client.json';
  const before = fs.readFileSync(f.translate(config), 'utf8');
  f.api.ensureClientFiles({ name: 'test', uid: 1000, gid: 1000, home: '/home/test' }, config);
  assert.ok(f.calls.some(args => args[0] === '-u' && args[1] === 'test' && args.includes('-initialize-config')));
  assert.equal(fs.readFileSync(f.translate(config), 'utf8'), before);
});

test('user service does not create ownership-remapping namespaces', () => {
  const unit = installer.buildServiceUnit({ runtimeBinary: '/home/test/client', configDir: '/home/test/data', configPath: '/home/test/data/client.json', user: { name: 'test', home: '/home/test' }, systemService: false });
  assert.doesNotMatch(unit, /^PrivateTmp=true$/m);
  assert.match(unit, /^NoNewPrivileges=true$/m);
  assert.match(unit, /-service-mode/);
});

test('npm metadata contains no automatic lifecycle scripts', () => {
  const metadata = require('../package.json');
  assert.equal(metadata.scripts, undefined);
  assert.equal(metadata.dependencies, undefined);
});
