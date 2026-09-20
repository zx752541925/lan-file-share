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
  qrState: document.getElementById('qrState'),
  qrRotate: document.getElementById('qrRotate'),
  qrCopy: document.getElementById('qrCopy'),
  sessionModal: document.getElementById('sessionModal'),
  sessionList: document.getElementById('sessionList'),
  linkBtn: document.getElementById('linkBtn'),
  linkModal: document.getElementById('linkModal'),
  hostLinkInput: document.getElementById('hostLinkInput'),
  hostLinkCopy: document.getElementById('hostLinkCopy'),
  hostLinkRotate: document.getElementById('hostLinkRotate'),
  hostQrBox: document.getElementById('hostQrBox'),
  inviteNote: document.getElementById('inviteNote'),
  inviteCreate: document.getElementById('inviteCreate'),
  inviteList: document.getElementById('inviteList'),
  deviceList: document.getElementById('deviceList'),
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

// 身份由服务端 cookie 决定（主机链接 ?host= / 邀请链接 ?invite= 在服务端兑换成 cookie，
// 然后 302 把参数抹掉）。旧版本把主机口令存在 localStorage 里，这里顺手清掉。
localStorage.removeItem('lanfile.host');

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
  messages: [],
  uploads: [],
  hostLink: '',
  invites: [],
  devices: [],
};

// 旧版本的自动昵称（我的电脑-3F）作废，交给服务端重新分配
if (/^我的(电脑|手机)-[0-9A-F]{2}$/.test(state.name)) {
  state.name = '';
  localStorage.removeItem('lanfile.name');
}

/* ---------------- 工具 ---------------- */

const esc = (value) =>
  String(value).replace(/[&<>"']/g, (ch) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch]));

// 网址识别：只认 http/https 与 www. 开头，转义后再拼成 <a>，避免 XSS。
// 中日韩文字与全角标点不算网址内容，"打开https://a.com就行" 里网址到 .com 为止。
const urlPattern = /(https?:\/\/[^\s<>"'\u4e00-\u9fff\u3000-\u303f\uff00-\uffef]+|www\.[^\s<>"'\u4e00-\u9fff\u3000-\u303f\uff00-\uffef]+)/gi;
// 中文标点通常紧跟在网址后面，属于句子而不是链接的一部分
const urlTailPattern = /[.,;:!?)\]}>。，、；：！？）】》」』"']+$/;

function linkify(value) {
  return String(value)
    .split(urlPattern)
    .map((part, index) => {
      if (index % 2 === 0) return esc(part);

      let url = part.replace(urlTailPattern, '');
      let tail = part.slice(url.length);
      // 括号成对时把右括号还给网址，如 .../wiki/A_(b)
      const open = (url.match(/\(/g) || []).length;
      const close = (url.match(/\)/g) || []).length;
      if (close < open && tail.startsWith(')')) {
        url += ')';
        tail = tail.slice(1);
      }
      // 去掉结尾标点后剩下的不构成网址（如只有 "https://"），按纯文本处理
      if (!url.replace(/^(https?:\/\/|www\.)/i, '')) return esc(part);

      const href = /^www\./i.test(url) ? `https://${url}` : url;
      return `<a href="${esc(href)}" target="_blank" rel="noopener noreferrer">${esc(url)}</a>${esc(tail)}`;
    })
    .join('');
}

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
// 只有真实内容就是图片/视频/音频才内联预览（类型由服务端读文件头判定）
const isImage = (file) => (file.type || '').startsWith('image/');
const isVideo = (file) => (file.type || '').startsWith('video/');
const isAudio = (file) => (file.type || '').startsWith('audio/');

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

// 主机不需要"下载"：直接在新标签页里打开（视频/图片/PDF 都是内联预览）
function openFile(file) {
  window.open(url.file(state.session.id, file.id), '_blank');
}

// 主机专属：让服务端在资源管理器里定位到这个文件
async function revealFile(file) {
  try {
    const res = await fetch(`/api/reveal/${state.session.id}/${file.id}`);
    const data = await res.json().catch(() => null);
    showToast(res.ok ? '已在文件管理器中定位' : data?.error || '定位失败');
  } catch {
    showToast('定位失败');
  }
}

/* ---------------- 音量增强：浏览器里播放偏小时，用 Web Audio 放大 2 倍 ---------------- */

const BOOST_KEY = 'lanfile.boost';
let boostOn = localStorage.getItem(BOOST_KEY) !== 'off';
let audioCtx = null;
const boostedMedia = new WeakMap(); // 媒体元素 -> GainNode（同一元素只能接入一次）

const boostLabel = () => (boostOn ? '音量 ×2' : '音量 原声');

// 视频/音频块：媒体本体 + 一个全局生效的音量增强开关
function mediaBlock(mediaHtml, file) {
  return `<div class="media-block">
      ${mediaHtml}
      <button type="button" class="media-boost${boostOn ? ' on' : ''}" data-boost="${esc(file.id)}">${boostLabel()}</button>
    </div>`;
}

// 把媒体元素接进 Web Audio 放大音量；元素自身的音量滑块仍然生效
function boostMedia(media) {
  if (!boostOn) return;
  if (!media.isConnected) return; // 重新渲染后留下的旧元素不接入，避免音频图堆积
  try {
    const Ctx = window.AudioContext || window.webkitAudioContext;
    if (!Ctx) return;
    if (!audioCtx) audioCtx = new Ctx();
    audioCtx.resume?.();

    let gain = boostedMedia.get(media);
    if (!gain) {
      gain = audioCtx.createGain();
      audioCtx.createMediaElementSource(media).connect(gain).connect(audioCtx.destination);
      boostedMedia.set(media, gain);
    }
    gain.gain.value = 2;
  } catch {
    // 浏览器不支持 Web Audio，或该元素已接入过音频图：保持原音量播放
  }
}

function applyBoost() {
  localStorage.setItem(BOOST_KEY, boostOn ? 'on' : 'off');

  for (const media of document.querySelectorAll('video, audio')) {
    const gain = boostedMedia.get(media);
    if (gain) gain.gain.value = boostOn ? 2 : 1;
    else if (boostOn) boostMedia(media);
  }
  for (const button of document.querySelectorAll('[data-boost]')) {
    button.textContent = boostLabel();
    button.classList.toggle('on', boostOn);
  }
}

// play 事件不冒泡，用捕获阶段监听：一开始播放就接入，用户无需先点开关
document.addEventListener(
  'play',
  (event) => {
    const media = event.target;
    if (media.tagName === 'VIDEO' || media.tagName === 'AUDIO') boostMedia(media);
  },
  true
);

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
  // 身份走 cookie，浏览器会自动带上；不再往 URL 里塞任何凭据
  socket = new WebSocket(`${scheme}://${location.host}/ws`);

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
        // 设备上下线时，若主机正开着「链接」浮窗就顺手刷新在线列表
        if (state.host && !el.linkModal.hidden) loadDevices();
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
  el.linkBtn.hidden = !state.host; // 只有主机能看主机链接与邀请管理
  el.qrBtn.hidden = !state.host; // 二维码 = 一次性邀请，只有主机能发
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

      if (msg.text) parts.push(`<div class="bubble">${linkify(msg.text)}</div>`);

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
        } else if (isVideo(file)) {
          parts.push(
            mediaBlock(
              `<video class="msg-video" src="${esc(url.file(state.session.id, file.id))}" controls preload="metadata" playsinline></video>`,
              file
            )
          );
        } else if (isAudio(file)) {
          parts.push(
            mediaBlock(
              `<audio class="msg-audio" src="${esc(url.file(state.session.id, file.id))}" controls preload="metadata"></audio>`,
              file
            )
          );
        } else {
          parts.push(`<div class="file-card" data-file="${esc(file.id)}">
              ${thumbHtml(file)}
              <span class="file-meta">
                <span class="file-name">${esc(file.name)}</span>
                <span class="file-sub">${formatSize(file.size)}</span>
              </span>
              ${state.host
                ? `<button type="button" class="card-btn" data-reveal="${esc(file.id)}" title="在文件夹中定位">定位</button>
                   <span class="download-tag">打开</span>`
                : '<span class="download-tag">下载</span>'}
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
              ? `<button type="button" class="file-btn" data-reveal="${esc(file.id)}" title="在文件夹中定位">
                   <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
                     <path d="M4 7a2 2 0 0 1 2-2h3.5l2 2.5H18a2 2 0 0 1 2 2V17a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2z" />
                     <path d="M12 11v5m0 0l-2-2m2 2l2-2" />
                   </svg>
                 </button>`
              : ''
          }
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

// 上次没传完的分片上传：提示用户重新选同一个文件即可继续
function notifyPendingUploads() {
  const pending = Object.keys(localStorage).filter((key) => key.startsWith(UPLOAD_PREFIX));
  if (!pending.length) return;

  const record = JSON.parse(localStorage.getItem(pending[0]) || 'null');
  if (!record?.name) return;
  const percent = record.percent ? `，已完成 ${record.percent}%` : '';
  showToast(`有未完成的上传：${record.name}${percent}，重新选择同一文件可继续`);
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

/* 扫码加入：二维码内容是一张一次性邀请链接，谁先扫谁绑定，扫过即失效 */

let qrInvite = null; // 当前二维码对应的邀请
let qrTimer = null; // 轮询邀请状态，扫码成功后页面立刻能看出来

function stopQrPolling() {
  if (qrTimer) {
    clearInterval(qrTimer);
    qrTimer = null;
  }
}

function renderQrState(invite) {
  if (!invite) {
    el.qrState.textContent = '';
    el.qrState.className = 'qr-state';
    return;
  }

  if (invite.status === '待使用') {
    el.qrState.textContent = '等待扫码加入…（扫过即失效）';
    el.qrState.className = 'qr-state pending';
    return;
  }

  if (invite.status === '已使用') {
    el.qrState.textContent = `已有人扫码加入${invite.note ? `（${invite.note}）` : ''}，再邀别人请点「换一张」`;
    el.qrState.className = 'qr-state used';
    return;
  }

  el.qrState.textContent = invite.status;
  el.qrState.className = 'qr-state';
}

function renderQrInvite(invite) {
  qrInvite = invite;
  const link = invite?.url || '';
  el.qrBox.innerHTML = link ? qrSvg(link) : '<p class="empty-tip">正在生成…</p>';
  el.qrUrl.textContent = link;
  el.qrCopy.disabled = !link;
  renderQrState(invite);
}

// 生成一张「扫码用」的邀请；旧的还没被扫就顺手撤销，避免堆一堆没人用的邀请
async function newQrInvite() {
  if (qrInvite && qrInvite.status === '待使用') {
    await apiJSON(`/api/invites/${qrInvite.id}`, { method: 'DELETE' }).catch(() => {});
  }

  const invite = await apiJSON('/api/invites', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ note: '扫码加入' }),
  });
  renderQrInvite(invite);
}

async function refreshQrStatus() {
  if (!qrInvite || el.qrModal.hidden) return;
  try {
    const data = await apiJSON('/api/invites');
    const current = (data.invites || []).find((item) => item.id === qrInvite.id);
    if (current) renderQrState(current);
  } catch {
    // 网络抖动就下次再查
  }
}

async function openQrModal() {
  openModal(el.qrModal);
  renderQrInvite(null);
  try {
    await newQrInvite();
  } catch (error) {
    showToast(error.message);
  }
  stopQrPolling();
  qrTimer = setInterval(refreshQrStatus, 3000);
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
  el.linkModal.hidden = true;
  stopQrPolling();
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

el.qrBtn.addEventListener('click', openQrModal);

el.qrRotate.addEventListener('click', async () => {
  try {
    await newQrInvite();
    showToast('已换一张，新二维码可用');
  } catch (error) {
    showToast(error.message);
  }
});

el.qrCopy.addEventListener('click', () => copyText(qrInvite?.url || ''));

el.historyBtn.addEventListener('click', () => {
  openModal(el.sessionModal);
  send({ type: 'sessions' });
});

for (const modal of [el.qrModal, el.sessionModal, el.linkModal]) {
  modal.addEventListener('click', (event) => {
    if (event.target === modal || event.target.hasAttribute('data-close')) closeModals();
  });
}

el.confirmModal.addEventListener('click', (event) => {
  if (event.target === el.confirmModal || event.target.hasAttribute('data-close')) hideConfirm();
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

/* ---------------- 主机链接与邀请（仅主机可见） ---------------- */

function qrSvg(text) {
  try {
    const qr = qrcode(0, 'M');
    qr.addData(text);
    qr.make();
    return qr.createSvgTag({ cellSize: 4, margin: 1, scalable: true });
  } catch {
    return '<p class="empty-tip">二维码生成失败</p>';
  }
}

async function copyText(text) {
  if (!text) {
    showToast('没有可复制的内容');
    return;
  }
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    // 非 HTTPS 或旧浏览器没有 clipboard API：退回临时输入框
    const temp = document.createElement('textarea');
    temp.value = text;
    document.body.appendChild(temp);
    temp.select();
    document.execCommand('copy');
    temp.remove();
  }
  showToast('已复制到剪贴板');
}

async function apiJSON(url, options) {
  const res = await fetch(url, options);
  const data = await res.json().catch(() => null);
  if (!res.ok) throw new Error(data?.error || `请求失败（HTTP ${res.status}）`);
  return data;
}

// 把 UA 压成人看得懂的「Chrome/Windows」
function shortUA(ua) {
  const browser = /Edg\//.test(ua)
    ? 'Edge'
    : /Chrome\//.test(ua)
      ? 'Chrome'
      : /Firefox\//.test(ua)
        ? 'Firefox'
        : /Safari\//.test(ua)
          ? 'Safari'
          : '浏览器';
  const os = /Windows/.test(ua)
    ? 'Windows'
    : /Android/.test(ua)
      ? 'Android'
      : /iPhone|iPad/.test(ua)
        ? 'iOS'
        : /Mac OS/.test(ua)
          ? 'macOS'
          : /Linux/.test(ua)
            ? 'Linux'
            : '';
  return os ? `${browser}/${os}` : browser;
}

function setHostLink(url) {
  state.hostLink = url || '';
  el.hostLinkInput.value = url || '（当前链接已被使用，点下面按钮生成新的）';
  el.hostQrBox.innerHTML = url ? qrSvg(url) : '';
}

async function loadHostLink() {
  try {
    const data = await apiJSON('/api/host/link?read=1');
    setHostLink(data.url);
  } catch (error) {
    showToast(error.message);
  }
}

function inviteStatusClass(status) {
  return { 待使用: 'pending', 已使用: 'used', 已撤销: 'revoked', 已过期: 'expired' }[status] || '';
}

function renderInvites() {
  if (!state.invites.length) {
    el.inviteList.innerHTML = '<li class="empty-tip">还没有发出邀请</li>';
    return;
  }

  el.inviteList.innerHTML = state.invites
    .map((invite) => {
      const time = invite.usedAt
        ? `使用于 ${formatStamp(invite.usedAt)}`
        : `创建于 ${formatStamp(invite.createdAt)} · ${formatStamp(invite.expiresAt)} 过期`;
      const activity = invite.lastSeen ? ` · 最近活动 ${formatStamp(invite.lastSeen)}` : '';

      return `<li class="invite-item">
          <span class="invite-main">
            <span class="invite-title">
              ${esc(invite.note || '未备注')}
              <em class="invite-status ${inviteStatusClass(invite.status)}">${esc(invite.status)}</em>
            </span>
            <span class="invite-sub">${time}${activity}</span>
          </span>
          ${invite.status === '已撤销' ? '' : `<button type="button" class="invite-copy" data-copy="${esc(invite.url)}">复制</button>`}
          <button type="button" class="invite-revoke" data-revoke="${esc(invite.id)}" title="撤销邀请" aria-label="撤销邀请">×</button>
        </li>`;
    })
    .join('');
}

function renderDevices() {
  if (!state.devices.length) {
    el.deviceList.innerHTML = '<li class="empty-tip">暂无记录</li>';
    return;
  }

  el.deviceList.innerHTML = state.devices
    .map((device) => {
      const title = device.name || (device.role === 'host' ? '主机' : '访客');
      const note = device.note ? ` · ${esc(device.note)}` : '';
      const last = device.lastSeen ? `最近活动 ${formatStamp(device.lastSeen)}` : '—';

      return `<li class="device-item">
          <span class="device-main">
            <span class="device-title">${esc(title)}${note}</span>
            <span class="device-sub">${esc(device.ip || '')}${device.ua ? ` · ${esc(shortUA(device.ua))}` : ''}</span>
            <span class="device-sub">${last}</span>
          </span>
          <span class="device-state ${device.online ? 'on' : ''}">${device.online ? '在线' : '离线'}</span>
        </li>`;
    })
    .join('');
}

async function loadInvites() {
  try {
    const data = await apiJSON('/api/invites');
    state.invites = data.invites || [];
    renderInvites();
  } catch (error) {
    showToast(error.message);
  }
}

async function loadDevices() {
  try {
    const data = await apiJSON('/api/devices');
    state.devices = data.devices || [];
    renderDevices();
  } catch (error) {
    showToast(error.message);
  }
}

function openLinkModal() {
  openModal(el.linkModal);
  loadHostLink();
  loadInvites();
  loadDevices();
}

el.linkBtn.addEventListener('click', openLinkModal);

el.hostLinkCopy.addEventListener('click', () => copyText(state.hostLink));

el.hostLinkRotate.addEventListener('click', async () => {
  try {
    const data = await apiJSON('/api/host/link');
    setHostLink(data.url);
    showToast('已生成新链接，旧链接立即失效');
  } catch (error) {
    showToast(error.message);
  }
});

el.inviteCreate.addEventListener('click', async () => {
  try {
    const invite = await apiJSON('/api/invites', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ note: el.inviteNote.value.trim() }),
    });
    el.inviteNote.value = '';
    await loadInvites();
    await copyText(invite.url); // 生成后直接复制，省一步操作
  } catch (error) {
    showToast(error.message);
  }
});

el.inviteList.addEventListener('click', async (event) => {
  const copy = event.target.closest('[data-copy]');
  if (copy) {
    copyText(copy.dataset.copy);
    return;
  }

  const revoke = event.target.closest('[data-revoke]');
  if (!revoke) return;
  if (!window.confirm('撤销这张邀请？对方下一次操作就会被挡在门外。')) return;

  try {
    await apiJSON(`/api/invites/${revoke.dataset.revoke}`, { method: 'DELETE' });
    await loadInvites();
    await loadDevices();
    showToast('已撤销');
  } catch (error) {
    showToast(error.message);
  }
});

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

// 大于这个大小走分片上传，支持切网/刷新后续传；小文件一次传完更省事
const CHUNK_THRESHOLD = 8 * 1024 * 1024;
const UPLOAD_PREFIX = 'lanfile.up.';

function uploadKey(file) {
  return `${UPLOAD_PREFIX}${file.name.length}-${file.size}-${file.lastModified}`;
}

function uploadFile(file, caption) {
  const item = { key: `${Date.now()}-${Math.random()}`, name: file.name, percent: 0, resuming: false };
  state.uploads.push(item);
  renderUploads();

  if (file.size > CHUNK_THRESHOLD) {
    uploadInChunks(file, caption, item).catch(() => {});
    return;
  }

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

// 分片上传：每片 8MB，逐片确认，中断后可以从已确认的位置继续
async function uploadInChunks(file, caption, item) {
  const key = uploadKey(file);
  let uploadId = '';
  let received = new Set();

  // 先看看有没有上次没传完的
  const saved = JSON.parse(localStorage.getItem(key) || 'null');
  if (saved?.uploadId) {
    try {
      const res = await fetch(`/api/upload/status?uploadId=${encodeURIComponent(saved.uploadId)}`);
      if (res.ok) {
        uploadId = saved.uploadId;
        received = new Set((await res.json()).received || []);
        item.resuming = received.size > 0;
      }
    } catch {}
  }

  if (!uploadId) {
    const res = await fetch('/api/upload/init', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: file.name, size: file.size }),
    });
    if (!res.ok) {
      failUpload(item, '初始化上传失败');
      return;
    }
    const data = await res.json();
    uploadId = data.uploadId;
    localStorage.setItem(key, JSON.stringify({ uploadId, name: file.name, size: file.size }));
  }

  const total = Math.max(1, Math.ceil(file.size / CHUNK_THRESHOLD));
  const applyProgress = (index, loaded) => {
    const done = index * CHUNK_THRESHOLD + loaded;
    item.percent = Math.min(99, Math.round((done / file.size) * 100));
    renderUploads();
  };

  for (let index = 0; index < total; index += 1) {
    if (received.has(index)) {
      applyProgress(index, CHUNK_THRESHOLD);
      continue;
    }

    const blob = file.slice(index * CHUNK_THRESHOLD, Math.min((index + 1) * CHUNK_THRESHOLD, file.size));
    const ok = await putChunk(uploadId, index, blob, (loaded) => applyProgress(index, loaded));
    if (!ok) {
      // 分片留在服务器上，下次（或重新选同一个文件）可以接着传
      localStorage.setItem(
        key,
        JSON.stringify({ uploadId, name: file.name, size: file.size, percent: item.percent })
      );
      failUpload(item, `上传中断，已完成 ${item.percent}%，重选同一文件可继续`);
      return;
    }
  }

  item.percent = 100;
  renderUploads();

  try {
    const res = await fetch('/api/upload/complete', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ uploadId, text: caption, name: state.name || '', clientId: state.clientId }),
    });
    const meta = await res.json();
    if (!res.ok) {
      failUpload(item, meta?.error || '合并文件失败');
      return;
    }
    localStorage.removeItem(key);
    if (!state.files.some((entry) => entry.id === meta.id)) {
      state.files.push(meta);
      renderFiles();
    }
    if (socket?.readyState !== WebSocket.OPEN) showToast('文件已上传，消息会在重连后同步');
  } catch {
    failUpload(item, '合并文件失败，可重试');
    return;
  }

  finishUpload(item);
}

// putChunk 上传单个分片，失败自动重试（指数退避）
function putChunk(uploadId, index, blob, onProgress, attempt = 1) {
  return new Promise((resolve) => {
    const xhr = new XMLHttpRequest();
    xhr.open('PUT', `/api/upload/chunk?uploadId=${encodeURIComponent(uploadId)}&index=${index}`);
    xhr.upload.addEventListener('progress', (event) => {
      if (event.lengthComputable) onProgress(event.loaded);
    });

    const retry = () => {
      if (attempt >= 5) {
        resolve(false);
        return;
      }
      setTimeout(() => {
        putChunk(uploadId, index, blob, onProgress, attempt + 1).then(resolve);
      }, 500 * 2 ** (attempt - 1));
    };

    xhr.addEventListener('load', () => {
      if (xhr.status === 200) {
        onProgress(blob.size);
        resolve(true);
      } else {
        retry();
      }
    });
    xhr.addEventListener('error', retry);
    xhr.addEventListener('timeout', retry);
    xhr.send(blob);
  });
}

function failUpload(item, message) {
  showToast(message);
  finishUpload(item);
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
  const reveal = event.target.closest('[data-reveal]');
  if (reveal) {
    event.stopPropagation();
    const file = findFile(reveal.dataset.reveal);
    if (file) revealFile(file);
    return;
  }

  const remove = event.target.closest('.file-del');
  if (remove) {
    event.stopPropagation();
    askDelete(remove.dataset.id);
    return;
  }

  const item = event.target.closest('.file-item');
  if (!item) return;
  const file = findFile(item.dataset.download);
  if (!file) return;
  if (state.host) openFile(file);
  else download(file);
});

document.addEventListener('click', (event) => {
  if (event.target.closest('#fileList')) return; // 文件列表面板有自己的处理

  const boost = event.target.closest('[data-boost]');
  if (boost) {
    event.stopPropagation();
    boostOn = !boostOn;
    applyBoost();
    showToast(boostOn ? '音量已增强 ×2' : '音量已还原为原始大小');
    return;
  }

  const reveal = event.target.closest('[data-reveal]');
  if (reveal) {
    const file = findFile(reveal.dataset.reveal);
    if (file) revealFile(file);
    return;
  }

  const image = event.target.closest('[data-open]');
  if (image) {
    window.open(url.file(state.session.id, image.dataset.open), '_blank');
    return;
  }

  const target = event.target.closest('[data-file], [data-download]');
  if (target && !event.target.closest('.file-del')) {
    const id = target.dataset.file || target.dataset.download;
    const file = findFile(id);
    if (!file) return;
    if (state.host) openFile(file);
    else download(file);
  }
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

  // 截图后 Ctrl+V：剪贴板里的图片直接当文件上传，链路与拖拽完全相同
  const shotExt = {
    'image/png': 'png',
    'image/jpeg': 'jpg',
    'image/gif': 'gif',
    'image/webp': 'webp',
    'image/bmp': 'bmp',
  };

  function asScreenshot(file, index) {
    const now = new Date();
    const pad = (value) => String(value).padStart(2, '0');
    const stamp = `${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}-${pad(now.getHours())}${pad(now.getMinutes())}${pad(now.getSeconds())}`;
    const name = `截图-${stamp}${index > 0 ? `-${index + 1}` : ''}.${shotExt[file.type] || 'png'}`;
    try {
      return new File([file], name, { type: file.type || 'image/png', lastModified: Date.now() });
    } catch {
      return file; // 不支持 File 构造器时退回原名（浏览器一般会给 image.png）
    }
  }

  document.addEventListener('paste', (event) => {
    const items = event.clipboardData?.items;
    if (!items) return;

    const pasted = [];
    for (const item of items) {
      if (item.kind !== 'file' || !item.type.startsWith('image/')) continue;
      const file = item.getAsFile();
      if (file) pasted.push(asScreenshot(file, pasted.length));
    }
    if (!pasted.length) return; // 剪贴板里没有图片，文本照常粘贴

    event.preventDefault();
    showToast(pasted.length > 1 ? `已粘贴 ${pasted.length} 张图片，正在上传` : '已粘贴图片，正在上传');
    handleFiles(pasted);
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
notifyPendingUploads();
