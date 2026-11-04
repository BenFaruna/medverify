// MedVerify NG web client.
//
// This is a plain module with no dependencies and no build step, so it loads
// quickly on a slow connection. It talks only to the documented public API and
// never touches an internal endpoint.
//
// The API key is held in memory for this tab only. It is deliberately not
// written to localStorage or a cookie: a key in browser storage outlives the
// session and is readable by any script that ends up on the page.

const MAX_PHOTOS = 4;
const MAX_LONG_EDGE = 1568;
const JPEG_QUALITY = 0.82;
const POLL_BUDGET_MS = 120_000;

/** @type {string} */
let apiKey = "";
/** @type {string | null} */
let currentScanId = null;

const el = {
  apiKey: document.getElementById("api-key"),
  apiBase: document.getElementById("api-base"),
  photos: document.getElementById("photos"),
  photoCount: document.getElementById("photo-count"),
  language: document.getElementById("language"),
  submit: document.getElementById("submit"),
  status: document.getElementById("status"),
  resultSection: document.getElementById("result-section"),
  result: document.getElementById("result"),
  retake: document.getElementById("retake"),
  askSection: document.getElementById("ask-section"),
  suggestions: document.getElementById("suggestions"),
  askForm: document.getElementById("ask-form"),
  question: document.getElementById("question"),
  answers: document.getElementById("answers"),
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

// --- Photos ---------------------------------------------------------------

function setStatus(message) {
  el.status.textContent = message;
}

function updatePhotoCount() {
  const count = el.photos.files ? el.photos.files.length : 0;
  if (count === 0) {
    el.photoCount.textContent = "No photos selected.";
    return;
  }
  const total = Math.min(count, MAX_PHOTOS);
  el.photoCount.textContent =
    `${total} photo${total === 1 ? "" : "s"} selected` +
    (count > MAX_PHOTOS ? ` (only the first ${MAX_PHOTOS} are sent)` : "");
}

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

// --- Result rendering -----------------------------------------------------

function text(tag, className, content) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (content !== undefined) node.textContent = content;
  return node;
}

function renderVerdict(view, container) {
  const verdict = view.verdict;
  if (!verdict) {
    container.append(
      text("p", "hint", "This pack has no verdict yet."),
    );
    return;
  }

  const block = text("div", "verdict");
  block.append(text("h3", null, VERDICT_COPY[verdict.value] || "This pack needs a closer look."));
  block.append(text("p", "confidence", `Confidence: ${verdict.confidence}`));
  container.append(block);
}

function renderFlags(flags, container) {
  if (!Array.isArray(flags) || flags.length === 0) return;

  container.append(text("h4", null, "Flags"));
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

  container.append(text("h4", null, "Why"));
  const list = document.createElement("ul");
  for (const reason of reasons) {
    // Show the reviewed sentence, never the code.
    if (reason.text) list.append(text("li", null, reason.text));
  }
  if (list.childElementCount > 0) container.append(list);
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

  container.append(text("h4", null, "Read from the pack"));
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

  container.append(text("h4", null, "NAFDAC records"));
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

function renderResult(view) {
  const container = el.result;
  container.replaceChildren();

  const status = view.status;
  if (status === "queued" || status === "processing") {
    container.append(text("p", null, "Still checking this pack…"));
  }

  renderVerdict(view, container);
  renderReasons(view.reasons, container);
  renderFlags(view.flags, container);
  renderExtracted(view.extracted, container);
  renderRegistry(view.features, container);

  if (view.disclaimer) {
    container.append(text("p", "hint", view.disclaimer));
  }

  el.resultSection.hidden = false;
  const terminal = ["completed", "unreadable", "failed"].includes(status);
  el.askSection.hidden = !terminal;
  if (terminal) {
    el.resultSection.querySelector("h2").focus?.();
  }
}

// --- Upload and poll ------------------------------------------------------

async function poll(scanId) {
  const started = Date.now();
  let delay = 700;

  while (Date.now() - started < POLL_BUDGET_MS) {
    const view = await api(`/v1/scans/${scanId}`);
    renderResult(view);
    if (["completed", "unreadable", "failed"].includes(view.status)) return view;

    setStatus("Still checking. This can take a few seconds…");
    await new Promise((resolve) => setTimeout(resolve, delay));
    // Back off gently so a slow scan does not hammer the API.
    delay = Math.min(delay * 1.6, 5000);
  }

  setStatus("This is taking longer than usual. Reload the page to check again.");
  return null;
}

async function upload(event) {
  event.preventDefault();

  apiKey = el.apiKey.value.trim();
  if (!apiKey) {
    setStatus("Enter your API key first.");
    el.apiKey.focus();
    return;
  }
  const files = Array.from(el.photos.files || []).slice(0, MAX_PHOTOS);
  if (files.length === 0) {
    setStatus("Choose at least one photo of the pack.");
    el.photos.focus();
    return;
  }

  el.submit.disabled = true;
  el.askSection.hidden = true;
  setStatus("Preparing your photos…");

  try {
    const form = new FormData();
    for (const file of files) {
      form.append("images", await downscale(file));
    }
    if (el.language.value) form.append("language", el.language.value);

    setStatus("Uploading…");
    // The same key makes a retry safe: the API returns the first scan instead
    // of creating a duplicate.
    const idempotencyKey = crypto.randomUUID();
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
    renderResult(created);

    if (created.status === "queued" || created.status === "processing") {
      await poll(currentScanId);
    }
    setStatus("");
    renderSuggestions();
  } catch (error) {
    setStatus(describeError(error));
  } finally {
    el.submit.disabled = false;
    updatePhotoCount();
  }
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

// --- Questions ------------------------------------------------------------

function renderSuggestions() {
  el.suggestions.replaceChildren();
  if (!currentScanId) return;

  for (const suggestion of SUGGESTIONS) {
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = suggestion;
    button.addEventListener("click", () => {
      el.question.value = suggestion;
      el.question.focus();
    });
    el.suggestions.append(button);
  }
}

function renderAnswer(answer) {
  const block = text("div", "answer");

  const body = text("p", null, answer.answer);
  if (answer.fallback || !answer.verified) body.className = "fixed";
  block.append(body);

  if (Array.isArray(answer.sources) && answer.sources.length > 0) {
    const citations = text("div", "citations");
    citations.append(text("p", null, "Sources:"));
    const list = document.createElement("ul");
    for (const source of answer.sources) {
      const item = document.createElement("li");
      if (source.url) {
        const link = document.createElement("a");
        link.href = source.url;
        link.rel = "noopener noreferrer";
        link.target = "_blank";
        link.textContent = `${source.label}: ${source.title || source.kind}`;
        item.append(link);
      } else {
        item.textContent = `${source.label}: ${source.title || source.kind}`;
      }
      list.append(item);
    }
    citations.append(list);
    block.append(citations);
  }

  el.answers.prepend(block);
}

async function ask(event) {
  event.preventDefault();
  const question = el.question.value.trim();
  if (!question || !currentScanId) return;

  const button = el.askForm.querySelector("button");
  button.disabled = true;
  try {
    const answer = await api(`/v1/scans/${currentScanId}/questions`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ question }),
    });
    renderAnswer(answer);
    el.question.value = "";
  } catch (error) {
    el.answers.prepend(text("div", "answer", describeError(error)));
  } finally {
    button.disabled = false;
  }
}

// --- Wiring ---------------------------------------------------------------

function reset() {
  currentScanId = null;
  el.result.replaceChildren();
  el.answers.replaceChildren();
  el.resultSection.hidden = true;
  el.askSection.hidden = true;
  el.photos.value = "";
  updatePhotoCount();
  setStatus("");
  el.photos.focus();
}

function init() {
  // Default the API address to the page's own origin: the API serves this
  // client in the MVP, so same-origin avoids a CORS preflight entirely.
  el.apiBase.value = window.location.origin;
  updatePhotoCount();

  el.photos.addEventListener("change", updatePhotoCount);
  el.submit.addEventListener("click", upload);
  el.retake.addEventListener("click", reset);
  el.askForm.addEventListener("submit", ask);
}

init();
