// 前端逻辑：WebSocket 收发消息与切换会话，HTTP 上传/下载文件

const el = {
  app: document.getElementById('app'),
  messages: document.getElementById('messages'),
  fileList: document.getElementById('fileList'),
  fileCount: document.getElementById('fileCount'),
  fileCountBadge: document.getElementById('fileCountBadge'),
  uploadTray: document.getElementById('uploadTray'),
  composer: document.getElementById('composer'),
  input: document.getElementById('input'),
  pickTop: document.getElementById('pickTop'),
  pickChat: document.getElementById('pickChat'),
  attachBtn: document.getElementById('attachBtn'),
  dropOverlay: document.getElementById('dropOverlay'),
  connStatus: document.getElementById('connStatus'),
  connText: document.getElementById('connText'),
  sessionName: document.getElementById('sessionName'),
  filesBtn: document.getElementById('filesBtn'),
  filesPanel: document.getElementById('filesPanel'),
  filesClose: document.getElementById('filesClose'),
  filesToggle: document.getElementById('filesToggle'),
  filesToggleLabel: document.getElementById('filesToggleLabel'),
  scrim: document.getElementById('scrim'),
  qrBtn: document.getElementById('qrBtn'),
  historyBtn: document.getElementById('historyBtn'),
  qrModal: document.getElementById('qrModal'),
  qrBox: document.getElementById('qrBox'),
  qrUrl: document.getElementById('qrUrl'),
  qrAlts: document.getElementById('qrAlts'),
  sessionModal: document.getElementById('sessionModal'),
  sessionList: document.getElementById('sessionList'),
  confirmModal: document.getElementById('confirmModal'),
  confirmText: document.getElementById('confirmText'),
  confirmOk: document.getElementById('confirmOk'),
  userName: document.getElementById('userName'),
  userAvatar: document.getElementById('userAvatar'),
  userBtn: document.getElementById('userBtn'),
  toast: document.getElementById('toast'),
};

const url = {
  upload: '/api/upload',
  file: (sessionId, fileId) => `/api/files/${sessionId}/${fileId}`,
  download: (sessionId, fileId) => `/api/files/${sessionId}/${fileId}/download`,
};

const isDesktop = () => window.matchMedia('(min-width: 900px)').matches;
const isMobileLayout = () => window.matchMedia('(max-width: 899px)').matches;

// 主机口令只出现在服务启动时自动打开的地址里，用于判定"谁启动的服务谁就是主机"
const hostToken =
  new URLSearchParams(location.search).get('host') || localStorage.getItem('lanfile.host') || '';
if (hostToken) {
  localStorage.setItem('lanfile.host', hostToken);
  const clean = new URL(location.href);
  clean.searchParams.delete('host');
  history.replaceState(null, '', clean.pathname + clean.search + clean.hash);
}

// 设备 ID：长期存在浏览器里，刷新或重开页面仍是同一台设备，
// 这样自己的历史消息刷新后依然显示在右边
let clientId = localStorage.getItem('lanfile.clientId') || '';
if (!/^[0-9a-f]{16}$/.test(clientId)) {
  const bytes = new Uint8Array(8);
  (window.crypto || {}).getRandomValues?.(bytes);
  clientId = [...bytes].map((byte) => byte.toString(16).padStart(2, '0')).join('').slice(0, 16);
  if (!/^[0-9a-f]{16}$/.test(clientId)) {
    clientId = Math.random().toString(16).slice(2).padEnd(16, '0').slice(0, 16);
  }
  localStorage.setItem('lanfile.clientId', clientId);
}

const state = {
  selfId: clientId,
  clientId,
  name: localStorage.getItem('lanfile.name') || '',
  host: false,
  peers: 0,
  connected: false,
  session: null,
  sessions: [],
  files: [],
  lan: [],
  qrUrl: '',
  messages: [],
  uploads: [],
};

// 旧版本的自动昵称（我的电脑-3F）作废，交给服务端重新分配
if (/^我的(电脑|手机)-[0-9A-F]{2}$/.test(state.name)) {
  state.name = '';
  localStorage.removeItem('lanfile.name');
}

/* ---------------- 工具 ---------------- */

const esc = (value) =>
  String(value).replace(/[&<>"']/g, (ch) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch]));

const formatSize = (bytes) => {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 ** 2) return `${(bytes / 1024).toFixed(0)} KB`;
  if (bytes < 1024 ** 3) return `${(bytes / 1024 ** 2).toFixed(1)} MB`;
  return `${(bytes / 1024 ** 3).toFixed(2)} GB`;
};

const formatTime = (ts) =>
  new Date(ts).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false });

// 会话文件夹名 2026-09-18_20-45-12 -> 2026-09-18 20:45
function formatSession(id) {
  const [date, time] = (id || '').split('_');
  if (!date || !time) return id || '';
  const [hour, minute] = time.split('-');
  return `${date} ${hour}:${minute}`;
}

// 列表里带上秒，避免同一分钟内建的会话看起来一样
function formatSessionFull(id) {
  const [date, time] = (id || '').split('_');
  if (!date || !time) return id || '';
  const [hour, minute, second] = time.split('-');
  return `${date} ${hour}:${minute}:${second}`;
}

// 当天只显示时分，跨天补上日期
function formatStamp(ts) {
  const date = new Date(ts);
  const today = new Date();
  const time = formatTime(ts);
  if (date.toDateString() === today.toDateString()) return time;
  return `${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')} ${time}`;
}

const extOf = (name) => (name.includes('.') ? name.split('.').pop().slice(0, 4).toUpperCase() : 'FILE');
// 只有真实内容就是图片才内联预览（类型由服务端读文件头判定）
const isImage = (file) => (file.type || '').startsWith('image/');

// 头像显示昵称里的数字（海豚-27 → 27），没有数字就用首字（主机 → 主）
function avatarText(name) {
  const match = /-(\d{1,3})$/.exec(name || '');
  if (match) return match[1];
  return (name || '?').slice(0, 1);
}

// 头像底色按设备 ID 固定，方便区分不同设备
function avatarClass(seed) {
  const text = String(seed || '');
  let hash = 0;
  for (let i = 0; i < text.length; i += 1) hash = (hash * 31 + text.charCodeAt(i)) >>> 0;
  return `av-${hash % 8}`;
}

let toastTimer;
function showToast(text) {
  el.toast.textContent = text;
  el.toast.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    el.toast.hidden = true;
  }, 2200);
}

const findFile = (id) => state.messages.find((msg) => msg.file?.id === id)?.file;

function download(file) {
  const link = document.createElement('a');
  link.href = url.download(state.session.id, file.id);
  link.download = file.name;
  document.body.appendChild(link);
  link.click();
  link.remove();
}

/* ---------------- 文件面板：PC 折叠 / 手机抽屉 ---------------- */

const FILES_KEY = 'lanfile.files';
const filesWantedOpen = () => localStorage.getItem(FILES_KEY) === 'open';

function applyFilesLayout() {
  const open = filesWantedOpen();
  el.filesToggleLabel.textContent = open ? '收起' : '展开';

  if (isMobileLayout()) {
    document.body.classList.toggle('files-open', open);
    el.scrim.hidden = !open;
    el.app.dataset.files = 'collapsed';
  } else {
    document.body.classList.remove('files-open');
    el.scrim.hidden = true;
    el.app.dataset.files = open ? 'open' : 'collapsed';
  }
}

function toggleFiles(force) {
  const next = force === undefined ? !filesWantedOpen() : force;
  localStorage.setItem(FILES_KEY, next ? 'open' : 'closed');
  applyFilesLayout();
}

el.filesBtn.addEventListener('click', () => toggleFiles());
el.filesClose.addEventListener('click', () => toggleFiles(false));
el.filesToggle.addEventListener('click', (event) => {
  event.stopPropagation();
  toggleFiles();
});
el.scrim.addEventListener('click', () => toggleFiles(false));
el.filesPanel.addEventListener('click', (event) => {
  // PC 折叠状态下点窄条即可展开
  if (!isMobileLayout() && !filesWantedOpen()) toggleFiles(true);
  // PC 展开状态下点标题栏可以收起
  else if (!isMobileLayout() && event.target.closest('.panel-head')) toggleFiles(false);
  if (isMobileLayout() && event.target.closest('.file-item, .dropzone')) toggleFiles(false);
});
window.addEventListener('resize', applyFilesLayout);

/* ---------------- WebSocket ---------------- */

let socket;
let retryDelay = 1000;

function send(payload) {
  if (socket?.readyState !== WebSocket.OPEN) {
    showToast('未连接到服务，请稍后重试');
    return false;
  }
  socket.send(JSON.stringify(payload));
  return true;
}

function setConnected(connected) {
  state.connected = connected;
  el.connStatus.classList.toggle('offline', !connected);
  renderStatus();
}

function connect() {
  const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
  const query = hostToken ? `?host=${encodeURIComponent(hostToken)}` : '';
  socket = new WebSocket(`${scheme}://${location.host}/ws${query}`);

  socket.addEventListener('open', () => {
    retryDelay = 1000;
    setConnected(true);
    send({ type: 'hello', name: state.name, clientId: state.clientId });
  });

  socket.addEventListener('message', (event) => {
    let data;
    try {
      data = JSON.parse(event.data);
    } catch {
      return;
    }

    switch (data.type) {
      case 'init':
        state.selfId = data.selfId;
        state.host = Boolean(data.host);
        state.peers = data.peers || 1;
        state.session = data.session;
        state.messages = data.history || [];
        state.files = data.files || [];
        state.lan = data.lan || [];
        state.qrUrl = '';
        if (!state.name && data.name) adoptName(data.name);
        renderAll();
        break;

      case 'self':
        // 服务端确认的昵称（没名字的设备会拿到「主机」或随机的「海豚-27」）
        if (data.selfId) state.selfId = data.selfId;
        if (data.name && data.name !== state.name) adoptName(data.name);
        break;

      case 'message':
        state.messages.push(data.message);
        if (data.message.file && !state.files.some((file) => file.id === data.message.file.id)) {
          state.files.push(data.message.file);
        }
        renderAll();
        break;

      case 'deleted':
        for (const msg of state.messages) {
          if (msg.file?.id === data.fileId) msg.file.deleted = true;
        }
        state.files = state.files.filter((file) => file.id !== data.fileId);
        renderAll();
        showToast('文件已删除');
        break;

      case 'peers':
        state.peers = data.peers;
        renderStatus();
        break;

      case 'sessions':
        state.sessions = data.sessions || [];
        renderSessions();
        break;

      case 'session':
        state.session = data.session;
        state.messages = data.history || [];
        state.files = data.files || [];
        if (keepModalsOpen) keepModalsOpen = false;
        else closeModals();
        renderAll();
        showToast(`已切换到 ${formatSession(data.session.id)}`);
        break;
    }
  });

  socket.addEventListener('close', () => {
    setConnected(false);
    setTimeout(connect, retryDelay);
    retryDelay = Math.min(retryDelay * 2, 10000);
  });

  socket.addEventListener('error', () => socket.close());
}

/* ---------------- 渲染 ---------------- */

function renderStatus() {
  el.connText.textContent = state.connected ? `已连接 · ${state.peers} 台设备` : '离线，重连中…';
}

function adoptName(name) {
  state.name = name;
  localStorage.setItem('lanfile.name', name);
  el.userName.textContent = name;
  el.userAvatar.textContent = avatarText(name);
  renderAll();
}

function renderSession() {
  el.sessionName.textContent = state.session ? formatSession(state.session.id) : '—';
  el.historyBtn.hidden = !state.host; // 只有主机能切换历史会话
}

function thumbHtml(file) {
  return isImage(file) && !file.deleted
    ? `<span class="file-thumb"><img src="${esc(url.file(state.session.id, file.id))}" alt="" loading="lazy" /></span>`
    : `<span class="file-thumb">${esc(extOf(file.name))}</span>`;
}

function renderMessages() {
  if (!state.messages.length) {
    el.messages.innerHTML = '<p class="empty-tip">还没有消息<br />发送第一条，或直接拖入文件</p>';
    return;
  }

  el.messages.innerHTML = state.messages
    .map((msg) => {
      // 自己发的：设备 ID 对得上（历史消息按昵称兜底）
      const mine =
        msg.clientId === state.clientId ||
        msg.clientId === state.selfId ||
        (Boolean(state.name) && msg.name === state.name);
      const parts = [];

      if (msg.text) parts.push(`<div class="bubble">${esc(msg.text)}</div>`);

      if (msg.file) {
        const file = msg.file;

        if (file.deleted) {
          parts.push(`<div class="file-card deleted">
              <span class="file-thumb">DEL</span>
              <span class="file-meta">
                <span class="file-name">${esc(file.name)}</span>
                <span class="file-sub">已删除</span>
              </span>
            </div>`);
        } else if (isImage(file)) {
          parts.push(
            `<img class="msg-image" src="${esc(url.file(state.session.id, file.id))}" alt="${esc(file.name)}" data-open="${esc(file.id)}" />`
          );
        } else {
          parts.push(`<div class="file-card" data-download="${esc(file.id)}">
              ${thumbHtml(file)}
              <span class="file-meta">
                <span class="file-name">${esc(file.name)}</span>
                <span class="file-sub">${formatSize(file.size)}</span>
              </span>
              <span class="download-tag">下载</span>
            </div>`);
        }
      }

      return `<div class="msg ${mine ? 'mine' : 'theirs'}">
          <span class="msg-avatar ${avatarClass(mine ? state.clientId : msg.clientId)}">${esc(avatarText(mine ? state.name : msg.name))}</span>
          <div class="msg-body">
            <div class="msg-head">
              <span class="msg-name">${esc(mine ? state.name : msg.name)}</span>
              <span>${formatTime(msg.ts)}</span>
            </div>
            ${parts.join('')}
          </div>
        </div>`;
    })
    .join('');

  el.messages.scrollTop = el.messages.scrollHeight;
}

function renderFiles() {
  // 以服务端下发的文件清单为准（不依赖聊天消息，孤儿文件也能看到）
  const files = state.files.filter((file) => !file.deleted);

  el.fileCount.textContent = `${files.length} 个`;
  el.fileCountBadge.textContent = String(files.length);

  if (!files.length) {
    el.fileList.innerHTML = '<li class="empty-tip">暂无共享文件</li>';
    return;
  }

  el.fileList.innerHTML = files
    .map(
      (file) => `<li class="file-item" data-download="${esc(file.id)}">
          ${thumbHtml(file)}
          <span class="file-meta">
            <span class="file-name">${esc(file.name)}</span>
            <span class="file-sub">${formatSize(file.size)}${file.from ? ` · 来自 ${esc(file.from)}` : ''}</span>
          </span>
          ${
            state.host
              ? `<button type="button" class="file-del" data-id="${esc(file.id)}" title="删除文件" aria-label="删除文件">
                   <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round">
                     <path d="M4 7h16M9 7V5h6v2M6 7l1 13h10l1-13M10 11v6M14 11v6" />
                   </svg>
                 </button>`
              : ''
          }
        </li>`
    )
    .join('');
}

function renderUploads() {
  el.uploadTray.hidden = state.uploads.length === 0;
  el.uploadTray.innerHTML = state.uploads
    .map(
      (item) => `<div class="upload-item">
          <span class="file-thumb">${esc(extOf(item.name))}</span>
          <span class="upload-info">
            <span class="upload-name">${esc(item.name)}</span>
            <span class="progress"><i style="width:${item.percent}%"></i></span>
          </span>
          <span class="upload-percent">${item.percent >= 100 ? '完成' : item.percent + '%'}</span>
        </div>`
    )
    .join('');
}

function renderSessions() {
  if (!state.sessions.length) {
    el.sessionList.innerHTML = '<li class="empty-tip">还没有历史会话</li>';
    return;
  }

  el.sessionList.innerHTML = state.sessions
    .map(
      (session) => `<li class="session-item ${session.current ? 'current' : ''}" data-session="${esc(session.id)}">
          <span class="session-main">
            <span class="session-title">
              ${esc(formatSessionFull(session.id))}
              ${session.current ? '<span class="session-tag">当前</span>' : ''}
            </span>
            <span class="session-sub">最后对话 ${formatStamp(session.updatedAt)} · ${session.messages} 条消息 · ${session.files} 个文件</span>
          </span>
          ${
            state.host
              ? `<button type="button" class="session-del" data-del="${esc(session.id)}" title="删除会话" aria-label="删除会话">×</button>`
              : ''
          }
        </li>`
    )
    .join('');
}

function renderQr() {
  const list = state.lan.length
    ? state.lan
    : [{ name: '本机', ip: location.hostname, url: location.origin, virtual: false }];

  const usable = list.filter((item) => !item.virtual);
  const targets = usable.length ? usable : list;
  if (!state.qrUrl || !list.some((item) => item.url === state.qrUrl)) {
    state.qrUrl = targets[0].url;
  }

  try {
    const qr = qrcode(0, 'M');
    qr.addData(state.qrUrl);
    qr.make();
    el.qrBox.innerHTML = qr.createSvgTag({ cellSize: 4, margin: 1, scalable: true });
  } catch {
    el.qrBox.innerHTML = '<p>二维码生成失败</p>';
  }

  el.qrUrl.textContent = state.qrUrl;
  el.qrAlts.innerHTML =
    list.length > 1
      ? list
          .map(
            (item) => `<button type="button" class="qr-alt ${item.url === state.qrUrl ? 'active' : ''}" data-url="${esc(item.url)}">
                <span class="qr-alt-name">${esc(item.name)}${item.virtual ? ' · 手机不可用' : ''}</span>
                <span>${esc(item.ip)}</span>
              </button>`
          )
          .join('')
      : '';
}

function renderAll() {
  renderStatus();
  renderSession();
  renderMessages();
  renderFiles();
  renderUploads();
}

/* ---------------- 浮窗 ---------------- */

function openModal(modal) {
  closeModals();
  modal.hidden = false;
}

function closeModals() {
  el.qrModal.hidden = true;
  el.sessionModal.hidden = true;
  hideConfirm();
}

// 确认框叠在会话列表之上：确认或取消之后，会话列表仍然开着
function openConfirm(text) {
  el.confirmText.textContent = text;
  el.confirmModal.hidden = false;
  el.confirmModal.classList.add('stacked');
}

function hideConfirm() {
  el.confirmModal.hidden = true;
  el.confirmModal.classList.remove('stacked');
}

el.qrBtn.addEventListener('click', () => {
  renderQr();
  openModal(el.qrModal);
});

el.historyBtn.addEventListener('click', () => {
  openModal(el.sessionModal);
  send({ type: 'sessions' });
});

for (const modal of [el.qrModal, el.sessionModal]) {
  modal.addEventListener('click', (event) => {
    if (event.target === modal || event.target.hasAttribute('data-close')) closeModals();
  });
}

el.confirmModal.addEventListener('click', (event) => {
  if (event.target === el.confirmModal || event.target.hasAttribute('data-close')) hideConfirm();
});

el.qrAlts.addEventListener('click', (event) => {
  const button = event.target.closest('.qr-alt');
  if (!button) return;
  state.qrUrl = button.dataset.url;
  renderQr();
});

el.sessionList.addEventListener('click', (event) => {
  const remove = event.target.closest('.session-del');
  if (remove) {
    event.stopPropagation();
    askDeleteSession(remove.dataset.del);
    return;
  }

  const item = event.target.closest('.session-item');
  if (!item || item.classList.contains('current')) return;
  send({ type: 'switch', sessionId: item.dataset.session });
});

// 删除文件：主持人确认后连磁盘文件一起删
let pendingDelete = '';
let pendingSession = '';
// 删除当前会话时服务端会广播新会话，这个标记让会话列表保持打开
let keepModalsOpen = false;

function askDelete(fileId) {
  const file = findFile(fileId);
  if (!file) return;
  pendingSession = '';
  pendingDelete = fileId;
  openConfirm(file.name);
}

// 删除整个会话：连文件夹和里面的文件一起删，删的是当前会话就马上开新会话
function askDeleteSession(sessionId) {
  const session = state.sessions.find((item) => item.id === sessionId);
  if (!session) return;
  pendingDelete = '';
  pendingSession = sessionId;
  const title = `${session.current ? '当前会话' : '会话'} ${formatSessionFull(sessionId)}（${session.messages} 条消息 · ${session.files} 个文件）`;
  openConfirm(`${title}${session.current ? '，删除后会立即开始一个新会话' : ''}`);
}

el.confirmOk.addEventListener('click', () => {
  if (pendingSession) {
    // 删当前会话会让服务端广播新会话，这里别把会话列表关掉
    keepModalsOpen = pendingSession === state.session?.id;
    send({ type: 'deleteSession', sessionId: pendingSession });
  } else if (pendingDelete) {
    send({ type: 'delete', fileId: pendingDelete });
  }
  pendingSession = '';
  pendingDelete = '';
  hideConfirm();
});

document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape') {
    closeModals();
    toggleFiles(false);
  }
});

/* ---------------- 交互 ---------------- */

function autoGrow() {
  el.input.style.height = 'auto';
  el.input.style.height = `${Math.min(el.input.scrollHeight, 120)}px`;
}

function sendText(text) {
  const content = text.trim();
  if (!content) return;
  if (send({ type: 'chat', text: content })) {
    el.input.value = '';
    autoGrow();
  }
}

function handleFiles(fileList) {
  const files = [...fileList];
  if (!files.length) return;

  const caption = el.input.value.trim();
  if (caption) {
    el.input.value = '';
    autoGrow();
  }

  files.forEach((file, index) => uploadFile(file, index === 0 ? caption : ''));
}

function uploadFile(file, caption) {
  const item = { key: `${Date.now()}-${Math.random()}`, name: file.name, percent: 0 };
  state.uploads.push(item);
  renderUploads();

  const form = new FormData();
  form.append('text', caption);
  form.append('name', state.name || '');
  form.append('clientId', state.clientId);
  form.append('file', file, file.name);

  const xhr = new XMLHttpRequest();
  xhr.open('POST', url.upload);
  xhr.upload.addEventListener('progress', (event) => {
    if (!event.lengthComputable) return;
    item.percent = Math.round((event.loaded / event.total) * 100);
    renderUploads();
  });
  xhr.addEventListener('load', () => {
    if (xhr.status === 200) {
      const meta = JSON.parse(xhr.responseText);
      // 服务端已经建好消息并广播；这里做一次本地兜底，断线时也能立刻看到文件
      if (!state.files.some((item) => item.id === meta.id)) {
        state.files.push(meta);
        renderFiles();
      }
      if (socket?.readyState !== WebSocket.OPEN) {
        showToast('文件已上传，消息会在重连后同步');
      }
    } else {
      let reason = `HTTP ${xhr.status}`;
      try {
        reason = JSON.parse(xhr.responseText).error || reason;
      } catch {}
      showToast(`上传失败：${reason}`);
    }
    finishUpload(item);
  });
  xhr.addEventListener('error', () => {
    showToast('上传失败，请检查网络连接');
    finishUpload(item);
  });
  xhr.send(form);
}

function finishUpload(item) {
  item.percent = 100;
  renderUploads();
  setTimeout(() => {
    state.uploads = state.uploads.filter((entry) => entry !== item);
    renderUploads();
  }, 800);
}

el.composer.addEventListener('submit', (event) => {
  event.preventDefault();
  sendText(el.input.value);
});

el.input.addEventListener('input', autoGrow);

el.input.addEventListener('keydown', (event) => {
  if (event.key === 'Enter' && !event.shiftKey && isDesktop()) {
    event.preventDefault();
    sendText(el.input.value);
  }
});

for (const picker of [el.pickTop, el.pickChat]) {
  picker.addEventListener('change', () => {
    handleFiles(picker.files);
    picker.value = '';
  });
}

el.attachBtn.addEventListener('click', () => el.pickChat.click());

el.fileList.addEventListener('click', (event) => {
  const remove = event.target.closest('.file-del');
  if (remove) {
    event.stopPropagation();
    askDelete(remove.dataset.id);
    return;
  }

  const item = event.target.closest('.file-item');
  if (!item) return;
  const file = findFile(item.dataset.download);
  if (file) download(file);
});

document.addEventListener('click', (event) => {
  const downloadTarget = event.target.closest('[data-download]');
  if (downloadTarget && !event.target.closest('.file-del')) {
    const file = findFile(downloadTarget.dataset.download);
    if (file) download(file);
    return;
  }

  const image = event.target.closest('[data-open]');
  if (image) window.open(url.file(state.session.id, image.dataset.open), '_blank');
});

el.userBtn.addEventListener('click', () => {
  const name = prompt('修改本机昵称', state.name);
  if (!name?.trim()) return;

  state.name = name.trim().slice(0, 16);
  localStorage.setItem('lanfile.name', state.name);
  el.userName.textContent = state.name;
  el.userAvatar.textContent = avatarText(state.name);
  send({ type: 'hello', name: state.name, clientId: state.clientId });
  renderAll();
});

// 仅鼠标设备启用拖拽上传：手机触摸拖动不应弹出遮罩层
const supportsDragDrop = window.matchMedia('(hover: hover) and (pointer: fine)').matches;
let dragDepth = 0;

function hideDropOverlay() {
  dragDepth = 0;
  el.dropOverlay.hidden = true;
}

if (supportsDragDrop) {
  document.addEventListener('dragenter', (event) => {
    event.preventDefault();
    dragDepth += 1;
    el.dropOverlay.hidden = false;
  });

  document.addEventListener('dragover', (event) => event.preventDefault());

  document.addEventListener('dragleave', () => {
    dragDepth = Math.max(0, dragDepth - 1);
    if (dragDepth === 0) el.dropOverlay.hidden = true;
  });

  document.addEventListener('drop', (event) => {
    event.preventDefault();
    handleFiles(event.dataTransfer.files);
    hideDropOverlay();
  });

  el.dropOverlay.addEventListener('click', hideDropOverlay);
  window.addEventListener('blur', hideDropOverlay);
}

el.userName.textContent = state.name;
el.userAvatar.textContent = avatarText(state.name) || '?';
el.userAvatar.className = `avatar ${avatarClass(state.clientId)}`;
applyFilesLayout();
renderAll();
connect();
