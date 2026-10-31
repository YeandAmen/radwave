import * as maplibre from "./vendor/maplibre-gl.mjs";

const scene = document.getElementById("scene");
const host = document.getElementById("globe-map");
const status = document.getElementById("globe-status");
const reduceMotion = matchMedia("(prefers-reduced-motion: reduce)").matches;
const regions = {
  Africa: {
    center: [20, 8],
    code: "AF",
    countries:
      "DZA AGO BEN BWA BFA BDI CMR CPV CAF TCD COM COG COD CIV DJI EGY GNQ ERI SWZ ETH GAB GMB GHA GIN GNB KEN LSO LBR LBY MDG MWI MLI MRT MUS MAR MOZ NAM NER NGA RWA STP SEN SYC SLE SOM ZAF SSD SDN TZA TGO TUN UGA ZMB ZWE ESH",
  },
  Americas: {
    center: [-90, 20],
    code: "AM",
    countries:
      "CAN USA MEX BLZ CRI SLV GTM HND NIC PAN ARG BOL BRA CHL COL ECU GUY PRY PER SUR URY VEN CUB HTI DOM JAM BHS TTO PRI GRL FLK",
  },
  Europe: {
    center: [15, 50],
    code: "EU",
    countries:
      "ALB AUT BLR BEL BIH BGR HRV CYP CZE DNK EST FIN FRA DEU GRC HUN ISL IRL ITA LVA LTU LUX MDA MNE NLD MKD NOR POL PRT ROU RUS SRB SVK SVN ESP SWE CHE UKR GBR XKX",
  },
  Asia: {
    center: [90, 30],
    code: "AS",
    countries:
      "AFG ARM AZE BHR BGD BTN BRN KHM CHN GEO IND IDN IRN IRQ ISR JPN JOR KAZ KWT KGZ LAO LBN MYS MNG MMR NPL PRK OMN PAK PHL QAT SAU SGP KOR LKA SYR TWN TJK THA TLS TUR TKM ARE UZB VNM YEM PSE",
  },
  Oceania: {
    center: [140, -25],
    code: "OC",
    countries: "AUS NZL PNG FJI SLB VUT NCL",
  },
};
const countryRegion = new Map();
for (const [name, region] of Object.entries(regions))
  for (const code of region.countries.split(" ")) countryRegion.set(code, name);
let globe,
  ready = false,
  currentRegion = "",
  counts = {},
  markerEntries = [],
  rotationPaused = reduceMotion,
  pauseUntil = 0,
  lastFrame = 0,
  fallback = false;

function zoom() {
  return Math.max(
    -0.5,
    Math.min(
      2.1,
      Math.log2(
        (Math.max(150, Math.min(host.clientWidth, host.clientHeight)) * 0.5) /
          81.5,
      ),
    ),
  );
}
function facing(coords) {
  const c = globe.getCenter(),
    r = Math.PI / 180;
  return (
    Math.sin(coords[1] * r) * Math.sin(c.lat * r) +
      Math.cos(coords[1] * r) *
        Math.cos(c.lat * r) *
        Math.cos((coords[0] - c.lng) * r) >
    0.15
  );
}
function flight(center, targetZoom) {
  if (!ready) return;
  globe[reduceMotion ? "jumpTo" : "flyTo"]({
    center,
    zoom: targetZoom,
    duration: 1100,
    essential: false,
  });
}
function grid() {
  const features = [];
  for (let x = -180; x < 180; x += 20) {
    const coordinates = [];
    for (let y = -80; y <= 80; y += 2) coordinates.push([x, y]);
    features.push({
      type: "Feature",
      geometry: { type: "LineString", coordinates },
      properties: {},
    });
  }
  for (let y = -80; y <= 80; y += 20) {
    const coordinates = [];
    for (let x = -180; x <= 180; x += 2) coordinates.push([x, y]);
    features.push({
      type: "Feature",
      geometry: { type: "LineString", coordinates },
      properties: {},
    });
  }
  return { type: "FeatureCollection", features };
}
function labels() {
  if (!ready) return;
  for (const entry of markerEntries) {
    const p = globe.project(entry.center);
    entry.element.hidden =
      !facing(entry.center) ||
      p.x < 10 ||
      p.y < 10 ||
      p.x > host.clientWidth - 10 ||
      p.y > host.clientHeight - 20;
  }
  const point =
    currentRegion && regions[currentRegion]
      ? regions[currentRegion].center
      : [20, 8];
  const p = globe.project(point),
    width = host.clientWidth,
    height = host.clientHeight;
  const callout = document.getElementById("globe-callout");
  callout.hidden =
    !facing(point) || p.x < 8 || p.y < 8 || p.x > width - 8 || p.y > height - 8;
  if (callout.hidden) return;
  const x = Math.max(95, width * 0.73),
    y = Math.max(25, height * 0.2);
  document
    .getElementById("globe-leader")
    .setAttribute("d", `M ${p.x} ${p.y} L ${x - 12} ${y} L ${x + 68} ${y}`);
  document.getElementById("globe-target").setAttribute("cx", p.x);
  document.getElementById("globe-target").setAttribute("cy", p.y);
  const label = document.getElementById("globe-label");
  label.style.left = `${x}px`;
  label.style.top = `${y + 8}px`;
  document.getElementById("globe-label-code").textContent =
    `${regions[currentRegion]?.code || "RW"} / 001`;
  document.getElementById("globe-label-title").textContent =
    currentRegion || "WORLDWIDE";
  document.getElementById("globe-label-subtitle").textContent = currentRegion
    ? `${counts[currentRegion] || 0} OPPORTUNITIES · DISCOVERY REGION`
    : "PUBLIC OPPORTUNITIES";
}
function markers() {
  if (!ready) return;
  for (const entry of markerEntries) entry.marker.remove();
  markerEntries = [];
  for (const [name, region] of Object.entries(regions)) {
    if (!counts[name]) continue;
    const element = document.createElement("button");
    element.type = "button";
    element.className = `region-pin${currentRegion === name ? " selected" : ""}`;
    element.setAttribute(
      "aria-label",
      `Explore ${name}: ${counts[name]} opportunities`,
    );
    const dot = document.createElement("span");
    dot.className = "region-pin-dot";
    dot.setAttribute("aria-hidden", "true");
    const text = document.createElement("span");
    text.className = "region-pin-label";
    text.textContent = `${name} · ${counts[name]}`;
    element.append(dot, text);
    element.addEventListener("click", (event) => {
      event.stopPropagation();
      pauseUntil = performance.now() + 9000;
      window.RadwaveUI?.selectRegion(name);
    });
    const marker = new maplibre.Marker({ element, anchor: "center" })
      .setLngLat(region.center)
      .addTo(globe);
    markerEntries.push({ element, marker, center: region.center });
  }
  labels();
}
function setRegion(region, animate = true) {
  const changed = currentRegion !== region;
  currentRegion = region;
  if (!ready) return;
  globe.setPaintProperty(
    "land",
    "fill-color",
    region && regions[region]
      ? ["case", ["==", ["get", "region"], region], "#899078", "#efebdf"]
      : "#efebdf",
  );
  if (changed) markers();
  if (animate) {
    pauseUntil = performance.now() + 9000;
    flight(
      regions[region]?.center || [10, 18],
      regions[region] ? Math.max(zoom(), 1.8) : zoom(),
    );
  }
  labels();
}
function setCounts(value) {
  const next = value || {};
  if (JSON.stringify(next) === JSON.stringify(counts)) return;
  counts = next;
  markers();
}
function reset() {
  pauseUntil = performance.now() + 9000;
  flight(regions[currentRegion]?.center || [10, 18], zoom());
}
function rotationFrame(time) {
  if (lastFrame && time - lastFrame < 45) {
    if (!fallback) requestAnimationFrame(rotationFrame);
    return;
  }
  const delta = lastFrame ? Math.min(time - lastFrame, 100) : 0;
  lastFrame = time;
  if (
    ready &&
    !rotationPaused &&
    document.visibilityState === "visible" &&
    !document.body.classList.contains("workspace-open") &&
    !document.querySelector("dialog[open]") &&
    time >= pauseUntil &&
    !globe.isMoving()
  ) {
    const c = globe.getCenter();
    globe.jumpTo({ center: [c.lng + delta * 0.0022, c.lat] });
  }
  if (!fallback) requestAnimationFrame(rotationFrame);
}
function staticGlobe() {
  if (fallback) return;
  fallback = true;
  ready = false;
  try {
    globe?.remove();
  } catch {}
  document.getElementById("globe-callout").hidden = true;
  host.innerHTML =
    '<svg class="globe-fallback" viewBox="0 0 600 600" role="img" aria-label="Radwave globe illustration"><defs><clipPath id="earth-clip"><circle cx="300" cy="300" r="238"/></clipPath></defs><circle cx="300" cy="300" r="238" fill="#deded3"/><g clip-path="url(#earth-clip)" fill="none" stroke="#aaa795" stroke-width=".6"><ellipse cx="300" cy="300" rx="112" ry="238"/><ellipse cx="300" cy="300" rx="195" ry="238"/><ellipse cx="300" cy="300" rx="238" ry="64"/><ellipse cx="300" cy="300" rx="238" ry="157"/><path d="M62 300h476M300 62v476"/><g fill="#efebdf"><path d="m269 243-67 12-29 44 27 49 33 20 20 74 35 55 33-33 35-102 45-64-32-36-63-13z"/><path d="m255 149 21-32 57 7 25 43 35 6 55 39 75 9 32 70-37 40-34-35-47-33-30 22-28-16-12-57-49-18-29 8-32-24z"/><path d="m107 102 73-24 43 30-18 54-42 30-38 79-45-22-21-86z"/><path d="m148 299 39 14 29 52-17 65-22 20-19-48-15-45z"/></g></g></svg>';
  status.textContent = "Use the region choices to explore your opportunities.";
  scene.dataset.sceneReady = "fallback";
}

window.RadwaveGlobe = { setCounts, setRegion, reset };
window.dispatchEvent(new Event("radwave:globe-ready"));
document.getElementById("globe-home").addEventListener("click", () => {
  window.RadwaveUI?.selectRegion("");
  setRegion("", true);
  reset();
});
document.getElementById("globe-reset").addEventListener("click", reset);
document.getElementById("globe-zoom-in").addEventListener("click", () => {
  if (ready) {
    pauseUntil = performance.now() + 9000;
    globe.easeTo({
      zoom: Math.min(5, globe.getZoom() + 0.6),
      duration: reduceMotion ? 0 : 300,
    });
  }
});
document.getElementById("globe-zoom-out").addEventListener("click", () => {
  if (ready) {
    pauseUntil = performance.now() + 9000;
    globe.easeTo({
      zoom: Math.max(-0.5, globe.getZoom() - 0.6),
      duration: reduceMotion ? 0 : 300,
    });
  }
});
document
  .getElementById("globe-orbit-toggle")
  .addEventListener("click", (event) => {
    rotationPaused = !rotationPaused;
    const button = event.currentTarget;
    button.setAttribute("aria-pressed", String(rotationPaused));
    button.setAttribute(
      "aria-label",
      rotationPaused ? "Start globe rotation" : "Pause globe rotation",
    );
    button.innerHTML = `<svg aria-hidden="true"><use href="/assets/icons.svg#${rotationPaused ? "play" : "pause"}"></use></svg>`;
  });
for (const type of ["pointerdown", "wheel", "keydown", "focusin"])
  scene.addEventListener(
    type,
    () => {
      pauseUntil = performance.now() + 8000;
    },
    { passive: true },
  );

try {
  maplibre.setWorkerUrl("/assets/vendor/maplibre-gl-worker.mjs");
  globe = new maplibre.Map({
    container: host,
    style: {
      version: 8,
      sources: {},
      layers: [
        {
          id: "ocean",
          type: "background",
          paint: { "background-color": "#deded3" },
        },
      ],
    },
    center: [10, 18],
    zoom: zoom(),
    minZoom: -0.5,
    maxZoom: 5,
    attributionControl: false,
    cooperativeGestures: matchMedia("(max-width: 760px)").matches,
    dragRotate: false,
    pitchWithRotate: false,
    renderWorldCopies: false,
    canvasContextAttributes: { antialias: true },
  });
  globe.touchZoomRotate.disableRotation();
  const timer = setTimeout(() => {
    if (!ready) staticGlobe();
  }, 15000);
  globe.on("style.load", async () => {
    try {
      globe.setProjection({ type: "globe" });
      if (globe.setSky)
        globe.setSky({
          "sky-color": "rgba(0,0,0,0)",
          "horizon-color": "rgba(0,0,0,0)",
          "fog-color": "rgba(0,0,0,0)",
          "atmosphere-blend": 0,
        });
      const response = await fetch("/assets/vendor/earth-countries.geojson");
      if (!response.ok) throw new Error("outline fetch failed");
      const earth = await response.json();
      for (const feature of earth.features)
        feature.properties.region =
          countryRegion.get(feature.properties.code) || "";
      globe.addSource("earth", {
        type: "geojson",
        data: earth,
        tolerance: 0.35,
      });
      globe.addSource("grid", { type: "geojson", data: grid() });
      globe.addLayer({
        id: "grid",
        type: "line",
        source: "grid",
        paint: {
          "line-color": "#b2b4a3",
          "line-width": 0.6,
          "line-opacity": 0.42,
        },
      });
      globe.addLayer({
        id: "land",
        type: "fill",
        source: "earth",
        paint: { "fill-color": "#efebdf" },
      });
      globe.addLayer({
        id: "borders",
        type: "line",
        source: "earth",
        paint: {
          "line-color": "#aaa795",
          "line-width": 0.65,
          "line-opacity": 0.8,
        },
      });
      ready = true;
      clearTimeout(timer);
      scene.dataset.sceneReady = "true";
      setRegion(currentRegion, false);
      markers();
      globe
        .getCanvas()
        .setAttribute(
          "aria-label",
          "Globe: drag to turn, scroll or pinch to zoom; choose a region to explore opportunities",
        );
      globe.on("click", "land", (event) => {
        const region = event.features?.[0]?.properties?.region;
        if (region) {
          pauseUntil = performance.now() + 9000;
          window.RadwaveUI?.selectRegion(region);
        }
      });
      status.textContent = "";
      requestAnimationFrame(rotationFrame);
    } catch (error) {
      console.warn("Radwave globe:", error);
      staticGlobe();
    }
  });
  globe.on("move", labels);
  globe.on("webglcontextlost", staticGlobe);
  new ResizeObserver(() => {
    if (ready) {
      globe.resize();
      reset();
    }
  }).observe(host);
  document.addEventListener("visibilitychange", () => {
    if (ready && document.visibilityState === "visible") globe.resize();
  });
} catch (error) {
  console.warn("Radwave globe:", error);
  staticGlobe();
}
