#!/usr/bin/env node
'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');
const crypto = require('crypto');

const serviceName = '1cat-tunnel-client.service';
const packageRoot = path.resolve(__dirname, '..');
const binaryPath = path.join(packageRoot, 'dist', 'tunnel-client-linux-amd64');
const packageVersion = require(path.join(packageRoot, 'package.json')).version;

function warn(message) {
  console.warn('1cattunnel service: ' + message);
}

function run(command, args) {
  return spawnSync(command, args, { encoding: 'utf8', windowsHide: true, timeout: 30000 });
}

function validateUserName(value) {
  const userName = String(value || '').trim();
  return /^[A-Za-z_][A-Za-z0-9_.-]*\$?$/.test(userName) ? userName : '';
}

function lookupUser(userName) {
  const result = run('getent', ['passwd', userName]);
  if (result.status !== 0) {
    return null;
  }
  const fields = String(result.stdout || '').trim().split(':');
  if (fields.length < 7) {
    return null;
  }
  const uid = Number(fields[2]);
  const gid = Number(fields[3]);
  if (!Number.isInteger(uid) || !Number.isInteger(gid) || !path.isAbsolute(fields[5])) {
    return null;
  }
  return { name: fields[0], uid: uid, gid: gid, home: fields[5] };
}

function targetUser() {
  const current = os.userInfo();
  if (typeof process.getuid === 'function' && process.getuid() === 0) {
    const existing = run('systemctl', ['show', serviceName, '-p', 'FragmentPath', '--value']);
    if (existing.status === 0 && String(existing.stdout || '').trim()) {
      const owner = run('systemctl', ['show', serviceName, '-p', 'User', '--value']);
      const resolved = lookupUser(validateUserName(String(owner.stdout || '').trim()) || 'root');
      if (owner.status === 0 && resolved) return resolved;
    }
  }
  const sudoUser = validateUserName(process.env.SUDO_USER);
  if (typeof process.getuid === 'function' && process.getuid() === 0 && sudoUser && sudoUser !== 'root') {
    const resolved = lookupUser(sudoUser);
    if (!resolved) {
      throw new Error('无法解析 sudo 安装用户 ' + sudoUser);
    }
    return resolved;
  }
  return lookupUser(validateUserName(current.username)) || {
    name: current.username,
    uid: current.uid,
    gid: current.gid,
    home: current.homedir,
  };
}

function systemdQuote(value, expandEnv = true) {
  let escaped = String(value).replace(/%/g, '%%').replace(/\\/g, '\\\\').replace(/"/g, '\\"');
  if (expandEnv) escaped = escaped.replace(/\$/g, '$$$$');
  if (/[\r\n\0]/.test(escaped)) throw new Error('服务路径包含无效字符');
  return '"' + escaped + '"';
}

function workingDirectory(value) {
  const directory = String(value);
  if (!path.isAbsolute(directory) || directory !== directory.trim() || /[\r\n\0]/.test(directory)) throw new Error('服务工作目录无效');
  // Unlike ExecStart, this single-path directive does not strip quotes.
  return directory.replace(/%/g, '%%');
}

function buildServiceUnit(options) {
  const lines = [
    '[Unit]',
    'Description=1Cat Tunnel Client',
    'Wants=network-online.target',
    'After=network-online.target',
    '',
    '[Service]',
    'Type=simple',
  ];
  if (options.systemService) {
    lines.push('User=' + options.user.name);
  }
  lines.push(
    'WorkingDirectory=' + workingDirectory(options.configDir),
    'Environment=' + systemdQuote('HOME=' + options.user.home, false),
    'ExecStart=' + systemdQuote(options.runtimeBinary || binaryPath) + ' -managed -service-mode -pause-on-exit=false -config ' + systemdQuote(options.configPath),
    'Restart=always',
    'RestartSec=5',
    'UMask=0077',
    'NoNewPrivileges=true',
  );
  if (options.systemService) {
    lines.push(
      'PrivateTmp=true',
      'ProtectSystem=strict',
      'ProtectHome=read-only',
      'ReadWritePaths=' + systemdQuote(options.configDir, false),
    );
  }
  lines.push('', '[Install]', 'WantedBy=' + (options.systemService ? 'multi-user.target' : 'default.target'), '');
  return lines.join('\n');
}

function assertTrustedPath(filePath) {
  const uid = process.getuid();
  for (let current = path.resolve(filePath); ; current = path.dirname(current)) {
    let stat;
    try { stat = fs.lstatSync(current); } catch (error) { if (error.code !== 'ENOENT') throw error; }
    if (stat) {
      if (stat.isSymbolicLink() || ![0, uid].includes(stat.uid) || (stat.mode & 0o022)) throw new Error('不安全的服务安装路径：' + current);
    }
    if (current === path.dirname(current)) break;
  }
}

function writeTrusted(filePath, contents, mode) {
  assertTrustedPath(filePath);
  const staged = filePath + '.next-' + crypto.randomBytes(12).toString('hex');
  const fd = fs.openSync(staged, fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_NOFOLLOW, mode);
  try {
    fs.writeFileSync(fd, contents);
    fs.fchmodSync(fd, mode);
    fs.fsyncSync(fd);
  } finally { fs.closeSync(fd); }
  fs.renameSync(staged, filePath);
}

function mkdirTrusted(directory, mode) {
  assertTrustedPath(directory);
  const parent = path.dirname(directory);
  if (!fs.existsSync(parent)) mkdirTrusted(parent, 0o755);
  if (!fs.existsSync(directory)) fs.mkdirSync(directory, { mode });
  // Explicit modes on root-owned runtime paths survive restrictive sudo umasks.
  fs.chmodSync(directory, mode);
}

function runAsOwner(user, command, args) {
  const options = { encoding: 'utf8', timeout: 30000, windowsHide: true,
    env: { PATH: '/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin', HOME: user.home, USER: user.name, LOGNAME: user.name } };
  // runuser resets supplementary groups too; uid/gid spawn options alone do not.
  const result = process.getuid() === 0 && user.uid !== 0
    ? spawnSync('/usr/sbin/runuser', ['-u', user.name, '--', command].concat(args), options)
    : spawnSync(command, args, options);
  if (result.status !== 0) throw new Error(String(result.stderr || result.error || '无法以服务所属账户操作配置').trim());
  return result.stdout;
}

function configFromUnit(text) {
  const line = String(text).split(/\r?\n/).find(value => /^ExecStart=/.test(value));
  if (!line) return '';
  const match = line.match(/(?:^|\s)--?config(?:=|\s+)(?:"((?:[^"\\]|\\.)*)"|([^\s]+))/);
  if (!match) return '';
  const value = (match[1] || match[2]).replace(/\\([\\"])/g, '$1').replace(/%%/g, '%').replace(/\$\$/g, '$');
  return path.isAbsolute(value) ? value : '';
}

function userConfigRoot(user) {
  return user.name === os.userInfo().username && path.isAbsolute(process.env.XDG_CONFIG_HOME || '')
    ? process.env.XDG_CONFIG_HOME : path.join(user.home, '.config');
}

function serviceUnitPath(user, systemService) {
  return systemService ? path.join('/etc/systemd/system', serviceName)
    : path.join(userConfigRoot(user), 'systemd', 'user', serviceName);
}

function previousUnitPath(user, systemService) {
  const result = run('systemctl', (systemService ? [] : ['--user']).concat(['show', serviceName, '-p', 'FragmentPath', '--value']));
  const fragment = String(result.stdout || '').trim();
  return result.status === 0 && path.isAbsolute(fragment) && fs.existsSync(fragment)
    ? fragment : serviceUnitPath(user, systemService);
}

function existingConfig(user, systemService) {
  const unitPath = previousUnitPath(user, systemService);
  assertTrustedPath(unitPath);
  if (!fs.existsSync(unitPath)) return '';
  const configPath = configFromUnit(fs.readFileSync(unitPath, 'utf8'));
  if (!configPath) throw new Error('旧服务未指定可识别的绝对配置路径，已保留旧服务，请先确认其 -config 参数');
  if (!fs.existsSync(configPath)) throw new Error('旧服务的配置文件不存在，已保留旧服务：' + configPath);
  return configPath;
}

function ensureClientFiles(user, existingPath) {
  const xdgRoot = userConfigRoot(user);
  const configPath = existingPath || path.join(xdgRoot, '1cat-tunnel', 'client-linux.json');
  const configDir = path.dirname(configPath);
  runAsOwner(user, binaryPath, ['-initialize-config', '-config', configPath]);
  return { configDir: configDir, configPath: configPath };
}

function installSystemdService(user, files) {
  const systemService = typeof process.getuid === 'function' && process.getuid() === 0;
  const runtimeRoot = systemService ? '/usr/local/lib/1cat-tunnel'
    : path.join(user.home, '.local', 'share', '1cat-tunnel');
  const releaseDir = path.join(runtimeRoot, 'releases', packageVersion);
  assertTrustedPath(releaseDir);
  mkdirTrusted(runtimeRoot, 0o755);
  mkdirTrusted(path.join(runtimeRoot, 'releases'), 0o755);
  mkdirTrusted(releaseDir, 0o755);
  const runtimeBinary = path.join(releaseDir, 'tunnel-client');
  writeTrusted(runtimeBinary, fs.readFileSync(binaryPath), 0o755);
  const unit = buildServiceUnit({
    configDir: files.configDir,
    configPath: files.configPath,
    user: user,
    systemService: systemService,
    runtimeBinary: runtimeBinary,
  });
  let unitPath;
  let systemctlArgs;

  if (systemService) {
    unitPath = path.join('/etc/systemd/system', serviceName);
    systemctlArgs = [];
  } else {
    unitPath = serviceUnitPath(user, false);
    assertTrustedPath(path.dirname(unitPath));
    fs.mkdirSync(path.dirname(unitPath), { recursive: true, mode: 0o700 });
    systemctlArgs = ['--user'];
  }

  const previousPath = previousUnitPath(user, systemService);
  assertTrustedPath(previousPath);
  assertTrustedPath(unitPath);
  const oldUnit = fs.existsSync(previousPath) ? fs.readFileSync(previousPath, 'utf8') : '';
  const wasActive = run('systemctl', systemctlArgs.concat(['is-active', '--quiet', serviceName])).status === 0;
  const wasEnabled = run('systemctl', systemctlArgs.concat(['is-enabled', '--quiet', serviceName])).status === 0;
  const backupDir = path.join(runtimeRoot, 'backups', new Date().toISOString().replace(/[:.]/g, '-') + '-' + process.pid);
  assertTrustedPath(backupDir);
  mkdirTrusted(backupDir, 0o700);
  if (oldUnit) fs.writeFileSync(path.join(backupDir, serviceName), oldUnit, { mode: 0o600 });
  const savedConfigText = runAsOwner(user, '/bin/cat', ['--', files.configPath]);
  writeTrusted(path.join(backupDir, 'client-linux.json'), savedConfigText, 0o600);
  // Preserve the previous live executable so rollback does not depend on npm.
  let rollbackUnit = oldUnit;
  if (oldUnit && wasActive) {
    const pidResult = run('systemctl', systemctlArgs.concat(['show', serviceName, '-p', 'MainPID', '--value']));
    const pid = String(pidResult.stdout || '').trim();
    if (/^[1-9][0-9]*$/.test(pid)) {
      const processExe = '/proc/' + pid + '/exe';
      const executableName = path.basename(fs.readlinkSync(processExe)).replace(/ \(deleted\)$/, '');
      if (/^(tunnel-client|1cat-tunnel-client)/.test(executableName)) {
        const retainedDir = path.join(runtimeRoot, 'releases', 'rollback-' + path.basename(backupDir));
        mkdirTrusted(retainedDir, 0o755);
        const retained = path.join(retainedDir, 'tunnel-client');
        fs.copyFileSync(processExe, retained);
        fs.chmodSync(retained, 0o755);
        rollbackUnit = oldUnit.replace(/^ExecStart=(?:"(?:[^"\\]|\\.)*"|\S+)/m, 'ExecStart=' + systemdQuote(retained));
      }
    }
  }
  if (rollbackUnit) fs.writeFileSync(path.join(backupDir, 'rollback.service'), rollbackUnit, { mode: 0o600 });
  const candidate = path.join(backupDir, 'candidate.service');
  fs.writeFileSync(candidate, unit, { mode: 0o600 });
  const verify = run('systemd-analyze', systemctlArgs.concat(['verify', candidate]));
  if (verify.status !== 0 && (!verify.error || verify.error.code !== 'ENOENT')) {
    throw new Error('服务文件校验失败，已保留旧服务：' + String(verify.stderr || verify.error || 'systemd-analyze verify'));
  }
  function checked(args) {
    const result = run('systemctl', systemctlArgs.concat(args));
    if (result.status !== 0) throw new Error(String(result.stderr || result.stdout || args.join(' ')).trim());
  }
  try {
    writeTrusted(unitPath, unit, 0o644);
    checked(['daemon-reload']);
    checked(['enable', serviceName]);
    checked(['restart', serviceName]);
    checked(['is-active', '--quiet', serviceName]);
    let healthy = false;
    for (let attempt = 0; attempt < 20; attempt += 1) {
      try {
        const health = JSON.parse(runAsOwner(user, runtimeBinary, ['-status', '-config', files.configPath]));
        healthy = health.ok === true && health.version === '1Cat-Tunnel-' + packageVersion;
      } catch (_) {}
      if (healthy) break;
      Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 250);
    }
    if (!healthy) throw new Error('新服务管理接口认证健康检查失败');
  } catch (error) {
    if (rollbackUnit) {
      writeTrusted(unitPath, rollbackUnit, 0o644);
      run('systemctl', systemctlArgs.concat(['daemon-reload']));
      if (wasActive) run('systemctl', systemctlArgs.concat(['restart', serviceName]));
      else run('systemctl', systemctlArgs.concat(['stop', serviceName]));
      if (!wasEnabled) run('systemctl', systemctlArgs.concat(['disable', serviceName]));
    } else {
      run('systemctl', systemctlArgs.concat(['disable', '--now', serviceName]));
      if (fs.existsSync(unitPath)) fs.unlinkSync(unitPath);
      run('systemctl', systemctlArgs.concat(['daemon-reload']));
    }
    throw error;
  }
  console.log('1cattunnel: 已注册并启动 ' + (systemService ? '系统级' : '用户级') + ' systemd 服务 ' + serviceName);
  const savedConfig = JSON.parse(savedConfigText);
  console.log('1cattunnel: 本机管理页面 http://' + (savedConfig.web_listen_addr || savedConfig.block_ip_api_listen_addr || '127.0.0.1:51888') + '/');
  console.log('1cattunnel: 升级备份 ' + backupDir);
  if (!systemService) {
    console.log('1cattunnel: 用户级服务随登录启动；无人登录运行需管理员明确启用 loginctl enable-linger。');
  }
}

function main(args = process.argv.slice(2)) {
  if (process.platform !== 'linux') throw new Error('service 子命令仅适用于 Linux systemd');
  const action = args[0];
  if (!['install', 'upgrade', 'stop', 'uninstall'].includes(action)) throw new Error('请指定 service install / upgrade / stop / uninstall');
  if (action !== 'stop' && !args.includes('--yes')) throw new Error('此操作会改变独立后台服务。确认后请加 --yes；配置及备份将保留。');
  if (!fs.existsSync('/run/systemd/system')) throw new Error('当前系统未运行 systemd');
  const user = targetUser();
  const system = process.getuid() === 0;
  const scope = system ? [] : ['--user'];
  if (action === 'stop' || action === 'uninstall') {
    const unitPath = serviceUnitPath(user, system);
    assertTrustedPath(unitPath);
    if (action === 'uninstall' && previousUnitPath(user, system) !== unitPath) throw new Error('旧服务位于自定义目录，请先人工确认其服务文件位置；没有删除任何文件。');
    const result = run('systemctl', scope.concat(action === 'stop' ? ['stop', serviceName] : ['disable', '--now', serviceName]));
    if (result.status !== 0) throw new Error(String(result.stderr || result.error));
    if (action === 'uninstall') {
      if (fs.existsSync(unitPath)) fs.unlinkSync(unitPath);
      const reload = run('systemctl', scope.concat(['daemon-reload']));
      if (reload.status !== 0) throw new Error(String(reload.stderr || reload.error));
    }
    console.log('1cattunnel: 服务已停止' + (action === 'uninstall' ? '并移除；配置、历史二进制和备份保留，不会再自动连接。' : '。'));
    return;
  }
  const files = ensureClientFiles(user, existingConfig(user, system));
  installSystemdService(user, files);
  console.log('请用服务所属账户运行 1cattunnel panel 获取登录链接。');
}

module.exports = { main, buildServiceUnit, systemdQuote, validateUserName, configFromUnit, ensureClientFiles, installSystemdService, serviceUnitPath, existingConfig, assertTrustedPath };

if (require.main === module) {
  main();
}
