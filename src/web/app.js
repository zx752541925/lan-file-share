// 前端逻辑：WebSocket 收发消息与切换会话，HTTP 上传/下载文件

const el = {
  messages: document.getElementById('messages'),
  fileList: document.getElementById('fileList'),
  fileCount: document.getElementById('fileCount'),
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
  qrBtn: document.getElementById('qrBtn'),
  historyBtn: document.getElementById('historyBtn'),
  qrModal: document.getElementById('qrModal'),
  qrBox: document.getElementById('qrBox'),
  qrUrl: document.getElementById('qrUrl'),
  qrAlts: document.getElementById('qrAlts'),
  sessionModal: document.getElementById('sessionModal'),
  sessionList: document.getElementById('sessionList'),
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
const isMobileDevice = () => /android|iphone|ipad|mobile/i.test(navigator.userAgent);

// 主机口令只出现在服务启动时自动打开的地址里，用于判定"谁启动的服务谁就是主机"
const hostToken = new URLSearchParams(location.search).get('host') || localStorage.getItem('lanfile.host') || '';
if (hostToken) {
  localStorage.setItem('lanfile.host', hostToken);
  const clean = new URL(location.href);
  clean.searchParams.delete('host');
  history.replaceState(null, '', clean.pathname + clean.search + clean.hash);
}

const state = {
  selfId: '',
  name: localStorage.getItem('lanfile.name') || '',
  host: false,
  peers: 0,
  connected: false,
  session: null,
  sessions: [],
  urls: [],
  qrUrl: '',
  messages: [],
  uploads: [],
};

if (!state.name) {
  state.name = `${isMobileDevice() ? '我的手机' : '我的电脑'}-${Math.random().toString(16).slice(2, 4).toUpperCase()}`;
  localStorage.setItem('lanfile.name', state.name);
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
  const sameDay = date.toDateString() === today.toDateString();
  const time = formatTime(ts);
  if (sameDay) return time;
  return `${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')} ${time}`;
}

const extOf = (name) => (name.includes('.') ? name.split('.').pop().slice(0, 4).toUpperCase() : 'FILE');
const isImage = (file) => (file.type || '').startsWith('image/');

let toastTimer;
function showToast(text) {
  el.toast.textContent = text;
  el.toast.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    el.toast.hidden = true;
  }, 2200);
}

function download(file) {
  const link = document.createElement('a');
  link.href = url.download(state.session.id, file.id);
  link.download = file.name;
  document.body.appendChild(link);
  link.click();
  link.remove();
}

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
    send({ type: 'hello', name: state.name });
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
        state.urls = data.urls || [];
        renderAll();
        break;

      case 'message':
        state.messages.push(data.message);
        renderAll();
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
        closeModals();
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

function renderSession() {
  el.sessionName.textContent = state.session ? formatSession(state.session.id) : '—';
  el.historyBtn.hidden = !state.host; // 只有主机能切换历史会话
}

function thumbHtml(file) {
  return isImage(file)
    ? `<span class="file-thumb"><img src="${esc(url.file(state.session.id, file.id))}" alt="" loading="lazy" /></span>`
    : `<span class="file-thumb">${esc(extOf(file.name))}</span>`;
}

function renderMessages() {
  if (!state.messages.length) {
    el.messages.innerHTML = '<p class="empty-tip">还没有消息，发送第一条吧</p>';
    return;
  }

  el.messages.innerHTML = state.messages
    .map((msg) => {
      const mine = msg.clientId === state.selfId;
      const parts = [];

      if (msg.text) parts.push(`<div class="bubble">${esc(msg.text)}</div>`);

      if (msg.file) {
        const file = msg.file;
        if (isImage(file)) {
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
          <span class="msg-avatar">${esc(mine ? state.name.slice(0, 1) : msg.name.slice(0, 1))}</span>
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
  const files = state.messages.filter((msg) => msg.file).map((msg) => ({ ...msg.file, from: msg.name }));
  el.fileCount.textContent = `${files.length} 个`;

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
            <span class="file-sub">${formatSize(file.size)} · 来自 ${esc(file.from)}</span>
          </span>
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
          <span class="session-title">
            ${esc(formatSessionFull(session.id))}
            ${session.current ? '<span class="session-tag">当前</span>' : ''}
          </span>
          <span class="session-sub">最后对话 ${formatStamp(session.updatedAt)} · ${session.messages} 条消息 · ${session.files} 个文件</span>
        </li>`
    )
    .join('');
}

function renderQr() {
  const targets = state.urls.length ? state.urls : [location.origin];
  if (!state.qrUrl || !targets.includes(state.qrUrl)) {
    state.qrUrl = targets[0];
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
    targets.length > 1
      ? targets.map((item) => `<button type="button" class="qr-alt ${item === state.qrUrl ? 'active' : ''}" data-url="${esc(item)}">${esc(item.replace('http://', ''))}</button>`).join('')
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

el.qrAlts.addEventListener('click', (event) => {
  const button = event.target.closest('.qr-alt');
  if (!button) return;
  state.qrUrl = button.dataset.url;
  renderQr();
});

el.sessionList.addEventListener('click', (event) => {
  const item = event.target.closest('.session-item');
  if (!item || item.classList.contains('current')) return;
  send({ type: 'switch', sessionId: item.dataset.session });
});

document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape') closeModals();
});

/* ---------------- 交互 ---------------- */

function autoGrow() {
  el.input.style.height = 'auto';
  el.input.style.height = `${Math.min(el.input.scrollHeight, 130)}px`;
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
      send({ type: 'chat', text: caption, fileId: meta.id });
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

document.addEventListener('click', (event) => {
  const downloadTarget = event.target.closest('[data-download]');
  if (downloadTarget) {
    const file = findFile(downloadTarget.dataset.download);
    if (file) download(file);
    return;
  }

  const image = event.target.closest('[data-open]');
  if (image) window.open(url.file(state.session.id, image.dataset.open), '_blank');
});

function findFile(id) {
  const message = state.messages.find((msg) => msg.file?.id === id);
  return message?.file;
}

el.userBtn.addEventListener('click', () => {
  const name = prompt('修改本机昵称', state.name);
  if (!name?.trim()) return;

  state.name = name.trim().slice(0, 16);
  localStorage.setItem('lanfile.name', state.name);
  el.userName.textContent = state.name;
  el.userAvatar.textContent = state.name.slice(0, 1);
  send({ type: 'hello', name: state.name });
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

document.querySelectorAll('.tabbar button').forEach((button) => {
  button.addEventListener('click', () => {
    document.body.dataset.view = button.dataset.view;
    document.querySelectorAll('.tabbar button').forEach((item) => item.classList.toggle('active', item === button));
  });
});

el.userName.textContent = state.name;
el.userAvatar.textContent = state.name.slice(0, 1);
renderAll();
connect();
