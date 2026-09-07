'use strict';

(() => {
  const byID = id => document.getElementById(id);
  const csrf = document.querySelector('meta[name="csrf-token"]').content;
  let revision = Number(document.querySelector('meta[name="config-revision"]').content);
  const countInput = byID('mapping-count');
  const mappingList = byID('mapping-list');
  const rowTemplate = byID('mapping-template');
  const removedRows = [];
  let dirty = false;
  let saving = false;
  window.addEventListener('1cat-panel-logout', () => { dirty = false; });

  function showNotice(message, bad = false) {
    byID('notice').textContent = message;
    byID('notice').className = 'notice show' + (bad ? ' bad' : '');
  }

  function markDirty() {
    dirty = true;
    byID('unsaved').classList.remove('hidden');
  }

  function rows() {
    return Array.from(mappingList.querySelectorAll('.mapping-row'));
  }

  async function copyAddress(value) {
    try {
      if (navigator.clipboard) {
        await navigator.clipboard.writeText(value);
        return;
      }
    } catch (_) { /* Some HTTP/remote desktop contexts deny the Clipboard API. */ }
    const input = document.createElement('textarea');
    input.value = value;
    input.style.position = 'fixed';
    input.style.left = '-9999px';
    document.body.appendChild(input);
    input.select();
    const copied = document.execCommand('copy');
    input.remove();
    if (!copied) throw new Error('浏览器不允许复制，请选中公网地址手动复制');
  }

  function prepareRow(row) {
    if (row.querySelector('.copy-button')) return;
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'copy-button secondary';
    button.textContent = '复制公网地址';
    button.disabled = true;
    button.addEventListener('click', async () => {
      try {
        await copyAddress(button.dataset.address);
        showNotice('公网地址已复制');
      } catch (error) { showNotice(error.message, true); }
    });
    row.querySelector('.mapping-head').appendChild(button);
  }

  function syncRows() {
    const count = Number(countInput.value);
    if (!countInput.value || !Number.isInteger(count) || count < 1 || count > 20) {
      showNotice('端口数量请填写 1 到 20 的整数', true);
      return false;
    }
    let current = rows();
    while (current.length < count) {
      let row = removedRows.pop();
      if (!row) {
        row = rowTemplate.content.firstElementChild.cloneNode(true);
        const names = new Set(current.map(item => item.querySelector('.mapping-name').value.trim().toLowerCase()));
        let index = current.length + 1;
        while (names.has('port-' + index)) index += 1;
        row.querySelector('.mapping-name').value = 'port-' + index;
        row.querySelector('.mapping-port').value = 8080;
      }
      prepareRow(row);
      mappingList.appendChild(row);
      current.push(row);
    }
    while (current.length > count) {
      const row = current.pop();
      row.remove();
      removedRows.push(row);
    }
    current.forEach((row, index) => { row.querySelector('[data-role="index"]').textContent = index + 1; });
    return true;
  }

  async function requestJSON(path, payload) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 12000);
    try {
      const options = { cache: 'no-store', signal: controller.signal,
        headers: { 'Authorization': 'Bearer ' + (sessionStorage.getItem('1cat-panel-access') || '') } };
      if (payload !== undefined) {
        options.method = 'POST';
        Object.assign(options.headers, { 'Content-Type': 'application/json', 'X-1Cat-CSRF': csrf });
        options.body = JSON.stringify(payload);
      }
      const response = await fetch(path, options);
      if (response.status === 401) {
        dirty = false;
        sessionStorage.removeItem('1cat-panel-access');
        location.reload();
        throw new Error('管理登录已失效，请重新获取本机登录链接');
      }
      const data = await response.json();
      if (!response.ok || !data.ok) throw new Error(data.error || '请求失败');
      return data;
    } catch (error) {
      if (error.name === 'AbortError') throw new Error('请求超时，请检查客户端是否仍在运行');
      throw error;
    } finally { clearTimeout(timer); }
  }

  countInput.addEventListener('change', syncRows);
  for (const [id, delta] of [['less-mapping', -1], ['more-mapping', 1]]) {
    byID(id).addEventListener('click', () => {
      countInput.value = Math.max(1, Math.min(20, (Number(countInput.value) || rows().length) + delta));
      if (syncRows()) markDirty();
    });
  }
  for (const element of [mappingList, countInput, byID('server-id'), byID('node-name'), byID('credential')]) {
    element.addEventListener('input', markDirty);
    element.addEventListener('change', markDirty);
  }
  window.addEventListener('beforeunload', event => {
    if (dirty) { event.preventDefault(); event.returnValue = ''; }
  });
  byID('toggle-credential').addEventListener('click', function () {
    const visible = byID('credential').type === 'password';
    byID('credential').type = visible ? 'text' : 'password';
    this.textContent = visible ? '隐藏' : '显示';
    this.setAttribute('aria-pressed', String(visible));
  });
  byID('save-button').addEventListener('click', async function () {
    if (saving || !syncRows()) return;
    const controls = Array.from(this.closest('section').querySelectorAll('input,select,button'));
    const previous = controls.map(control => control.disabled);
    const mappings = rows().map(row => ({
      name: row.querySelector('.mapping-name').value.trim(),
      protocol: row.querySelector('.mapping-protocol').value,
      local_host: row.querySelector('.mapping-host').value.trim(),
      local_port: Number(row.querySelector('.mapping-port').value),
    }));
    saving = true;
    controls.forEach(control => { control.disabled = true; });
    this.textContent = '正在保存…';
    try {
      const data = await requestJSON('/api/config', {
        revision, server_id: byID('server-id').value, node_name: byID('node-name').value.trim(),
        credential: byID('credential').value, mapping_count: mappings.length, mappings,
      });
      revision = data.revision;
      byID('node-name').value = data.node_name;
      byID('credential').value = '';
      byID('credential').placeholder = '已保存，留空保持不变';
      byID('credential').type = 'password';
      byID('toggle-credential').textContent = '显示';
      byID('toggle-credential').setAttribute('aria-pressed', 'false');
      dirty = false;
      removedRows.length = 0;
      byID('unsaved').classList.add('hidden');
      showNotice(data.message + '。连接结果将在上方自动更新。');
    } catch (error) { showNotice(error.message, true); }
    finally {
      saving = false;
      controls.forEach((control, index) => { control.disabled = previous[index]; });
      this.textContent = '保存并应用';
    }
  });
  byID('reconnect-button').addEventListener('click', async function () {
    this.disabled = true;
    try { showNotice((await requestJSON('/api/reconnect', {})).message); }
    catch (error) { showNotice(error.message, true); }
    finally { this.disabled = false; }
  });
  byID('add-server').addEventListener('click', async function () {
    this.disabled = true;
    try {
      const data = await requestJSON('/api/servers', {
        action: 'add', name: byID('new-server-name').value,
        server_addr: byID('new-server-address').value, tls_server_name: byID('new-server-tls').value,
      });
      const option = document.createElement('option');
      option.value = data.server.id;
      option.textContent = data.server.name + ' · ' + data.server.server_addr + ' · TLS 1.3';
      byID('server-id').appendChild(option);
      const entry = document.createElement('div');
      entry.className = 'server-entry';
      entry.dataset.serverId = data.server.id;
      const label = document.createElement('span');
      label.textContent = data.server.name + ' · ' + data.server.server_addr;
      const button = document.createElement('button');
      button.type = 'button'; button.className = 'secondary remove-server'; button.textContent = '删除';
      entry.append(label, button);
      byID('server-list').appendChild(entry);
      for (const id of ['new-server-name', 'new-server-address', 'new-server-tls']) byID(id).value = '';
      showNotice('服务器已添加。请选择需要连接的服务器，填写凭据后保存。');
    } catch (error) { showNotice(error.message, true); }
    finally { this.disabled = false; }
  });
  byID('server-list').addEventListener('click', async event => {
    const button = event.target.closest('.remove-server');
    if (!button) return;
    const entry = button.closest('.server-entry');
    button.disabled = true;
    try {
      await requestJSON('/api/servers', { action: 'remove', id: entry.dataset.serverId });
      for (const option of Array.from(byID('server-id').options)) {
        if (option.value === entry.dataset.serverId) {
          if (option.selected) markDirty();
          option.remove();
        }
      }
      entry.remove();
      showNotice('服务器已从列表删除');
    } catch (error) { showNotice(error.message, true); }
    finally { button.disabled = false; }
  });

  async function refreshStatus() {
    try {
      const data = await requestJSON('/api/status');
      const pill = byID('runtime-pill');
      pill.textContent = data.status;
      pill.className = 'status-pill' + (data.connected ? ' online' : '');
      byID('status-server').textContent = data.server_addr || '等待配置';
      byID('status-node').textContent = data.node_name || '等待配置';
      byID('runtime-error').textContent = data.last_error || '';
      const mappings = data.mappings || [];
      byID('status-count').textContent = mappings.filter(item => item.public_target).length + ' / ' + mappings.length;
      const byName = new Map(mappings.map(item => [item.name.toLowerCase(), item]));
      rows().forEach(row => {
        const item = byName.get(row.querySelector('.mapping-name').value.trim().toLowerCase());
        const target = row.querySelector('[data-role="public"]');
        target.textContent = item ? (item.public_target || '等待服务端分配') + ' · ' + item.status : '等待保存';
        const copy = row.querySelector('.copy-button');
        copy.dataset.address = item ? item.public_target : '';
        copy.disabled = saving || !copy.dataset.address;
      });
      if (data.revision !== revision) {
        showNotice('配置已被其他页面修改。请刷新页面后继续编辑，当前输入不会被自动覆盖。', true);
      }
    } catch (error) {
      byID('runtime-pill').textContent = '管理连接中断';
      byID('runtime-pill').className = 'status-pill';
      byID('runtime-error').textContent = '状态刷新失败：' + error.message;
    } finally { setTimeout(refreshStatus, document.hidden ? 8000 : 2500); }
  }
  rows().forEach(prepareRow);
  refreshStatus();
})();
