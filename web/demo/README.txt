GraphWAN globe demo

From web/:
  pnpm install --frozen-lockfile
  pnpm demo:setup
  pnpm demo

Open http://localhost:5174/ (also listens on other host interfaces).
For a compiled preview: pnpm demo:build && pnpm demo:preview
No credentials or running GraphWAN Server / Agent are required.

This is an isolated demo sharing its globe renderer with the production UI. The
example topology is fictional; its public IPs are looked up in the actual
local DB-IP City Lite database. Two example nodes intentionally share an IP
to demonstrate co-located markers. IP input supports IPv4 and IPv6 and adds
a temporary node linked to the first example node. Reload resets the demo.
Lookup failures remain unlocated rather than inventing coordinates.

Database: ~/.cache/graphwan-demo/city.mmdb
Override with GRAPHWAN_GEOIP_DB=/absolute/path/to/city.mmdb.
pnpm demo:setup downloads the current month's database from DB-IP over HTTPS.
The database is not committed, embedded, or included in release binaries.
The production topology view locates real Agent endpoints through the Server.
GeoIP estimates a location; anycast, VPNs and provider registration can make
that location differ from the actual physical machine.

Drag to orbit, scroll/pinch to zoom, click nodes or links to inspect them.
The sidebar also provides keyboard-accessible node/link selection.
Node selection in the list flies to its location. Markers within roughly
50 km are spread along the tangent, retaining original coordinates and
surface leader lines. Label placement avoids overlaps. Curves follow a
great circle with radial elevation; the opaque globe hides the back side.

Rendering is on demand while idle; optional rotation and camera transitions
are capped at 30 fps, pause in background tabs and respect reduced motion.
WebGL resources are disposed on unmount / view switching. The 2D modes remain
available if WebGL 2 is unavailable. No browser/deployment test suite added.

Data provenance and licensing: public/NOTICE.txt
