"use strict";

const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];
const icon = (name, className = "") =>
  `<svg aria-hidden="true"${className ? ` class="${className}"` : ""}><use href="/assets/icons.svg#${name}"></use></svg>`;
const esc = (value) =>
  String(value ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
const labels = {
  program: "Programs",
  scholarship: "Scholarships",
  fellowship: "Fellowships",
  grant: "Grants",
  internship: "Internships",
  competition: "Competitions",
  job: "Jobs",
  training: "Training",
  conference: "Conferences",
  volunteering: "Volunteering",
};
const singular = {
  program: "Program",
  scholarship: "Scholarship",
  fellowship: "Fellowship",
  grant: "Grant",
  internship: "Internship",
  competition: "Competition",
  job: "Job",
  training: "Training",
  conference: "Conference",
  volunteering: "Volunteering",
};
const kindIcons = {
  program: "sprout",
  scholarship: "graduation",
  fellowship: "sparkles",
  grant: "gift",
  internship: "briefcase",
  competition: "trophy",
  job: "briefcase",
  training: "book",
  conference: "users",
  volunteering: "heart",
};
const sourceLabels = {
  linkedin_post: "LinkedIn post",
  linkedin_page: "Public LinkedIn page",
  rss: "RSS / Atom feed",
  web_page: "Opportunity article",
};
const captures = {
  public_page: "Public page capture",
  rss_summary: "RSS summary",
  manual_import: "Text import",
  reference_import: "Reference snapshot",
};

function readStorage(storage, key, fallback) {
  try {
    return storage.getItem(key) ?? fallback;
  } catch {
    return fallback;
  }
}
let initialSaved = [];
try {
  const parsed = JSON.parse(
    readStorage(
      localStorage,
      "radwave-saved",
      readStorage(localStorage, "radar-saved", "[]"),
    ),
  );
  if (Array.isArray(parsed))
    initialSaved = parsed.filter((id) => typeof id === "string");
} catch {
  /* A malformed preference is safely reset. */
}
const state = {
  page: "discover",
  kind: "",
  search: "",
  region: "",
  status: "active",
  sort: "deadline",
  offset: 0,
  pageSize: 12,
  layout: readStorage(localStorage, "radwave-layout", "list"),
  saved: new Set(initialSaved),
  token: readStorage(
    sessionStorage,
    "radwave-token",
    readStorage(sessionStorage, "radar-token", ""),
  ),
  meta: null,
  items: [],
  total: 0,
  sources: [],
  pendingAction: null,
  detailID: null,
  loadController: null,
  detailController: null,
  signature: "",
  pollBusy: false,
  workspaceOpen: false,
  horizonItems: [],
};

function safeURL(value) {
  try {
    const u = new URL(value);
    return ["http:", "https:"].includes(u.protocol) ? u.href : "";
  } catch {
    return "";
  }
}
function formatDate(value) {
  if (!value) return "Not stated";
  const date = new Date(value.length === 10 ? `${value}T12:00:00Z` : value);
  return Number.isNaN(date.getTime())
    ? "Not stated"
    : new Intl.DateTimeFormat("en-GB", {
        day: "numeric",
        month: "short",
        year: "numeric",
        timeZone: "UTC",
      }).format(date);
}
function formatTime(value) {
  if (!value) return "Never";
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? "Unknown"
    : new Intl.DateTimeFormat("en-GB", {
        day: "numeric",
        month: "short",
        hour: "2-digit",
        minute: "2-digit",
        timeZone: "UTC",
      }).format(date) + " UTC";
}
function ago(value) {
  if (!value) return "Not refreshed yet";
  const seconds = Math.max(0, (Date.now() - new Date(value).getTime()) / 1000);
  if (seconds < 60) return "Just now";
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
}
function statusLabel(o) {
  if (o.status === "closed") return "Closed";
  if (o.status === "closing")
    return o.days_left === 0
      ? "Closes today"
      : o.days_left === 1
        ? "Closes tomorrow"
        : `${o.days_left} days left`;
  if (o.status === "rolling") return "Rolling";
  if (o.status === "unknown") return "Unconfirmed";
  return "Open";
}
function deadlineLabel(o) {
  return o.deadline
    ? formatDate(o.deadline)
    : o.deadline_state === "rolling"
      ? "Rolling applications"
      : "Deadline not stated";
}
function statusPill(o) {
  return `<span class="status-pill status-${esc(o.status)}">${esc(statusLabel(o))}</span>`;
}
function kindPill(o) {
  return `<span class="kind-pill kind-${esc(o.kind)}">${icon(kindIcons[o.kind] || "sprout")}${esc(singular[o.kind] || "Program")}</span>`;
}
function sourceMark(kind) {
  return kind.startsWith("linkedin")
    ? '<span class="source-mark linkedin" aria-label="LinkedIn">in</span>'
    : `<span class="source-mark feed">${icon("rss")}</span>`;
}

class APIError extends Error {
  constructor(message, status) {
    super(message);
    this.status = status;
  }
}
async function api(path, options = {}) {
  const { authToken, ...requestOptions } = options;
  const headers = { Accept: "application/json", ...requestOptions.headers };
  if (requestOptions.body) headers["Content-Type"] = "application/json";
  const token = authToken ?? state.token;
  if (token) headers.Authorization = `Bearer ${token}`;
  const response = await fetch(path, { ...requestOptions, headers });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    if (response.status === 401 && authToken === undefined) {
      state.token = "";
      try {
        sessionStorage.removeItem("radwave-token");
        sessionStorage.removeItem("radar-token");
      } catch {
        /* Session memory still works. */
      }
      updateManagement();
    }
    throw new APIError(
      data.error || "The request could not be completed. Please try again.",
      response.status,
    );
  }
  return data;
}
function toast(message, error = false) {
  const item = document.createElement("div");
  item.className = `toast${error ? " error" : ""}`;
  item.innerHTML = `${icon(error ? "info" : "check-circle")}<span>${esc(message)}</span>`;
  $("#toast-region").replaceChildren(item);
  setTimeout(() => item.remove(), error ? 7000 : 4200);
}
function showDialog(id) {
  const dialog = $(`#${id}`);
  if (!dialog.open) dialog.showModal();
}
function closeDialog(id) {
  $(`#${id}`).close();
}
function requireManagement(action) {
  if (!state.meta?.management.enabled) {
    toast(
      "This dashboard is read-only. Source management is configured by the server owner.",
      true,
    );
    return;
  }
  if (state.meta.management.token_required && !state.token) {
    state.pendingAction = action;
    $("#token-error").hidden = true;
    showDialog("token-dialog");
    return;
  }
  action();
}
function updateManagement() {
  if (!state.meta) return;
  ["#add-opportunity", "#add-source", "#refresh-all"].forEach((selector) => {
    $(selector).disabled =
      !state.meta.management.enabled ||
      (selector === "#refresh-all" && state.meta.running);
  });
  const unlock = $("#unlock-button");
  unlock.hidden = !state.meta.management.token_required;
  unlock.innerHTML = icon(state.token ? "unlock" : "lock");
  unlock.setAttribute(
    "aria-label",
    state.token ? "Management unlocked" : "Unlock management",
  );
  unlock.title = state.token ? "Management unlocked" : "Unlock management";
}
function busyButton(button, busy, message = "Working…") {
  if (busy) {
    button.dataset.original = button.innerHTML;
    button.innerHTML = `${icon("refresh", "spin")} ${esc(message)}`;
    button.disabled = true;
  } else {
    button.innerHTML = button.dataset.original || button.innerHTML;
    button.disabled = false;
  }
}

function updateMeta(meta) {
  state.meta = meta;
  const stats = meta.stats;
  $("#stat-available").textContent = stats.available.toLocaleString();
  $("#stat-closing").textContent = stats.closing_soon.toLocaleString();
  $("#stat-sources").textContent = stats.sources.toLocaleString();
  $("#stat-updated").textContent = stats.updated_today.toLocaleString();
  $("#discover-count").textContent = stats.available;
  $("#closing-count").textContent = stats.closing_soon;
  $("#saved-count").textContent = state.saved.size;
  $("#hero-saved-count").textContent = String(state.saved.size).padStart(
    2,
    "0",
  );
  $("#edition-status").textContent = meta.running
    ? "· REFRESHING"
    : "· PUBLIC SOURCES";
  renderTypes();
  renderRegions();
  window.RadwaveGlobe?.setCounts(stats.regions);
  const select = $("#region");
  if (select.options.length === 1)
    meta.regions.forEach((region) => select.add(new Option(region, region)));
  const last = meta.last_run;
  const sync = $("#sync-dot");
  sync.className = `sync-dot${meta.running ? " busy" : ["partial", "error", "interrupted"].includes(last?.status) ? " partial" : ""}`;
  $("#sync-title").textContent = meta.running
    ? "Refreshing Radwave"
    : last?.status === "completed"
      ? "Sources refreshed"
      : last
        ? "Refresh details available"
        : "Ready to discover";
  $("#sync-detail").textContent = meta.running
    ? "Finding your next possibilities"
    : last?.finished_at
      ? `${ago(last.finished_at)} · See source status`
      : "Public sources, clear details";
  const notice = $("#crawl-notice");
  notice.hidden =
    !meta.running &&
    !["partial", "error", "interrupted"].includes(last?.status);
  if (!notice.hidden)
    notice.innerHTML = `${icon(meta.running ? "refresh" : "info", meta.running ? "spin" : "")}<span>${meta.running ? "Refreshing public sources. New discoveries will appear as they’re captured." : "Some sources could not be fully refreshed. See the capture date and source status for the latest available details."}</span><button data-page="sources">View updates</button>`;
  updateManagement();
}
function renderTypes() {
  const counts = state.meta?.stats.kinds || {};
  const sidebarKinds = [
    "scholarship",
    "fellowship",
    "grant",
    "program",
    "internship",
    "competition",
    "job",
    "training",
  ];
  $("#type-nav").innerHTML = sidebarKinds
    .map(
      (kind) =>
        `<button class="nav-item${state.page === "discover" && state.kind === kind ? " active" : ""}" data-kind="${kind}">${icon(kindIcons[kind])}<span>${labels[kind]}</span><span class="nav-count">${counts[kind] || 0}</span></button>`,
    )
    .join("");
  $("#category-tabs").innerHTML = [
    "",
    "program",
    "fellowship",
    "scholarship",
    "grant",
    "internship",
  ]
    .map(
      (kind) =>
        `<button class="category-tab${state.kind === kind ? " active" : ""}" data-tab="${kind}" aria-pressed="${state.kind === kind}">${kind ? labels[kind] : "All opportunities"}<span>${kind ? counts[kind] || 0 : (state.meta?.stats.available ?? "—")}</span></button>`,
    )
    .join("");
}

function renderRegions() {
  const counts = state.meta?.stats.regions || {};
  $("#region-rail").innerHTML = [
    "",
    "Worldwide",
    "Africa",
    "Americas",
    "Europe",
    "Asia",
    "Oceania",
  ]
    .map(
      (region) =>
        `<button class="region-choice${state.region === region ? " active" : ""}" data-region="${region}" aria-pressed="${state.region === region}">${region ? (region === "Worldwide" ? "Worldwide" : region) : "All regions"}<span>${region ? counts[region] || 0 : (state.meta?.stats.available ?? "—")}</span></button>`,
    )
    .join("");
}

async function loadHorizon() {
  const data = await api(
    "/api/opportunities?status=active&sort=deadline&limit=8",
  );
  state.horizonItems = data.items;
  renderHorizon();
}

function renderHorizon() {
  const container = $("#horizon-strip");
  if (!state.horizonItems.length) {
    container.innerHTML =
      '<p class="horizon-empty">Your next chapter starts with a source. Add a public announcement or explore your workspace.</p>';
    return;
  }
  container.innerHTML = state.horizonItems
    .map((o, index) => {
      const saved = state.saved.has(o.id);
      return `<article class="horizon-card"><div class="horizon-meta"><span>${String(index + 1).padStart(2, "0")} / ${esc(singular[o.kind] || "Program")}</span>${statusPill(o)}</div><h3><button data-open="${esc(o.id)}" aria-label="Preview ${esc(o.title)}">${esc(o.title)}</button></h3><div class="horizon-bottom"><span>${esc(deadlineLabel(o))}</span><button class="save-button${saved ? " saved" : ""}" data-save="${esc(o.id)}" aria-label="${saved ? "Remove from shortlist" : "Shortlist"} ${esc(o.title)}" aria-pressed="${saved}">${icon("bookmark")}</button></div></article>`;
    })
    .join("");
}
function params() {
  const query = new URLSearchParams({
    status: state.page === "closing" ? "closing" : state.status,
    sort: state.sort,
    limit: String(state.pageSize),
    offset: String(state.offset),
  });
  if (state.kind) query.set("kind", state.kind);
  if (state.search) query.set("q", state.search);
  if (state.region) query.set("region", state.region);
  if (state.page === "saved") query.set("ids", [...state.saved].join(","));
  return query;
}
function updateFilters() {
  $("#search").value = state.search;
  $("#region").value = state.region;
  $("#status").value = state.page === "closing" ? "closing" : state.status;
  $("#status").disabled = state.page === "closing";
  $("#sort").value = state.sort;
  const chips = [];
  if (state.kind) chips.push(["kind", labels[state.kind]]);
  if (state.search) chips.push(["search", `“${state.search}”`]);
  if (state.region) chips.push(["region", state.region]);
  const target = $("#active-filters");
  target.hidden = chips.length === 0;
  target.innerHTML =
    chips
      .map(
        ([key, label]) =>
          `<button class="filter-chip" data-clear="${key}" aria-label="Remove ${esc(label)} filter">${esc(label)}${icon("x")}</button>`,
      )
      .join("") +
    '<button class="clear-filters" data-action="clear">Clear filters</button>';
  renderTypes();
  renderRegions();
  window.RadwaveGlobe?.setRegion(state.region, false);
}
function skeletons() {
  return Array.from(
    { length: 6 },
    () =>
      `<div class="opportunity-card skeleton" aria-hidden="true">${'<div class="skeleton-bar"></div>'.repeat(8)}</div>`,
  ).join("");
}
async function loadOpportunities(showLoading = false) {
  if (state.page === "sources") return;
  state.loadController?.abort();
  const controller = new AbortController();
  state.loadController = controller;
  $("#opportunity-grid").setAttribute("aria-busy", "true");
  if (showLoading) $("#opportunity-grid").innerHTML = skeletons();
  updateFilters();
  try {
    const data =
      state.page === "saved" && state.saved.size === 0
        ? { items: [], total: 0 }
        : await api(`/api/opportunities?${params()}`, {
            signal: controller.signal,
          });
    if (controller.signal.aborted) return;
    state.items = data.items;
    state.total = data.total;
    renderCards();
  } catch (error) {
    if (error.name === "AbortError") return;
    $("#opportunity-grid").innerHTML =
      `<div class="empty-state"><span class="empty-icon">${icon("radio")}</span><h3>Your radar needs a moment.</h3><p>${esc(error.message)}</p><button class="button button-quiet" data-action="reload">Try again</button></div>`;
  } finally {
    if (!controller.signal.aborted)
      $("#opportunity-grid").setAttribute("aria-busy", "false");
  }
}
function card(o) {
  const saved = state.saved.has(o.id);
  const age = (o.eligibility || []).find((text) =>
    /\b(?:between|aged?|ages)\s+\d/i.test(text),
  );
  const url = safeURL(o.application_url);
  const engagement = o.reactions !== null || o.comments !== null;
  return `<article class="opportunity-card${o.status === "closed" ? " closed" : ""}" data-id="${esc(o.id)}">
    <div class="card-top">${kindPill(o)}<button class="save-button${saved ? " saved" : ""}" data-save="${esc(o.id)}" aria-label="${saved ? "Unsave" : "Save"} ${esc(o.title)}" aria-pressed="${saved}">${icon("bookmark")}</button></div>
    <div class="card-source">${sourceMark(o.source_kind)}<span>${esc(o.source_name)}</span></div>
    <div class="card-content"><h3 class="card-title"><button data-open="${esc(o.id)}">${esc(o.title)}</button></h3>${o.organizer ? `<div class="card-organizer">${esc(o.organizer)}</div>` : ""}<p class="card-summary">${esc(o.summary)}</p></div>
    <div class="card-facts"><div class="card-fact">${icon("pin")}<span class="${o.location ? "" : "unknown"}" title="${esc(o.location || "Program location is not stated in the captured source")}">${esc(o.location || "Location not stated")}</span></div><div class="card-fact">${icon("calendar")}<span class="deadline-info">${esc(deadlineLabel(o))}${statusPill(o)}</span></div>${o.funding ? `<div class="card-fact">${icon("gift")}<span>${esc(o.funding)}</span></div>` : age ? `<div class="card-fact">${icon("users")}<span title="${esc(age)}">${esc(age)}</span></div>` : ""}</div>
    <div class="card-tags">${(o.tags || [])
      .slice(0, 3)
      .map((tag) => `<span class="tag">${esc(tag)}</span>`)
      .join("")}</div>
    <div class="card-bottom"><div class="engagement">${engagement ? `${o.reactions !== null ? `<span title="Total post reactions at capture time">${icon("thumbs-up")}${Number(o.reactions).toLocaleString()}</span>` : ""}${o.comments !== null ? `<span title="Post comments at capture time">${icon("message")}${Number(o.comments).toLocaleString()}</span>` : ""}` : `<span class="capture-label">${esc(captures[o.capture_method] || "Original source")}</span>`}</div><div class="card-actions">${url && o.status !== "closed" ? `<a class="apply-link" href="${esc(url)}" target="_blank" rel="noopener noreferrer" aria-label="Apply for ${esc(o.title)}">Apply${icon("arrow-up-right")}</a>` : ""}<button class="details-link" data-open="${esc(o.id)}">View details${icon("arrow-right")}</button></div></div>
  </article>`;
}
function renderCards() {
  const grid = $("#opportunity-grid");
  grid.className = `opportunity-grid${state.layout === "list" ? " list-mode" : ""}`;
  $("#result-count").textContent = state.total;
  if (state.items.length) grid.innerHTML = state.items.map(card).join("");
  else {
    const filtered = Boolean(state.search || state.kind || state.region);
    let heading = "A fresh space for your next chapter.",
      text =
        "Add an announcement or a trusted source to start discovering opportunities.",
      action =
        '<button class="button button-primary" data-action="import">Add an opportunity</button>',
      symbol = "sprout";
    if (state.page === "saved" && !state.saved.size) {
      heading = "A little shortlist. A lot of possibility.";
      text =
        "Save opportunities with the bookmark button. Your shortlist stays in this browser, ready when you are.";
      action =
        '<button class="button button-primary" data-page="discover">Explore opportunities</button>';
      symbol = "bookmark";
    } else if (filtered || state.page === "saved") {
      heading = "There’s more beyond this search.";
      text =
        "Try another keyword or loosen your filters to find a different next step.";
      action =
        '<button class="button button-quiet" data-action="clear">Clear filters</button>';
      symbol = "search";
    } else if (state.meta?.running) {
      heading = "Your world is opening up.";
      text =
        "We’re reading the public sources. Opportunities will appear as each source finishes.";
      action = "";
      symbol = "radio";
    } else if (state.page === "closing") {
      heading = "A little room to plan ahead.";
      text =
        "No captured opportunities close in the next seven days. Explore the rest of your radar.";
      action =
        '<button class="button button-quiet" data-page="discover">Explore opportunities</button>';
      symbol = "clock";
    }
    grid.innerHTML = `<div class="empty-state"><span class="empty-icon">${icon(symbol)}</span><h3>${heading}</h3><p>${text}</p>${action}</div>`;
  }
  $("#pagination").hidden = state.total <= state.pageSize;
  $("#page-summary").textContent =
    `${state.offset + 1}–${Math.min(state.offset + state.pageSize, state.total)} of ${state.total} opportunities`;
  $("#previous-page").disabled = state.offset === 0;
  $("#next-page").disabled = state.offset + state.pageSize >= state.total;
  $("#grid-view").classList.toggle("active", state.layout !== "list");
  $("#list-view").classList.toggle("active", state.layout === "list");
  $("#grid-view").setAttribute("aria-pressed", String(state.layout !== "list"));
  $("#list-view").setAttribute("aria-pressed", String(state.layout === "list"));
}
function navigate(page, kind = "") {
  state.page = page;
  state.kind = kind;
  state.search = "";
  state.region = "";
  state.offset = 0;
  state.status = page === "saved" ? "all" : "active";
  openWorkspace();
  $("#stats-grid").hidden = page === "sources" || page === "saved";
  $("#opportunity-section").hidden = page === "sources";
  $("#sources-section").hidden = page !== "sources";
  $("#workspace-eyebrow").textContent = {
    discover: "YOUR NEXT STEP, IN FOCUS",
    saved: "THE POSSIBILITIES YOU KEPT",
    closing: "A LITTLE TIME / A BIG POSSIBILITY",
    sources: "WHERE POSSIBILITY BEGINS",
  }[page];
  $("#feed-title").innerHTML =
    `${page === "saved" ? "Your shortlist." : page === "closing" ? "Worth taking soon." : "Find your next step."} <span id="result-count" class="count-pill">—</span>`;
  $("#feed-subtitle").textContent =
    page === "saved"
      ? "Your personal shortlist. Saved in this browser."
      : page === "closing"
        ? "Applications with a stated deadline in the next seven days."
        : "A little discovery. A whole new direction.";
  $$(".primary-nav .nav-item").forEach((button) => {
    const active =
      button.dataset.page === page ||
      (page === "discover" && button.dataset.action === "home");
    button.classList.toggle("active", active);
    if (active) button.setAttribute("aria-current", "page");
    else button.removeAttribute("aria-current");
  });
  closeNavigation();
  if (page === "sources")
    loadSources().catch((error) => toast(error.message, true));
  else loadOpportunities(true);
  $(".workspace-body").scrollTop = 0;
}

function openWorkspace() {
  state.workspaceOpen = true;
  $("#workspace").hidden = false;
  document.body.classList.add("workspace-open");
  $(".hero-copy").inert = true;
  $(".hero-copy").setAttribute("aria-hidden", "true");
}

function closeWorkspace() {
  state.workspaceOpen = false;
  $("#workspace").hidden = true;
  document.body.classList.remove("workspace-open");
  $(".hero-copy").inert = false;
  $(".hero-copy").removeAttribute("aria-hidden");
  closeNavigation();
}

function home() {
  state.page = "discover";
  state.kind = "";
  state.search = "";
  state.region = "";
  state.status = "active";
  state.offset = 0;
  closeWorkspace();
  $$(".primary-nav .nav-item").forEach((button) => {
    const active = button.dataset.action === "home";
    button.classList.toggle("active", active);
    if (active) button.setAttribute("aria-current", "page");
    else button.removeAttribute("aria-current");
  });
  window.RadwaveGlobe?.setRegion("", true);
  loadOpportunities();
}

function selectRegion(region) {
  if (state.page !== "discover") navigate("discover");
  else openWorkspace();
  state.region = region;
  state.offset = 0;
  loadOpportunities();
  window.RadwaveGlobe?.setRegion(region, true);
}

window.RadwaveUI = { selectRegion };
window.addEventListener("radwave:globe-ready", () => {
  window.RadwaveGlobe?.setCounts(state.meta?.stats.regions || {});
  window.RadwaveGlobe?.setRegion(state.region, false);
});
function clearFilters() {
  state.kind = "";
  state.search = "";
  state.region = "";
  state.offset = 0;
  loadOpportunities();
}
function toggleSaved(id) {
  if (state.saved.has(id)) state.saved.delete(id);
  else {
    if (state.saved.size >= 200) {
      toast(
        "Your shortlist holds up to 200 opportunities. Remove one to add another.",
        true,
      );
      return;
    }
    state.saved.add(id);
  }
  try {
    localStorage.setItem("radwave-saved", JSON.stringify([...state.saved]));
  } catch {
    toast("Saved for this session; browser storage is unavailable.", true);
  }
  $("#saved-count").textContent = state.saved.size;
  $("#hero-saved-count").textContent = String(state.saved.size).padStart(
    2,
    "0",
  );
  renderHorizon();
  if (state.page === "saved") loadOpportunities();
  else renderCards();
  $$(`[data-save="${CSS.escape(id)}"]`).forEach((button) => {
    const saved = state.saved.has(id);
    button.classList.toggle("saved", saved);
    button.setAttribute("aria-pressed", String(saved));
    const title =
      state.items.find((item) => item.id === id)?.title || "opportunity";
    const isHorizon = !!button.closest("#horizon-strip");
    button.setAttribute(
      "aria-label",
      `${isHorizon ? (saved ? "Remove from shortlist" : "Shortlist") : saved ? "Unsave" : "Save"} ${title}`,
    );
    if (button.hasAttribute("data-detail-save"))
      button.innerHTML = `${icon("bookmark")} ${saved ? "Saved to shortlist" : "Save opportunity"}`;
  });
  toast(
    state.saved.has(id)
      ? "Saved to your shortlist."
      : "Removed from your shortlist.",
  );
}

async function openDetail(id) {
  state.detailController?.abort();
  const controller = new AbortController();
  state.detailController = controller;
  state.detailID = id;
  $("#detail-content").innerHTML =
    '<div class="detail-loading">Bringing the details into focus…</div>';
  showDialog("detail-dialog");
  try {
    const data = await api(`/api/opportunities/${encodeURIComponent(id)}`, {
      signal: controller.signal,
    });
    if (controller.signal.aborted || !$("#detail-dialog").open) return;
    renderDetail(data.opportunity, data.history);
  } catch (error) {
    if (error.name !== "AbortError") {
      closeDialog("detail-dialog");
      toast(error.message, true);
    }
  }
}
function renderDetail(o, history) {
  const saved = state.saved.has(o.id),
    source = state.sources.find((item) => item.id === o.source_id);
  const external = safeURL(o.application_url),
    original = safeURL(o.url);
  const applyURL = o.status === "closed" ? original : external || original;
  const applyLabel =
    o.status === "closed"
      ? "View announcement"
      : external
        ? "Go to application"
        : "See source to apply";
  const missing = (o.missing || []).length
    ? `<section class="detail-section missing-box"><strong>Still worth checking</strong><p>The captured source does not confirm: ${esc(o.missing.join(", "))}.</p></section>`
    : "";
  const evidence = Object.entries(o.evidence || {})
    .map(
      ([key, value]) =>
        `<dt>${esc(key.replaceAll("_", " "))}</dt><dd>${esc(value)}</dd>`,
    )
    .join("");
  $("#detail-content").innerHTML =
    `<div class="detail-topbar"><span>A clearer look at your next step</span><div><button class="icon-button" data-action="share" aria-label="Copy opportunity link" title="Copy link">${icon("copy")}</button><button class="icon-button" data-close="detail-dialog" aria-label="Close opportunity details">${icon("x")}</button></div></div>
    <div class="detail-body"><div class="detail-badges">${kindPill(o)}${statusPill(o)}</div><h2 id="detail-title">${esc(o.title)}</h2><p class="detail-organizer">${esc(o.organizer || `Published by ${o.source_name}`)}</p><p class="detail-summary">${esc(o.summary)}</p><div class="detail-tags">${(o.tags || []).map((tag) => `<span class="tag">${esc(tag)}</span>`).join("")}</div>
    <div class="detail-facts"><div class="detail-fact">${icon("calendar")}<div><small>Application deadline</small><strong>${esc(deadlineLabel(o))}</strong></div></div><div class="detail-fact">${icon("pin")}<div><small>Program location</small><strong>${esc(o.location || "Not stated in source")}</strong></div></div><div class="detail-fact">${icon("globe")}<div><small>Discovery region · confirm eligibility</small><strong>${esc(o.region)}</strong></div></div><div class="detail-fact">${icon("gift")}<div><small>Funding / support</small><strong>${esc(o.funding || "See the original announcement")}</strong></div></div></div>
    <section class="detail-section"><h3>${icon("users")}Who can apply?</h3>${o.eligibility?.length ? `<ul>${o.eligibility.map((item) => `<li>${esc(item)}</li>`).join("")}</ul>` : "<p>Eligibility requirements weren’t available in the captured text. Check the original announcement for the full criteria.</p>"}</section>
    ${o.benefits?.length ? `<section class="detail-section"><h3>${icon("sprout")}What’s on offer</h3><ul>${o.benefits.map((item) => `<li>${esc(item)}</li>`).join("")}</ul></section>` : ""}${missing}
    <div class="detail-source-box">${original ? `<a class="source-link" href="${esc(original)}" target="_blank" rel="noopener noreferrer"><span>Read the original · ${esc(o.source_name)}</span>${icon("arrow-up-right")}</a>` : `<strong>${esc(o.source_name)}</strong>`}<p>${o.capture_method === "reference_import" ? `Reference example captured on ${formatDate(o.last_fetched_at)}. A successful public-page refresh replaces this snapshot.` : `${esc(captures[o.capture_method] || "Source capture")} · ${formatTime(o.last_fetched_at)}.`}${source?.status === "blocked" || source?.status === "error" ? " The latest attempt to refresh this source was unsuccessful." : ""} Reaction counts are totals observed at capture time.</p><span class="capture-badge">${esc(captures[o.capture_method] || "Source capture")} · ${esc(o.extraction_version)}</span></div>
    <details class="evidence-details"><summary>Where these details came from${icon("chevron-down")}</summary><dl>${evidence || "No extraction evidence was available."}</dl></details><details class="raw-details"><summary>Read the captured announcement${icon("chevron-down")}</summary><pre>${esc(o.body)}</pre></details>
    <section class="detail-section"><h3>${icon("refresh")}Updates & engagement</h3><div class="history-list">${
      history
        .slice(0, 12)
        .map((entry) => {
          const deltas = [];
          if (entry.reactions_delta !== null && entry.reactions_delta !== 0)
            deltas.push(
              `Reactions ${entry.reactions_delta > 0 ? "+" : ""}${entry.reactions_delta}`,
            );
          if (entry.comments_delta !== null && entry.comments_delta !== 0)
            deltas.push(
              `Comments ${entry.comments_delta > 0 ? "+" : ""}${entry.comments_delta}`,
            );
          return `<div class="history-entry"><time datetime="${esc(entry.observed_at)}">${entry.capture_method === "reference_import" ? formatDate(entry.observed_at) : formatTime(entry.observed_at)}</time><strong>${esc(entry.changes?.join(" · ") || "Source checked · details unchanged")}</strong><p>${entry.reactions !== null ? `${Number(entry.reactions).toLocaleString()} reactions` : "Reaction count not exposed"}${entry.comments !== null ? ` · ${Number(entry.comments).toLocaleString()} comments` : ""}</p>${deltas.length ? `<div class="history-deltas">${deltas.map((delta) => `<span>${esc(delta)}</span>`).join("")}</div>` : ""}</div>`;
        })
        .join("") || "<p>No observation history yet.</p>"
    }</div></section></div>
    <div class="detail-footer"><button class="button button-quiet${saved ? " saved" : ""}" data-save="${esc(o.id)}" data-detail-save aria-pressed="${saved}">${icon("bookmark")}${saved ? "Saved to shortlist" : "Save opportunity"}</button>${applyURL ? `<a class="button button-primary" href="${esc(applyURL)}" target="_blank" rel="noopener noreferrer">${applyLabel}${icon("arrow-up-right")}</a>` : ""}</div>`;
}

async function loadSources() {
  const [sources, runs] = await Promise.all([
    api("/api/sources"),
    api("/api/runs"),
  ]);
  state.sources = sources.items;
  renderSources(runs.items);
}
function renderSources(runs = null) {
  const writable = state.meta?.management.enabled;
  const statuses = {
    ok: "Fetched",
    blocked: "Access blocked",
    partial: "Partial capture",
    error: "Fetch failed",
    idle: "Not yet refreshed",
  };
  $("#source-list").innerHTML = state.sources.length
    ? state.sources
        .map((source) => {
          const name = esc(source.name),
            kind = source.kind.startsWith("linkedin")
              ? "blue"
              : source.kind === "rss"
                ? "green"
                : "purple";
          return `<article class="source-row" data-source-id="${esc(source.id)}"><div class="source-main"><span class="stat-icon ${kind}">${icon(source.kind.startsWith("linkedin") ? "link" : source.kind === "rss" ? "rss" : "book")}</span><div class="source-heading"><h3>${name}</h3><a class="source-url" href="${esc(safeURL(source.url))}" target="_blank" rel="noopener noreferrer">${esc(source.url)}</a></div><div class="source-actions"><button class="source-switch" role="switch" aria-checked="${source.enabled}" aria-label="Track ${name}" data-source-toggle="${esc(source.id)}"${writable ? "" : " disabled"}><span class="switch-track"></span><span>${source.enabled ? "Tracking" : "Paused"}</span></button><button class="icon-button" data-source-refresh="${esc(source.id)}" aria-label="Refresh ${name}" title="Refresh this source"${!writable || state.meta?.running ? " disabled" : ""}>${icon("refresh")}</button></div></div><div class="source-meta"><span class="source-badge ${source.enabled ? esc(source.status) : "paused"}">${source.enabled ? statuses[source.status] || source.status : "Paused"}</span><span>${icon("radio")}${esc(sourceLabels[source.kind])}</span><span>${icon("clock")}Last checked: ${source.last_crawled_at ? ago(source.last_crawled_at) : "Never"}</span><span>${icon("compass")}${source.item_count} captured in last refresh</span>${source.next_crawl_at && source.enabled ? `<span title="${formatTime(source.next_crawl_at)}">Next check: ${formatTime(source.next_crawl_at)}</span>` : ""}</div>${source.last_error ? `<details class="source-error"><summary>View fetch details</summary><p>${esc(source.last_error)}</p></details>` : ""}</article>`;
        })
        .join("")
    : '<div class="empty-state"><span class="empty-icon">' +
      icon("radio") +
      '</span><h3>Open a new channel of possibility.</h3><p>Add a trusted opportunity feed or a public LinkedIn source to grow your radar.</p><button class="button button-primary" data-action="source">Add your first source</button></div>';
  if (runs !== null)
    $("#run-list").innerHTML = runs.length
      ? runs
          .map(
            (run) =>
              `<article class="run-row"><div class="run-main"><span class="stat-icon ${run.status === "completed" ? "green" : "peach"}">${icon(run.status === "running" ? "refresh" : run.status === "completed" ? "check" : "info")}</span><div class="run-copy"><strong>${run.status === "running" ? "Refreshing public sources" : `${run.sources_checked} source${run.sources_checked === 1 ? "" : "s"} checked · ${run.created} new · ${run.updated} updated`}</strong><p>${run.opportunities_seen} captured announcements · ${esc(run.id.slice(0, 18))}</p></div><time class="run-time">${formatTime(run.started_at)}</time><span class="source-badge ${run.status === "completed" ? "ok" : "partial"}">${esc(run.status)}</span></div>${run.errors?.length ? `<details class="run-error"><summary>${run.errors.length} fetch detail${run.errors.length === 1 ? "" : "s"}</summary><ul>${run.errors.map((error) => `<li>${esc(error)}</li>`).join("")}</ul></details>` : ""}</article>`,
          )
          .join("")
      : '<div class="run-row"><div class="run-copy"><strong>Your first refresh is ahead.</strong><p>Refresh a source to start its observation history.</p></div></div>';
}
async function refresh(sourceID = "") {
  if (state.meta) {
    state.meta.running = true;
    updateMeta(state.meta);
    renderSources();
  }
  try {
    const data = await api("/api/crawl", {
      method: "POST",
      body: JSON.stringify({ source_id: sourceID }),
    });
    toast(
      data.run?.status === "running"
        ? "Source refresh started. Follow its progress in Updates."
        : data.run?.status === "completed"
          ? `Refresh complete: ${data.run.created} new, ${data.run.updated} updated.`
          : "Refresh finished. See source status for the captured results.",
    );
    await poll(true);
  } catch (error) {
    toast(error.message, true);
    await poll(true);
  }
}

function closeNavigation() {
  document.body.classList.remove("header-open");
  $("#mobile-menu").setAttribute("aria-expanded", "false");
}
function openImport() {
  requireManagement(() => {
    $("#import-form").reset();
    $("#import-error").hidden = true;
    $("#paste-details").open = false;
    showDialog("import-dialog");
  });
}
function openSource() {
  requireManagement(() => {
    $("#source-form").reset();
    $("#source-error").hidden = true;
    showDialog("source-dialog");
  });
}

document.addEventListener("click", (event) => {
  const button = event.target.closest("button,[data-close]");
  if (!button) return;
  if (button.dataset.page) navigate(button.dataset.page);
  if (button.hasAttribute("data-region")) selectRegion(button.dataset.region);
  if (button.hasAttribute("data-kind"))
    navigate("discover", button.dataset.kind);
  if (button.hasAttribute("data-tab")) {
    state.kind = button.dataset.tab;
    state.offset = 0;
    loadOpportunities();
  }
  if (button.dataset.clear) {
    state[button.dataset.clear] = "";
    state.offset = 0;
    loadOpportunities();
  }
  if (button.dataset.open) openDetail(button.dataset.open);
  if (button.dataset.save) toggleSaved(button.dataset.save);
  if (button.dataset.close) closeDialog(button.dataset.close);
  if (button.dataset.sourceRefresh)
    requireManagement(() => refresh(button.dataset.sourceRefresh));
  if (button.dataset.sourceToggle)
    requireManagement(async () => {
      const source = state.sources.find(
        (item) => item.id === button.dataset.sourceToggle,
      );
      try {
        await api(`/api/sources/${encodeURIComponent(source.id)}`, {
          method: "PATCH",
          body: JSON.stringify({ enabled: !source.enabled }),
        });
        await Promise.all([loadSources(), loadMeta()]);
        toast(
          source.enabled
            ? "Source paused. Captured opportunities remain available."
            : "Source tracking resumed.",
        );
      } catch (error) {
        toast(error.message, true);
      }
    });
  if (button.dataset.action === "clear") clearFilters();
  if (button.dataset.action === "reload") loadOpportunities(true);
  if (button.dataset.action === "import") openImport();
  if (button.dataset.action === "source") openSource();
  if (button.dataset.action === "home") home();
  if (button.dataset.action === "about") showDialog("about-dialog");
  if (button.dataset.action === "share" && state.detailID) {
    const link = `${location.origin}/opportunity/${encodeURIComponent(state.detailID)}`;
    navigator.clipboard
      ?.writeText(link)
      .then(() => toast("Opportunity link copied."))
      .catch(() => toast(`Share this link: ${link}`));
    if (!navigator.clipboard) toast(`Share this link: ${link}`);
  }
  if (button.dataset.export) {
    if (state.page === "saved" && !state.saved.size) {
      toast("Save an opportunity to start your shortlist export.");
      return;
    }
    const p = params();
    p.set("format", button.dataset.export);
    p.delete("limit");
    p.delete("offset");
    const link = document.createElement("a");
    link.href = `/api/export?${p}`;
    link.download = `opportunities.${button.dataset.export}`;
    document.body.append(link);
    link.click();
    link.remove();
    closeDialog("export-dialog");
    toast("Your opportunity export is on its way.");
  }
});

$("#add-opportunity").addEventListener("click", openImport);
$("#add-source").addEventListener("click", openSource);
$("#refresh-all").addEventListener("click", () =>
  requireManagement(() => refresh()),
);
$("#sidebar-sync").addEventListener("click", () => navigate("sources"));
$("#export-button").addEventListener("click", () =>
  showDialog("export-dialog"),
);
$("#unlock-button").addEventListener("click", () => {
  state.pendingAction = null;
  $("#token-error").hidden = true;
  showDialog("token-dialog");
});
$("#mobile-menu").addEventListener("click", () => {
  const open = document.body.classList.toggle("header-open");
  $("#mobile-menu").setAttribute("aria-expanded", String(open));
});
$("#close-workspace").addEventListener("click", closeWorkspace);
let searchTimer;
$("#search").addEventListener("input", (event) => {
  state.search = event.target.value;
  state.offset = 0;
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => loadOpportunities(), 180);
});
["region", "status", "sort"].forEach((key) =>
  $(`#${key}`).addEventListener("change", (event) => {
    state[key] = event.target.value;
    state.offset = 0;
    loadOpportunities();
  }),
);
$("#previous-page").addEventListener("click", () => {
  state.offset = Math.max(0, state.offset - state.pageSize);
  loadOpportunities();
  $("#opportunity-section").scrollIntoView({ block: "start" });
});
$("#next-page").addEventListener("click", () => {
  state.offset += state.pageSize;
  loadOpportunities();
  $("#opportunity-section").scrollIntoView({ block: "start" });
});
["grid", "list"].forEach((layout) =>
  $(`#${layout}-view`).addEventListener("click", () => {
    state.layout = layout;
    try {
      localStorage.setItem("radwave-layout", layout);
    } catch {
      /* Layout remains available in memory. */
    }
    renderCards();
  }),
);
document.addEventListener("keydown", (event) => {
  const typing = event.target.matches(
    "input,textarea,select,[contenteditable]",
  );
  if (
    event.key === "/" &&
    !typing &&
    !$("dialog[open]") &&
    state.page !== "sources"
  ) {
    event.preventDefault();
    if (!state.workspaceOpen) navigate("discover");
    $("#search").focus();
  }
  if (event.key === "Escape") {
    closeNavigation();
    if (!event.target.closest("dialog") && !$("dialog[open]")) closeWorkspace();
  }
});
$$("dialog").forEach((dialog) =>
  dialog.addEventListener("click", (event) => {
    if (event.target !== dialog) return;
    const box = dialog.getBoundingClientRect();
    if (
      event.clientX < box.left ||
      event.clientX > box.right ||
      event.clientY < box.top ||
      event.clientY > box.bottom
    )
      dialog.close();
  }),
);
$("#detail-dialog").addEventListener("close", () => {
  state.detailController?.abort();
  state.detailID = null;
  if (location.pathname.startsWith("/opportunity/"))
    history.replaceState({}, "", "/");
});
$("#token-dialog").addEventListener("close", () => {
  $("#admin-token").value = "";
});
window.addEventListener("storage", (event) => {
  if (event.key !== "radwave-saved" && event.key !== "radar-saved") return;
  try {
    const items = JSON.parse(event.newValue || "[]");
    if (!Array.isArray(items)) return;
    state.saved = new Set(items.filter((id) => typeof id === "string"));
    $("#saved-count").textContent = state.saved.size;
    $("#hero-saved-count").textContent = String(state.saved.size).padStart(
      2,
      "0",
    );
    renderHorizon();
    if (state.page === "saved") loadOpportunities();
    else renderCards();
  } catch {
    /* Ignore invalid external preference writes. */
  }
});

$("#import-form").addEventListener("submit", (event) => {
  event.preventDefault();
  requireManagement(async () => {
    const button = $("#import-submit");
    busyButton(button, true, "Bringing into focus…");
    $("#import-error").hidden = true;
    try {
      const data = await api("/api/import", {
        method: "POST",
        body: JSON.stringify({
          url: $("#import-url").value.trim(),
          text: $("#import-text").value.trim(),
          organizer: $("#import-organizer").value.trim(),
        }),
      });
      closeDialog("import-dialog");
      navigate("discover");
      await loadMeta();
      await loadOpportunities();
      toast(
        data.created
          ? "A new possibility, brought into focus."
          : "Opportunity updated with the latest captured details.",
      );
      openDetail(data.opportunity.id);
    } catch (error) {
      $("#import-error").textContent = error.message;
      $("#import-error").hidden = false;
      $("#paste-details").open = true;
    } finally {
      busyButton(button, false);
    }
  });
});
$("#source-form").addEventListener("submit", (event) => {
  event.preventDefault();
  requireManagement(async () => {
    const button = $("#source-submit");
    busyButton(button, true, "Adding to Radwave…");
    $("#source-error").hidden = true;
    try {
      const data = await api("/api/sources", {
        method: "POST",
        body: JSON.stringify({
          name: $("#source-name").value.trim(),
          url: $("#source-url").value.trim(),
          kind: $("#source-kind").value,
          enabled: true,
        }),
      });
      closeDialog("source-dialog");
      navigate("sources");
      await loadMeta();
      toast(
        data.created
          ? "Source added. Refresh it when you’re ready."
          : "That source is already on your radar.",
      );
    } catch (error) {
      $("#source-error").textContent = error.message;
      $("#source-error").hidden = false;
    } finally {
      busyButton(button, false);
    }
  });
});
$("#token-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const token = $("#admin-token").value.trim(),
    button = $("#token-form button[type=submit]");
  busyButton(button, true, "Unlocking…");
  $("#token-error").hidden = true;
  try {
    await api("/api/unlock", { method: "POST", authToken: token });
    state.token = token;
    try {
      sessionStorage.setItem("radwave-token", token);
    } catch {
      /* The token remains scoped to this page in memory. */
    }
    closeDialog("token-dialog");
    updateManagement();
    toast("Management unlocked for this session.");
    const action = state.pendingAction;
    state.pendingAction = null;
    action?.();
  } catch (error) {
    $("#token-error").textContent = error.message;
    $("#token-error").hidden = false;
  } finally {
    busyButton(button, false);
  }
});

async function loadMeta() {
  const meta = await api("/api/meta");
  updateMeta(meta);
  return meta;
}
async function poll(force = false) {
  if (state.pollBusy || (document.hidden && !force)) return;
  state.pollBusy = true;
  try {
    const meta = await loadMeta(),
      run = meta.last_run;
    const signature = `${run?.id}:${run?.status}:${run?.sources_checked}:${meta.stats.total}:${meta.stats.updated_today}`;
    if (force || signature !== state.signature) {
      state.signature = signature;
      await Promise.all([loadOpportunities(), loadSources(), loadHorizon()]);
    }
  } catch (error) {
    if (force) toast(error.message, true);
  } finally {
    state.pollBusy = false;
  }
}
async function init() {
  $("#opportunity-grid").innerHTML = skeletons();
  $("#saved-count").textContent = state.saved.size;
  try {
    await loadMeta();
    await Promise.all([loadOpportunities(), loadSources(), loadHorizon()]);
    if (state.token && state.meta.management.token_required) {
      try {
        await api("/api/unlock", { method: "POST" });
      } catch {
        state.token = "";
        updateManagement();
      }
    }
    if (location.pathname.startsWith("/opportunity/"))
      openDetail(decodeURIComponent(location.pathname.split("/").pop()));
  } catch (error) {
    $("#opportunity-grid").innerHTML =
      `<div class="empty-state"><span class="empty-icon">${icon("radio")}</span><h3>Your radar needs a moment.</h3><p>${esc(error.message)}</p><button class="button button-quiet" data-action="reload">Try again</button></div>`;
    $("#opportunity-grid").setAttribute("aria-busy", "false");
  }
  setInterval(() => poll(), 8000);
}
init();
