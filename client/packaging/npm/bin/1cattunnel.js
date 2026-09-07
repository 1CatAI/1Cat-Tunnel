#!/usr/bin/env node
'use strict';

// No downloads, installation hooks or automatic service registration.
const fs = require('fs');
const os = require('os');
const path = require('path');
const crypto = require('crypto');
const { spawnSync } = require('child_process');
const root = path.resolve(__dirname, '..');
const version = require('../package.json').version;
function fail(message) { console.error('1cattunnel: ' + message); process.exit(1); }
if (process.arch !== 'x64' || !['linux', 'win32'].includes(process.platform)) fail('目前支持 Windows / Linux x64');
const windows = process.platform === 'win32';
let binary = path.join(root, 'dist', windows ? 'tunnel-client-windows-amd64.exe' : 'tunnel-client-linux-amd64');
let args = process.argv.slice(2);
if (args.length === 1 && ['version', '--version', '-version', '-v'].includes(args[0])) {
  console.log('1Cat-Tunnel-' + version); process.exit(0);
}
if (args[0] === 'service') {
  try { require('../scripts/service').main(args.slice(1)); } catch (error) { fail(error.message); }
  process.exit(0);
}
if (args.length === 1 && ['help', '--help', '-h'].includes(args[0])) {
  console.log('1cattunnel                 启动本机管理面板，未配置时不会连接服务器');
  console.log('1cattunnel panel           获取本机账户专用登录链接（请勿分享）');
  console.log('1cattunnel status          查看实时状态和映射');
  console.log('1cattunnel service install --yes   明确启用 Linux 后台服务');
  console.log('1cattunnel service upgrade --yes   保留配置升级服务，失败回滚');
  console.log('1cattunnel service stop            停止后台连接');
  console.log('1cattunnel service uninstall --yes 停止并移除服务，保留配置和备份');
  console.log('安装 / 卸载 npm 包不会隐式改变独立服务。卸载前请先执行 service uninstall。');
  process.exit(0);
}
if (['panel', 'status'].includes(args[0])) args[0] = '-' + args[0];
if (!args.some(arg => /^--?config(?:=|$)/.test(arg))) {
  let config = '';
  if (!windows) {
    const installer = require('../scripts/service');
    for (const scope of [[], ['--user']]) {
      const result = spawnSync('systemctl', scope.concat(['show', '1cat-tunnel-client.service', '-p', 'FragmentPath']), { encoding: 'utf8', timeout: 1500 });
      const match = String(result.stdout || '').match(/^FragmentPath=(.+)$/m);
      if (!match || !path.isAbsolute(match[1])) continue;
      try { config = installer.configFromUnit(fs.readFileSync(match[1], 'utf8')); if (config) break; } catch (_) {}
    }
  }
  const configRoot = windows ? (process.env.APPDATA || path.join(os.homedir(), 'AppData', 'Roaming'))
    : (process.env.XDG_CONFIG_HOME || path.join(os.homedir(), '.config'));
  args.push('-config', config || path.join(configRoot, '1cat-tunnel', windows ? 'client-windows.json' : 'client-linux.json'));
}
try {
  if (windows) {
    const contents = fs.readFileSync(binary);
    const digest = data => crypto.createHash('sha256').update(data).digest('hex');
    const hash = digest(contents);
    const dir = path.join(process.env.LOCALAPPDATA || path.join(os.homedir(), 'AppData', 'Local'), '1cat-tunnel', 'releases', version + '-' + hash.slice(0, 16));
    fs.mkdirSync(dir, { recursive: true });
    const target = path.join(dir, path.basename(binary));
    if (fs.existsSync(target)) {
      if (fs.lstatSync(target).isSymbolicLink() || digest(fs.readFileSync(target)) !== hash) fail('缓存客户端校验失败，请移除该缓存文件后重试：' + target);
    } else {
      const staged = target + '.next-' + crypto.randomBytes(12).toString('hex');
      fs.writeFileSync(staged, contents, { flag: 'wx' });
      fs.renameSync(staged, target);
    }
    binary = target;
  } else { fs.accessSync(binary, fs.constants.X_OK); }
  const result = spawnSync(binary, args, { stdio: 'inherit', windowsHide: false });
  if (result.error) fail(result.error.message);
  process.exit(result.status === null ? 1 : result.status);
} catch (error) { fail(error.message); }
