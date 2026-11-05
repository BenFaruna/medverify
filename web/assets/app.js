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

// The verdict wording shown to people. The API never sends a score and this
// client never invents one. No entry claims a medicine is safe or genuine.
const VERDICT_COPY = {
  consistent_with_records:
    "The details printed on this pack agree with the NAFDAC record found for it.",
  mismatch_found:
    "A detail printed on this pack disagrees with the NAFDAC record found for it.",
  not_found_in_records:
    "No matching record was found in our NAFDAC data. Our data can be incomplete, so this does not mean the pack is fake.",
  flagged: "A NAFDAC alert or a safety check applies to this pack.",
  needs_review:
    "The evidence is ambiguous or incomplete, so this pack needs a closer look.",
  unreadable: "The photos could not be read clearly. Please take another photo.",
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
  nrn: "NRN",
  product_name: "Product name",
  manufacturer: "Manufacturer",
  batch_no: "Batch number",
  mfg_date: "Manufactured",
  expiry_date: "Expiry",
};

const SUGGESTIONS = [
  "What does the NAFDAC record say about this product?",
  "What are the listed ingredients?",
  "Where can I read the leaflet?",
];

const ASK_HELP =
  "Answers come only from NAFDAC records and leaflets, with citations. This is not medical advice.";

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
    return "The connection failed. Check your network and try again; the same photos will not create a second scan.";
  }
  const messages = {
    unauthorized: "That API key was not accepted.",
    insufficient_scope: "This API key cannot create scans.",
    rate_limited: "Too many requests. Wait a moment and try again.",
    quota_exceeded: "This API key has used its daily allowance.",
    too_many_images: "Send at most four photos.",
    image_too_large: "One photo is too large even after resizing.",
    payload_too_large: "The upload is too large. Try fewer photos.",
    unsupported_format: "Use JPEG, PNG, or WebP photos.",
    corrupt_image: "One photo could not be read. Try taking it again.",
    invalid_language: "That language is not supported.",
    unavailable: "The service is temporarily unavailable. Try again shortly.",
  };
  return messages[error.code] || `Something went wrong (${error.code}). Try again.`;
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
    incoming.length > room ? `Only ${MAX_PHOTOS} photos fit in one check.` : "";
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
    img.alt = `Photo ${index + 1} of the pack`;
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
  pending.bubble.append(text("p", "error-title", "That did not work"));
  pending.bubble.append(text("p", null, message));
  scrollToEnd();
}

// --- Result rendering -----------------------------------------------------

function renderVerdict(view, container) {
  const verdict = view.verdict;
  if (!verdict) {
    container.append(text("p", "hint", "This pack has no verdict yet."));
    return;
  }

  const tone = VERDICT_TONE[verdict.value] || "warn";
  const block = text("div", "verdict");
  block.dataset.tone = tone;

  const head = text("div", "verdict-head");
  const mark = text("span", "verdict-mark", VERDICT_MARK[tone] || "!");
  mark.setAttribute("aria-hidden", "true");
  const copy = text("div", "verdict-copy");
  copy.append(text("p", "verdict-kicker", "Result"));
  copy.append(text("h2", null, VERDICT_COPY[verdict.value] || "This pack needs a closer look."));
  head.append(mark, copy);

  block.append(head);
  block.append(text("p", "confidence", `Confidence: ${verdict.confidence}`));
  container.append(block);
}

function renderFlags(flags, container) {
  if (!Array.isArray(flags) || flags.length === 0) return;

  container.append(text("h3", null, "Flags"));
  for (const flag of flags) {
    const item = text("div", "flag");
    item.append(text("p", null, `A ${flag.severity} severity finding: ${flag.type.replaceAll("_", " ")}.`));

    // Every flag must cite where it came from. Keep the link text meaningful.
    const source = flag.source || {};
    if (source.url) {
      const link = document.createElement("a");
      link.href = source.url;
      link.rel = "noopener noreferrer";
      link.target = "_blank";
      link.textContent = source.title || "Source document";
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
    container.append(text("h3", null, "Why"));
    container.append(list);
  }
}

function renderExtracted(extracted, container) {
  if (!extracted) return;

  const rows = [];
  for (const [key, label] of Object.entries(FIELD_LABELS)) {
    const field = extracted[key];
    if (!field || !field.value) continue;
    const confidence =
      typeof field.confidence === "number"
        ? ` (${Math.round(field.confidence * 100)}%)`
        : "";
    rows.push([label, `${field.value}${confidence}`]);
  }
  if (rows.length === 0) return;

  container.append(text("h3", null, "Read from the pack"));
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

  container.append(text("h3", null, "NAFDAC records"));
  const product = features.product;
  if (product) {
    const parts = [product.name, product.nrn && `NRN ${product.nrn}`, product.status]
      .filter(Boolean)
      .join(" · ");
    container.append(text("p", null, parts));
  } else {
    const outcomeCopy = {
      not_found_in_dataset:
        "No record matched the read details. Our dataset can be incomplete, so this is not proof of a problem.",
      candidate_only: "A possible match was found but it was not confirmed.",
      ambiguous: "More than one possible match was found.",
      unreadable_input: "The pack details could not be read well enough to compare.",
    };
    container.append(
      text("p", null, outcomeCopy[features.outcome] || "The record comparison was inconclusive."),
    );
  }

  if (features.dataset && features.dataset.products) {
    container.append(
      text("p", "hint", `Compared against ${features.dataset.products} NAFDAC records.`),
    );
  }
}

function isTerminal(status) {
  return ["completed", "unreadable", "failed"].includes(status);
}

function showResult(pending, view) {
  settle(pending);
  pending.row.classList.add("wide");

  const container = text("div", "result");
  renderVerdict(view, container);
  renderReasons(view.reasons, container);
  renderFlags(view.flags, container);
  renderExtracted(view.extracted, container);
  renderRegistry(view.features, container);

  if (view.disclaimer) {
    container.append(text("p", "hint", view.disclaimer));
  }

  pending.bubble.append(container);
  scrollToEnd();
}

// --- Upload and poll ------------------------------------------------------

async function poll(scanId, pending) {
  const started = Date.now();
  let delay = 700;

  while (Date.now() - started < POLL_BUDGET_MS) {
    const view = await api(`/v1/scans/${scanId}`);
    if (isTerminal(view.status)) return view;

    setStatus(pending, "Still checking. This can take a few seconds…");
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
    note.bubble.append(text("p", null, "No API key is set for this page."));
    return;
  }

  busy = true;
  limitNote = "";
  addUserPhotos(batch);
  pendingFiles = [];
  updateComposer();
  const pending = addPending("Preparing your photos…");

  try {
    const form = new FormData();
    for (const item of batch) {
      form.append("images", await downscale(item.file));
    }
    if (el.language.value) form.append("language", el.language.value);

    setStatus(pending, "Uploading…");
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
      showError(pending, "This is taking longer than usual. Reload the page to check again.");
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
    citations.append(text("p", null, "Sources:"));
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
  const pending = addPending("Checking the records…");

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

  for (const suggestion of SUGGESTIONS) {
    if (asked.has(suggestion)) continue;
    const button = text("button", "chip", suggestion);
    button.type = "button";
    button.addEventListener("click", () => sendQuestion(suggestion));
    el.chips.append(button);
  }

  const another = text("button", "chip new", "Check another pack");
  another.type = "button";
  another.addEventListener("click", reset);
  el.chips.append(another);
}

// --- Composer state -------------------------------------------------------

function photoHint() {
  const count = pendingFiles.length;
  let message =
    count === 0
      ? `Add up to ${MAX_PHOTOS} photos: front, back and a side.`
      : `${count} of ${MAX_PHOTOS} photos ready.`;
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
    img.alt = `Photo ${index + 1}`;
    const remove = text("button", "remove", "×");
    remove.type = "button";
    remove.setAttribute("aria-label", `Remove photo ${index + 1}`);
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
      ? "Add photos of the pack"
      : `Add photos of the pack, ${count} of ${MAX_PHOTOS} selected`,
  );

  el.question.disabled = !canAsk;
  el.question.placeholder = canAsk ? "Ask about this pack" : "Add photos to begin";

  renderTray();

  const ready = canAsk
    ? el.question.value.trim().length > 0
    : pendingFiles.length > 0;
  el.send.setAttribute("aria-disabled", String(busy || !ready));
  el.send.setAttribute("aria-label", canAsk ? "Ask" : "Check this pack");

  // Replacing the node on every keystroke would re-announce the hint, so only
  // write when the wording actually changes.
  const hintText = canAsk ? ASK_HELP : photoHint();
  if (el.hint.textContent !== hintText) el.hint.textContent = hintText;
}

function reset() {
  if (busy) return;
  currentScanId = null;
  canAsk = false;
  idempotencyKey = null;
  limitNote = "";
  asked.clear();
  for (const item of pendingFiles) URL.revokeObjectURL(item.url);
  pendingFiles = [];
  el.question.value = "";
  el.chips.replaceChildren();

  const note = addMessage("assistant");
  note.bubble.append(text("p", null, "Ready for the next pack. Add its photos below."));

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
  el.form.addEventListener("submit", (event) => {
    event.preventDefault();
    if (busy || el.send.getAttribute("aria-disabled") === "true") return;
    if (canAsk) {
      sendQuestion(el.question.value.trim());
    } else {
      sendPack();
    }
  });

  updateComposer();
}

init();
