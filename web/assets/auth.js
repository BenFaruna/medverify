// Account pages: sign-in, registration, and API key management.
//
// Plain module with no dependencies and no build step, like the chat client.
// It talks only to the documented account endpoints and always sends cookies
// same-origin, so the session never has to be readable by script.
//
// The API key is displayed once, straight from the response that created it. It
// is never written to localStorage, a cookie, or the URL: a key kept in browser
// storage outlives the tab and is readable by anything that ends up on the
// page. The server stores only a hash, so the page cannot fetch it again.
//
// The account screens are English-only for now. The reviewed Pidgin copy in
// copy.js covers the scan conversation; translating these forms is a separate
// review, and half-translated security wording would be worse than none.

const MIN_PASSWORD_BYTES = 12;

/** @type {Promise<void> | null} */
let inFlight = null;

class ApiError extends Error {
  constructor(status, problem) {
    super(problem?.detail || `Request failed with status ${status}`);
    this.status = status;
    this.code = problem?.code || "unknown";
    this.detail = problem?.detail || "";
  }
}

async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  if (options.body !== undefined) headers.set("Content-Type", "application/json");

  const response = await fetch(path, {
    ...options,
    headers,
    credentials: "same-origin",
  });

  if (response.ok) {
    if (response.status === 204) return null;
    return response.json();
  }

  let problem = null;
  try {
    problem = await response.json();
  } catch {
    // A proxy or gateway may not answer with a problem document.
  }
  throw new ApiError(response.status, problem);
}

// Wording for the cases a person can act on. The server's own detail is used
// for form validation, which is the only place it is written for a human.
const CODE_MESSAGES = {
  conflict: "That email address already has an account. Sign in instead.",
  unauthorized: "That email address and password do not match an account.",
  rate_limited: "Too many attempts from this connection. Wait a minute and try again.",
  forbidden: "This request was blocked. Reload the page and try again.",
  unavailable: "The service is temporarily unavailable. Try again shortly.",
  payload_too_large: "That request was too large.",
};

function describeError(error) {
  if (!(error instanceof ApiError)) {
    return "The connection failed. Check your network and try again.";
  }
  if (error.code === "bad_request" && error.detail) return error.detail;
  return CODE_MESSAGES[error.code] || "Something went wrong. Try again.";
}

function showError(node, message) {
  if (!node) return;
  node.textContent = message;
  node.hidden = !message;
}

// nextAfterSignIn returns the page to open once the session exists. Only a
// same-origin path is accepted: honouring an absolute URL would turn the
// sign-in page into an open redirect that lends our domain to someone else.
function nextAfterSignIn() {
  const raw = new URLSearchParams(window.location.search).get("next");
  if (!raw || !raw.startsWith("/") || raw.startsWith("//")) return "/settings";
  return raw;
}

function formatDate(value) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

function setBusy(button, busy, busyLabel) {
  if (!button) return;
  if (busy) {
    button.dataset.idleLabel ??= button.textContent;
    button.textContent = busyLabel;
    button.setAttribute("aria-disabled", "true");
    button.disabled = true;
  } else {
    if (button.dataset.idleLabel) button.textContent = button.dataset.idleLabel;
    button.removeAttribute("aria-disabled");
    button.disabled = false;
  }
}

/**
 * Copies the key from its read-only field. The Clipboard API needs a secure
 * origin, so a plain-HTTP local run falls back to selecting the text and telling
 * the person which keys to press. Either way the value never leaves the field.
 */
async function copyKey(input, status, button) {
  if (!input?.value) return;
  try {
    await navigator.clipboard.writeText(input.value);
    if (status) status.textContent = "Copied to the clipboard.";
  } catch {
    input.focus();
    input.select();
    const mac = /mac/i.test(navigator.platform || "");
    if (status) status.textContent = `Press ${mac ? "⌘C" : "Ctrl+C"} to copy the selected key.`;
  }
  if (button) {
    const label = button.textContent;
    button.textContent = "Copied";
    setTimeout(() => {
      button.textContent = label;
    }, 1500);
  }
}

// --- Shared form handling -------------------------------------------------

function readForm(form) {
  const data = new FormData(form);
  return {
    email: String(data.get("email") || "").trim(),
    password: String(data.get("password") || ""),
    name: String(data.get("name") || "").trim(),
    confirm: String(data.get("confirm") || ""),
  };
}

function initLogin() {
  const form = document.getElementById("login-form");
  const errorNode = document.getElementById("form-error");
  const submit = document.getElementById("submit");
  if (!form) return;

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (inFlight) return;
    showError(errorNode, "");

    const { email, password } = readForm(form);
    if (!email || !password) {
      showError(errorNode, "Enter your email address and password.");
      (email ? document.getElementById("password") : document.getElementById("email")).focus();
      return;
    }

    setBusy(submit, true, "Signing in…");
    try {
      inFlight = api("/v1/auth/login", {
        method: "POST",
        body: JSON.stringify({ email, password }),
      });
      await inFlight;
      window.location.assign(nextAfterSignIn());
    } catch (error) {
      showError(errorNode, describeError(error));
      document.getElementById("password").select();
    } finally {
      inFlight = null;
      setBusy(submit, false);
    }
  });

  // Already signed in, perhaps after a redirect from a chat page: skip the
  // form entirely. A failure here is the ordinary signed-out case.
  api("/v1/account")
    .then(() => window.location.replace(nextAfterSignIn()))
    .catch(() => {});

  document.getElementById("email")?.focus();
}

function initRegister() {
  const form = document.getElementById("register-form");
  const errorNode = document.getElementById("form-error");
  const submit = document.getElementById("submit");
  const formPanel = document.getElementById("register-panel");
  const keyPanel = document.getElementById("key-panel");
  if (!form) return;

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (inFlight) return;
    showError(errorNode, "");

    const { email, password, name, confirm } = readForm(form);
    if (!email) {
      showError(errorNode, "Enter your email address.");
      document.getElementById("email").focus();
      return;
    }
    if (password.length < MIN_PASSWORD_BYTES) {
      showError(errorNode, `Use a password of at least ${MIN_PASSWORD_BYTES} characters.`);
      document.getElementById("password").focus();
      return;
    }
    if (password !== confirm) {
      showError(errorNode, "The two passwords do not match.");
      document.getElementById("confirm").focus();
      return;
    }

    setBusy(submit, true, "Creating your account…");
    try {
      inFlight = api("/v1/auth/register", {
        method: "POST",
        body: JSON.stringify({ email, password, name }),
      });
      const result = await inFlight;

      // Replace the form with the key. The password fields leave the DOM so a
      // shoulder-surfer and the browser's own form history both lose them.
      formPanel.hidden = true;
      keyPanel.hidden = false;

      const keyInput = document.getElementById("new-key");
      keyInput.value = result.api_key.key;
      document.getElementById("key-prefix").textContent = result.api_key.prefix;
      document.getElementById("key-scopes").textContent = (result.api_key.scopes || []).join(" ");
      keyInput.focus();
      keyInput.select();

      document.getElementById("copy-key").addEventListener("click", (clickEvent) => {
        copyKey(keyInput, document.getElementById("copy-status"), clickEvent.currentTarget);
      });
    } catch (error) {
      showError(errorNode, describeError(error));
      if (error instanceof ApiError && error.code === "conflict") {
        document.getElementById("email").focus();
      }
    } finally {
      inFlight = null;
      setBusy(submit, false);
    }
  });

  document.getElementById("name")?.focus();
}

function initSettings() {
  const loading = document.getElementById("loading");
  const content = document.getElementById("account-content");
  const pageError = document.getElementById("page-error");
  const list = document.getElementById("key-list");
  const keyPanel = document.getElementById("key-panel");
  const keyInput = document.getElementById("new-key");
  const copyStatus = document.getElementById("copy-status");
  const rotateButton = document.getElementById("rotate");
  const confirmBlock = document.getElementById("rotate-confirm");
  const confirmYes = document.getElementById("rotate-confirm-yes");
  const confirmCancel = document.getElementById("rotate-cancel");
  const rotateStatus = document.getElementById("rotate-status");
  const rotateError = document.getElementById("rotate-error");
  if (!content) return;

  async function signOut() {
    try {
      await api("/v1/auth/logout", { method: "POST", body: JSON.stringify({}) });
    } catch {
      // The cookie is cleared by the response even if revocation failed, so
      // leaving for the sign-in page is still the right move.
    }
    window.location.assign("/login");
  }

  for (const id of ["logout", "logout-secondary"]) {
    document.getElementById(id)?.addEventListener("click", signOut);
  }

  function renderKeys(keys) {
    list.replaceChildren();

    if (!keys || keys.length === 0) {
      const empty = document.createElement("li");
      empty.className = "hint";
      empty.textContent = "No keys have been issued to this account.";
      list.append(empty);
      return;
    }

    for (const key of keys) {
      const item = document.createElement("li");
      item.className = "key-item";

      const head = document.createElement("div");
      head.className = "key-item-head";

      const prefix = document.createElement("code");
      prefix.textContent = key.prefix;
      // A prefix alone cannot authenticate anything; it is the public half of
      // the key, kept so two keys can be told apart.
      head.append(prefix);

      const badge = document.createElement("span");
      badge.className = `state-badge state-${key.state}`;
      badge.textContent = key.state;
      head.append(badge);

      const facts = document.createElement("dl");
      facts.className = "key-item-facts";
      for (const [label, value] of [
        ["Created", formatDate(key.created_at)],
        ["Last used", key.last_used_at ? formatDate(key.last_used_at) : "never"],
      ]) {
        const row = document.createElement("div");
        const term = document.createElement("dt");
        const detail = document.createElement("dd");
        term.textContent = label;
        detail.textContent = value;
        row.append(term, detail);
        facts.append(row);
      }

      item.append(head, facts);
      list.append(item);
    }
  }

  async function loadKeys() {
    const result = await api("/v1/account/api-keys");
    renderKeys(result.api_keys);
  }

  async function start() {
    try {
      const account = await api("/v1/account");
      const user = account.user;

      document.getElementById("account-name").textContent = user.name || "—";
      document.getElementById("account-email").textContent = user.email;
      document.getElementById("account-created").textContent = formatDate(user.created_at);

      await loadKeys();

      loading.hidden = true;
      content.hidden = false;
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        // A session that expired while the tab sat open is not an error page.
        window.location.replace("/login?next=%2Fsettings");
        return;
      }
      loading.hidden = true;
      showError(pageError, describeError(error));
    }
  }

  rotateButton?.addEventListener("click", () => {
    rotateButton.hidden = true;
    confirmBlock.hidden = false;
    confirmCancel?.focus();
  });

  confirmCancel?.addEventListener("click", () => {
    confirmBlock.hidden = true;
    rotateButton.hidden = false;
    rotateButton.focus();
  });

  confirmYes?.addEventListener("click", async () => {
    if (inFlight) return;
    showError(rotateError, "");
    rotateStatus.textContent = "";

    setBusy(confirmYes, true, "Rotating…");
    try {
      inFlight = api("/v1/account/api-keys/rotate", {
        method: "POST",
        body: JSON.stringify({}),
      });
      const result = await inFlight;

      // Show the replacement before anything else: it cannot be retrieved later.
      keyInput.value = result.api_key.key;
      keyPanel.hidden = false;

      await loadKeys();

      const revoked = Number(result.revoked) || 0;
      rotateStatus.textContent =
        revoked > 0
          ? `Rotated. ${revoked} previous key${revoked === 1 ? "" : "s"} revoked and no longer accepted.`
          : "New key issued. No previous key was active.";

      confirmBlock.hidden = true;
      rotateButton.hidden = false;

      keyInput.focus();
      keyInput.select();
    } catch (error) {
      showError(rotateError, describeError(error));
    } finally {
      inFlight = null;
      setBusy(confirmYes, false);
    }
  });

  function handleCopy(event) {
    copyKey(keyInput, copyStatus, event.currentTarget);
  }

  // One listener for the life of the page: the field's value changes on each
  // rotation, but the handler reads it when clicked.
  document.getElementById("copy-key")?.addEventListener("click", handleCopy);

  start();
}

const page = document.body.dataset.page;
if (page === "login") initLogin();
else if (page === "register") initRegister();
else if (page === "settings") initSettings();
