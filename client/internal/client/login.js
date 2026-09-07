'use strict';
window.addEventListener('hashchange', () => {
  if (new URLSearchParams(location.hash.slice(1)).has('access')) {
    window.dispatchEvent(new Event('1cat-panel-logout'));
    location.reload();
  }
});
(async () => {
  const slot = '1cat-panel-access';
  const fragment = new URLSearchParams(location.hash.slice(1));
  const incoming = fragment.get('access');
  history.replaceState(null, '', '/');
  if (incoming && /^[a-f0-9]{64}$/.test(incoming)) sessionStorage.setItem(slot, incoming);
  const key = sessionStorage.getItem(slot);
  if (!key) return;
  try {
    const response = await fetch('/api/view', { cache: 'no-store', headers: { Authorization: 'Bearer ' + key } });
    if (!response.ok) throw new Error('登录已失效，请重新执行 1cattunnel panel 获取链接。');
    const page = new DOMParser().parseFromString(await response.text(), 'text/html');
    // Parsed scripts are inert. Load only the known, same-origin application.
    for (const script of page.querySelectorAll('script')) script.remove();
    document.head.replaceChildren(...page.head.childNodes);
    document.body.replaceChildren(...page.body.childNodes);
    const logout = document.createElement('button');
    logout.type = 'button';
    logout.textContent = '退出本页面登录';
    logout.title = '退出本页面，并放弃尚未保存的修改';
    logout.addEventListener('click', () => {
      window.dispatchEvent(new Event('1cat-panel-logout'));
      sessionStorage.removeItem(slot);
      location.reload();
    });
    document.body.prepend(logout);
    const script = document.createElement('script');
    script.src = '/assets/management.js';
    document.body.appendChild(script);
  } catch (error) {
    sessionStorage.removeItem(slot);
    const message = document.getElementById('login-message');
    if (message) message.textContent = error.message;
  }
})();
