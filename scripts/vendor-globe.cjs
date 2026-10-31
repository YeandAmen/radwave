// Build the self-hosted globe assets used by Radwave. The renderer and public
// domain country outlines are served by Go; browsing makes no tile requests.
const { mkdir, copyFile, writeFile } = require("node:fs/promises");
const path = require("node:path");

(async () => {
  const root = path.dirname(require.resolve("maplibre-gl/package.json"));
  const output = path.resolve(__dirname, "../internal/web/assets/vendor");
  await mkdir(output, { recursive: true });
  for (const [source, target] of [
    ["maplibre-gl.mjs", "maplibre-gl.mjs"],
    ["maplibre-gl-shared.mjs", "maplibre-gl-shared.mjs"],
    ["maplibre-gl-worker.mjs", "maplibre-gl-worker.mjs"],
    ["maplibre-gl.css", "maplibre-gl.css"],
  ]) {
    await copyFile(path.join(root, "dist", source), path.join(output, target));
  }
  await copyFile(
    path.join(root, "LICENSE.txt"),
    path.join(output, "MAPLIBRE-LICENSE.txt"),
  );
  const response = await fetch(
    "https://sinnogram.vercel.app/vendor/earth-countries.geojson",
  );
  if (!response.ok) throw new Error("Country outline download failed.");
  const data = await response.json();
  if (data.type !== "FeatureCollection" || !data.features?.length)
    throw new Error("Invalid country outline data.");
  await writeFile(
    path.join(output, "earth-countries.geojson"),
    JSON.stringify(data),
  );
  await writeFile(
    path.join(output, "NATURAL-EARTH.txt"),
    "Country outlines: Natural Earth, public domain.\nhttps://www.naturalearthdata.com/about/terms-of-use/\nDataset provided through the Sinnogram reference globe.\n",
  );
  console.log(
    `Prepared MapLibre and ${data.features.length} public-domain country outlines.`,
  );
  console.log(
    "Country properties:",
    Object.keys(data.features[0].properties).join(", "),
  );
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
