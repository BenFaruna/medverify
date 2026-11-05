// MedVerify NG web client (chat interface).
//
// This is a plain module with no dependencies and no build step, so it loads
// quickly on a slow connection. It talks only to the documented public API and
// never touches an internal endpoint.
//
// The API key is read from the page and held in memory for this tab only. It
// is deliberately not written to localStorage or a cookie: a key in browser
// storage outlives the session and is readable by any script that ends up on
// the page.

import { COPY, REVIEWED_LANGUAGES, copyFor } from "./copy.js";

const MAX_PHOTOS = 4;
const MAX_LONG_EDGE = 1568;
const JPEG_QUALITY = 0.82;
const POLL_BUDGET_MS = 120_000;

/** @type {string} */
let apiKey = "";
/** @type {string | null} */
let currentScanId = null;
/** @type {string | null} Reused when the same photos are retried. */
let idempotencyKey = null;
/** True once the current scan has a final result and questions can be asked. */
let canAsk = false;
/** True while a request is in flight. */
let busy = false;
/** @type {{file: File, url: string}[]} */
let pendingFiles = [];
let limitNote = "";
const asked = new Set();
/** @type {object | null} Last rendered scan, kept so a language change can re-render it. */
let lastView = null;
/** @type {HTMLElement | null} */
let lastResultContainer = null;

/** @type {object} */
let activeCopy = COPY.en;
/** @type {string} Language actually in use after the review gate. */
let activeLanguage = "en";

const el = {
  apiKey: document.getElementById("api-key"),
  apiBase: document.getElementById("api-base"),
  language: document.getElementById("language"),
  log: document.getElementById("log"),
  chips: document.getElementById("chips"),
  form: document.getElementById("chat-form"),
  tray: document.getElementById("tray"),
  tools: document.getElementById("tools"),
  photoBtn: document.getElementById("photo-btn"),
  photoInput: document.getElementById("photo-input"),
  question: document.getElementById("question"),
  send: document.getElementById("send"),
  hint: document.getElementById("composer-hint"),
};

function t(key, params) {
  const template = activeCopy[key] ?? COPY.en[key] ?? key;
  return template.replace(/\{(\w+)\}/g, (match, name) =>
    params && params[name] !== undefined ? String(params[name]) : match,
  );
}

// Rewrites the static page and anything already rendered for the selected
// language. Pidgin stays inactive until REVIEWED_LANGUAGES includes it.
function applyCopy() {
  const { language, copy } = copyFor(el.language?.value);
  activeCopy = copy;
  activeLanguage = language;
  document.documentElement.lang = language;
  document.title = copy.pageTitle;

  for (const node of document.querySelectorAll("[data-copy]")) {
    const value = copy[node.dataset.copy];
    if (value !== undefined) node.textContent = value;
  }
  for (const node of document.querySelectorAll("[data-copy-aria-label]")) {
    const value = copy[node.dataset.copyAriaLabel];
    if (value !== undefined) node.setAttribute("aria-label", value);
  }
  for (const node of document.querySelectorAll("[data-copy-title]")) {
    const value = copy[node.dataset.copyTitle];
    if (value !== undefined) node.setAttribute("title", value);
  }
  for (const node of document.querySelectorAll("[data-copy-content]")) {
    const value = copy[node.dataset.copyContent];
    if (value !== undefined) node.setAttribute("content", value);
  }

  const languageHint = document.getElementById("language-hint");
  if (languageHint) {
    languageHint.textContent = copy.languageHintReady;
  }

  updateComposer();
  renderChips();
  rerenderResult();
}

// The verdict wording shown to people. The API never sends a score and this
// client never invents one. No entry claims a medicine is safe or genuine.
const VERDICT_KEYS = {
  consistent_with_records: "verdictConsistent",
  mismatch_found: "verdictMismatch",
  not_found_in_records: "verdictNotFound",
  flagged: "verdictFlagged",
  needs_review: "verdictNeedsReview",
  unreadable: "verdictUnreadable",
};

// Only used to colour the stripe beside the verdict. The sentence above is
// what carries the meaning, so colour is never the only signal.
const VERDICT_TONE = {
  consistent_with_records: "ok",
  mismatch_found: "bad",
  flagged: "bad",
  not_found_in_records: "warn",
  needs_review: "warn",
  unreadable: "neutral",
};

// A glyph repeats the tone for people who cannot see the colour. The sentence
// below the icon is still the only thing that carries the meaning.
const VERDICT_MARK = {
  ok: "✓",
  bad: "!",
  warn: "!",
  neutral: "?",
};

const FIELD_LABELS = {
  nrn: "fieldNrn",
  product_name: "fieldProductName",
  manufacturer: "fieldManufacturer",
  batch_no: "fieldBatchNo",
  mfg_date: "fieldManufactured",
  expiry_date: "fieldExpiry",
};

const SUGGESTIONS = ["suggestionRecord", "suggestionIngredients", "suggestionLeaflet"];

// --- API access -----------------------------------------------------------

function apiBase() {
  return (el.apiBase.value || window.location.origin).replace(/\/+$/, "");
}

class ApiError extends Error {
  constructor(status, problem) {
    super(problem?.detail || `Request failed with status ${status}`);
    this.status = status;
    this.code = problem?.code || "unknown";
  }
}

async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  headers.set("X-API-Key", apiKey);
  const response = await fetch(apiBase() + path, { ...options, headers });

  if (response.ok) {
    if (response.status === 204) return null;
    return response.json();
  }

  let problem = null;
  try {
    problem = await response.json();
  } catch {
    // A proxy or gateway may not return a problem document.
  }
  throw new ApiError(response.status, problem);
}

function describeError(error) {
  if (!(error instanceof ApiError)) {
    return t("connectionFailed");
  }
  const messages = {
    unauthorized: "errorUnauthorized",
    insufficient_scope: "errorInsufficientScope",
    rate_limited: "errorRateLimited",
    quota_exceeded: "errorQuotaExceeded",
    too_many_images: "errorTooManyImages",
    image_too_large: "errorImageTooLarge",
    payload_too_large: "errorPayloadTooLarge",
    unsupported_format: "errorUnsupportedFormat",
    corrupt_image: "errorCorruptImage",
    invalid_language: "errorInvalidLanguage",
    unavailable: "errorUnavailable",
  };
  return t(messages[error.code] || "errorGeneric", { code: error.code });
}

// --- Photos ---------------------------------------------------------------

/**
 * Shrinks a photo before upload. Phones produce 3-8 MB files; a 1568px long
 * edge is plenty for label text and an order of magnitude smaller, which is
 * what makes the upload survivable on a poor connection.
 */
async function downscale(file) {
  if (!file.type.startsWith("image/")) return file;

  const bitmap = await createImageBitmap(file);
  const scale = Math.min(1, MAX_LONG_EDGE / Math.max(bitmap.width, bitmap.height));
  const width = Math.round(bitmap.width * scale);
  const height = Math.round(bitmap.height * scale);

  const canvas = document.createElement("canvas");
  canvas.width = width;
  canvas.height = height;
  const context = canvas.getContext("2d");
  if (!context) {
    bitmap.close();
    return file;
  }
  context.drawImage(bitmap, 0, 0, width, height);
  bitmap.close();

  const blob = await new Promise((resolve) =>
    canvas.toBlob(resolve, "image/jpeg", JPEG_QUALITY),
  );
  if (!blob) return file;

  return new File([blob], "photo.jpg", { type: "image/jpeg" });
}

function addFiles(fileList) {
  const incoming = Array.from(fileList || []);
  const room = MAX_PHOTOS - pendingFiles.length;
  for (const file of incoming.slice(0, Math.max(room, 0))) {
    pendingFiles.push({ file, url: URL.createObjectURL(file) });
  }
  limitNote =
    incoming.length > room ? t("photosFit", { max: MAX_PHOTOS }) : "";
  idempotencyKey = null;
  updateComposer();
}

function removePhoto(index) {
  const [removed] = pendingFiles.splice(index, 1);
  if (removed) URL.revokeObjectURL(removed.url);
  limitNote = "";
  idempotencyKey = null;
  updateComposer();

  const buttons = el.tray.querySelectorAll(".remove");
  const next = buttons[Math.min(index, buttons.length - 1)];
  (next || el.photoBtn).focus();
}

// --- Chat helpers ---------------------------------------------------------

function text(tag, className, content) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (content !== undefined) node.textContent = content;
  return node;
}

function scrollToEnd() {
  const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  requestAnimationFrame(() => {
    el.log.scrollTo({
      top: el.log.scrollHeight,
      behavior: reduced ? "auto" : "smooth",
    });
  });
}

function addMessage(role) {
  const row = text("div", `msg ${role}`);
  const bubble = text("div", "bubble");
  row.append(bubble);
  el.log.append(row);
  scrollToEnd();
  return { row, bubble };
}

function addUserPhotos(items) {
  const { bubble } = addMessage("user");
  bubble.classList.add("photos-bubble");
  const grid = text("div", items.length === 1 ? "photos single" : "photos");
  items.forEach((item, index) => {
    const img = document.createElement("img");
    img.src = item.url;
    img.alt = t("photoAlt", { index: index + 1 });
    grid.append(img);
  });
  bubble.append(grid);
}

function addUserText(message) {
  const { bubble } = addMessage("user");
  bubble.append(text("p", null, message));
}

/** An assistant bubble that shows a status line until a reply replaces it. */
function addPending(message) {
  const message_ = addMessage("assistant");
  const dots = text("span", "dots");
  dots.setAttribute("aria-hidden", "true");
  dots.append(document.createElement("span"), document.createElement("span"), document.createElement("span"));
  const label = text("span", null, message);
  const wrapper = text("div", "pending");
  wrapper.append(dots, label);
  message_.bubble.append(wrapper);
  return { ...message_, label };
}

function setStatus(pending, message) {
  if (pending && pending.label.textContent !== message) {
    pending.label.textContent = message;
  }
}

function settle(pending) {
  pending.bubble.replaceChildren();
}

function showError(pending, message) {
  settle(pending);
  pending.bubble.classList.add("error");
  pending.bubble.append(text("p", "error-title", t("errorTitle")));
  pending.bubble.append(text("p", null, message));
  scrollToEnd();
}

// --- Result rendering -----------------------------------------------------

function renderVerdict(view, container) {
  const verdict = view.verdict;
  if (!verdict) {
    container.append(text("p", "hint", t("noVerdict")));
    return;
  }

  const tone = VERDICT_TONE[verdict.value] || "warn";
  const block = text("div", "verdict");
  block.dataset.tone = tone;

  const head = text("div", "verdict-head");
  const mark = text("span", "verdict-mark", VERDICT_MARK[tone] || "!");
  mark.setAttribute("aria-hidden", "true");
  const copy = text("div", "verdict-copy");
  copy.append(text("p", "verdict-kicker", t("resultKicker")));
  const verdictKey = VERDICT_KEYS[verdict.value] || "verdictNeedsLook";
  copy.append(text("h2", null, t(verdictKey)));
  head.append(mark, copy);

  block.append(head);
  block.append(text("p", "confidence", t("confidence", { value: verdict.confidence })));
  container.append(block);
}

function renderFlags(flags, container) {
  if (!Array.isArray(flags) || flags.length === 0) return;

  container.append(text("h3", null, t("flagsHeading")));
  for (const flag of flags) {
    const item = text("div", "flag");
    item.append(
      text(
        "p",
        null,
        t("flagSeverity", {
          severity: flag.severity,
          type: flag.type.replaceAll("_", " "),
        }),
      ),
    );

    // Every flag must cite where it came from. Keep the link text meaningful.
    const source = flag.source || {};
    if (source.url) {
      const link = document.createElement("a");
      link.href = source.url;
      link.rel = "noopener noreferrer";
      link.target = "_blank";
      link.textContent = source.title || t("sourceDocument");
      item.append(link);
    } else if (source.title) {
      item.append(text("p", "hint", source.title));
    }
    container.append(item);
  }
}

function renderReasons(reasons, container) {
  if (!Array.isArray(reasons) || reasons.length === 0) return;

  const list = document.createElement("ul");
  for (const reason of reasons) {
    // Show the reviewed sentence, never the code.
    if (reason.text) list.append(text("li", null, reason.text));
  }
  if (list.childElementCount > 0) {
    container.append(text("h3", null, t("whyHeading")));
    container.append(list);
  }
}

// Dates arrive as parsed objects, not strings, so they need formatting before
// they reach the page. Month-only labels must not gain a made-up day.
function formatFieldValue(value) {
  if (value && typeof value === "object") {
    if (typeof value.year === "number" && typeof value.month === "number") {
      const year = value.year;
      const month = String(value.month).padStart(2, "0");
      if (value.precision === "day" && typeof value.day === "number") {
        return `${year}-${month}-${String(value.day).padStart(2, "0")}`;
      }
      return `${year}-${month}`;
    }
    if (typeof value.raw === "string" && value.raw) return value.raw;
  }
  return String(value);
}

function renderExtracted(extracted, container) {
  if (!extracted) return;

  const rows = [];
  for (const [key, labelKey] of Object.entries(FIELD_LABELS)) {
    const field = extracted[key];
    if (!field || !field.value) continue;
    const confidence =
      typeof field.confidence === "number"
        ? ` (${Math.round(field.confidence * 100)}%)`
        : "";
    rows.push([t(labelKey), `${formatFieldValue(field.value)}${confidence}`]);
  }
  if (rows.length === 0) return;

  container.append(text("h3", null, t("readFromPack")));
  const list = text("dl", "fields");
  for (const [label, value] of rows) {
    const row = document.createElement("div");
    row.append(text("dt", null, label));
    row.append(text("dd", null, value));
    list.append(row);
  }
  container.append(list);
}

function renderRegistry(features, container) {
  if (!features || !features.outcome) return;

  container.append(text("h3", null, t("nafdacRecords")));
  const product = features.product;
  if (product) {
    const parts = [product.name, product.nrn && `NRN ${product.nrn}`, product.status]
      .filter(Boolean)
      .join(" · ");
    container.append(text("p", null, parts));
  } else {
    const outcomeCopy = {
      not_found_in_dataset: "recordNotFound",
      candidate_only: "recordCandidateOnly",
      ambiguous: "recordAmbiguous",
      unreadable_input: "recordUnreadableInput",
    };
    container.append(
      text("p", null, t(outcomeCopy[features.outcome] || "recordInconclusive")),
    );
  }

  if (features.dataset && features.dataset.products) {
    container.append(
      text(
        "p",
        "hint",
        t("comparedAgainst", { count: features.dataset.products }),
      ),
    );
  }
}

function isTerminal(status) {
  return ["completed", "unreadable", "failed"].includes(status);
}

function renderResult(view, container) {
  container.replaceChildren();
  renderVerdict(view, container);
  renderReasons(view.reasons, container);
  renderFlags(view.flags, container);
  renderExtracted(view.extracted, container);
  renderRegistry(view.features, container);

  if (view.disclaimer) {
    container.append(text("p", "hint", view.disclaimer));
  }
}

function rerenderResult() {
  if (lastView && lastResultContainer && lastResultContainer.isConnected) {
    renderResult(lastView, lastResultContainer);
  }
}

function showResult(pending, view) {
  settle(pending);
  pending.row.classList.add("wide");

  const container = text("div", "result");
  renderResult(view, container);
  pending.bubble.append(container);

  lastView = view;
  lastResultContainer = container;
  scrollToEnd();
}

// --- Upload and poll ------------------------------------------------------

async function poll(scanId, pending) {
  const started = Date.now();
  let delay = 700;

  while (Date.now() - started < POLL_BUDGET_MS) {
    const view = await api(`/v1/scans/${scanId}`);
    if (isTerminal(view.status)) return view;

    setStatus(pending, t("stillChecking"));
    await new Promise((resolve) => setTimeout(resolve, delay));
    // Back off gently so a slow scan does not hammer the API.
    delay = Math.min(delay * 1.6, 5000);
  }
  return null;
}

async function sendPack() {
  apiKey = el.apiKey.value.trim();
  const batch = pendingFiles.slice(0, MAX_PHOTOS);
  if (batch.length === 0) return;

  if (!apiKey) {
    const note = addMessage("assistant");
    note.bubble.classList.add("error");
    note.bubble.append(text("p", null, t("noApiKey")));
    return;
  }

  busy = true;
  limitNote = "";
  addUserPhotos(batch);
  pendingFiles = [];
  updateComposer();
  const pending = addPending(t("preparingPhotos"));

  try {
    const form = new FormData();
    for (const item of batch) {
      form.append("images", await downscale(item.file));
    }
    form.append("language", activeLanguage);

    setStatus(pending, t("uploading"));
    // The same key makes a retry safe: the API returns the first scan instead
    // of creating a duplicate. It is cleared when the photos change.
    idempotencyKey ??= crypto.randomUUID();
    const response = await fetch(`${apiBase()}/v1/scans`, {
      method: "POST",
      headers: {
        "X-API-Key": apiKey,
        "Idempotency-Key": idempotencyKey,
      },
      body: form,
    });

    if (!response.ok) {
      let problem = null;
      try {
        problem = await response.json();
      } catch {
        // Fall through to the generic message below.
      }
      throw new ApiError(response.status, problem);
    }

    const created = await response.json();
    currentScanId = created.id;
    idempotencyKey = null;

    let view = created;
    if (!isTerminal(view.status)) {
      view = await poll(currentScanId, pending);
    }

    if (view) {
      showResult(pending, view);
      canAsk = true;
      asked.clear();
      renderChips();
    } else {
      showError(pending, t("takingTooLong"));
    }
  } catch (error) {
    // Keep the photos so the person can send them again without re-picking.
    pendingFiles = batch;
    showError(pending, describeError(error));
  } finally {
    busy = false;
    updateComposer();
  }
}

// --- Questions ------------------------------------------------------------

function showAnswer(pending, answer) {
  settle(pending);

  const body = text("p", "answer-text", answer.answer);
  if (answer.fallback || !answer.verified) body.classList.add("fixed");
  pending.bubble.append(body);

  if (Array.isArray(answer.sources) && answer.sources.length > 0) {
    const citations = text("div", "citations");
    citations.append(text("p", null, t("sources")));
    const list = document.createElement("ul");
    for (const source of answer.sources) {
      const item = document.createElement("li");
      const label = `${source.label}: ${source.title || source.kind}`;
      if (source.url) {
        const link = document.createElement("a");
        link.href = source.url;
        link.rel = "noopener noreferrer";
        link.target = "_blank";
        link.textContent = label;
        item.append(link);
      } else {
        item.textContent = label;
      }
      list.append(item);
    }
    citations.append(list);
    pending.bubble.append(citations);
  }
  scrollToEnd();
}

async function sendQuestion(question) {
  if (busy || !canAsk || !currentScanId || !question) return;

  busy = true;
  asked.add(question);
  addUserText(question);
  el.question.value = "";
  renderChips();
  updateComposer();
  const pending = addPending(t("checkingRecords"));

  try {
    const answer = await api(`/v1/scans/${currentScanId}/questions`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ question }),
    });
    showAnswer(pending, answer);
  } catch (error) {
    // Give the question back so a retry is one tap.
    el.question.value = question;
    asked.delete(question);
    renderChips();
    showError(pending, describeError(error));
  } finally {
    busy = false;
    updateComposer();
  }
}

function renderChips() {
  el.chips.replaceChildren();
  if (!canAsk) return;

  for (const key of SUGGESTIONS) {
    const suggestion = t(key);
    if (asked.has(suggestion)) continue;
    const button = text("button", "chip", suggestion);
    button.type = "button";
    button.addEventListener("click", () => sendQuestion(suggestion));
    el.chips.append(button);
  }

  const another = text("button", "chip new", t("checkAnotherPack"));
  another.type = "button";
  another.addEventListener("click", reset);
  el.chips.append(another);
}

// --- Composer state -------------------------------------------------------

function photoHint() {
  const count = pendingFiles.length;
  let message =
    count === 0
      ? t("addUpTo", { max: MAX_PHOTOS })
      : t("photosReadyCount", { count, max: MAX_PHOTOS });
  if (limitNote) message += ` ${limitNote}`;
  return message;
}

function renderTray() {
  el.tray.replaceChildren();
  el.tray.hidden = pendingFiles.length === 0 || canAsk;

  pendingFiles.forEach((item, index) => {
    const li = document.createElement("li");
    const img = document.createElement("img");
    img.src = item.url;
    img.alt = t("photoThumbAlt", { index: index + 1 });
    const remove = text("button", "remove", "×");
    remove.type = "button";
    remove.setAttribute("aria-label", t("removePhoto", { index: index + 1 }));
    remove.addEventListener("click", () => removePhoto(index));
    li.append(img, remove);
    el.tray.append(li);
  });
}

function updateComposer() {
  el.tools.hidden = canAsk;
  el.photoBtn.disabled = busy;

  const count = pendingFiles.length;
  el.photoBtn.setAttribute(
    "aria-label",
    count === 0
      ? t("addPhotosOfPack")
      : t("addPhotosSelected", { count, max: MAX_PHOTOS }),
  );

  el.question.disabled = !canAsk;
  el.question.placeholder = canAsk
    ? t("askAboutPack")
    : t("addPhotosToBegin");

  renderTray();

  const ready = canAsk
    ? el.question.value.trim().length > 0
    : pendingFiles.length > 0;
  el.send.setAttribute("aria-disabled", String(busy || !ready));
  el.send.setAttribute("aria-label", canAsk ? t("ask") : t("checkThisPack"));

  // Replacing the node on every keystroke would re-announce the hint, so only
  // write when the wording actually changes.
  const hintText = canAsk ? t("askHelp") : photoHint();
  if (el.hint.textContent !== hintText) el.hint.textContent = hintText;
}

function reset() {
  if (busy) return;
  currentScanId = null;
  canAsk = false;
  idempotencyKey = null;
  limitNote = "";
  lastView = null;
  lastResultContainer = null;
  asked.clear();
  for (const item of pendingFiles) URL.revokeObjectURL(item.url);
  pendingFiles = [];
  el.question.value = "";
  el.chips.replaceChildren();

  const note = addMessage("assistant");
  note.bubble.append(text("p", null, t("readyNextPack")));

  updateComposer();
  el.photoBtn.focus();
}

// --- Wiring ---------------------------------------------------------------

function init() {
  // Default the API address to the page's own origin: the API serves this
  // client in the MVP, so same-origin avoids a CORS preflight entirely.
  el.apiBase.value = window.location.origin;

  el.photoBtn.addEventListener("click", () => el.photoInput.click());
  el.photoInput.addEventListener("change", () => {
    addFiles(el.photoInput.files);
    // Clear it so choosing the same photo again still fires "change".
    el.photoInput.value = "";
  });

  el.question.addEventListener("input", updateComposer);
  el.language.addEventListener("change", applyCopy);
  el.form.addEventListener("submit", (event) => {
    event.preventDefault();
    if (busy || el.send.getAttribute("aria-disabled") === "true") return;
    if (canAsk) {
      sendQuestion(el.question.value.trim());
    } else {
      sendPack();
    }
  });

  applyCopy();
}

init();
