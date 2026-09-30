import { defineConfig, type Plugin, type ViteDevServer } from 'vite'
import react from '@vitejs/plugin-react'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { isIP } from 'node:net'
import { open, type CityResponse } from 'maxmind'

// A demo-only local database reader. No production API, credentials, or agents.
function geoip(): Plugin {
  const filename =
    process.env.GRAPHWAN_GEOIP_DB || join(homedir(), '.cache/graphwan-demo/city.mmdb')
  const install = async (server: Pick<ViteDevServer, 'middlewares'>) => {
    const database = await open<CityResponse>(filename).catch(() => {
      throw new Error('GeoIP database unavailable. Run pnpm demo:setup or set GRAPHWAN_GEOIP_DB.')
    })
    server.middlewares.use(
      '/__demo/geoip',
      (req: import('node:http').IncomingMessage, res: import('node:http').ServerResponse) => {
        const ip = new URL(req.url || '/', 'http://localhost').searchParams.get('ip')?.trim() || ''
        res.setHeader('Content-Type', 'application/json')
        res.setHeader('Cache-Control', 'no-store')
        const fail = (status: number, error: string) => {
          res.statusCode = status
          res.end(JSON.stringify({ error }))
        }
        if (req.method !== 'GET') return fail(405, 'Only GET is supported.')
        if (!isIP(ip)) return fail(400, 'Enter a valid IPv4 or IPv6 address.')
        const result = database.get(ip)
        if (
          !result?.location ||
          !Number.isFinite(result.location.latitude) ||
          !Number.isFinite(result.location.longitude)
        ) {
          return fail(404, 'No location in the database for this IP. Use a public IP address.')
        }
        res.end(
          JSON.stringify({
            ip,
            latitude: result.location.latitude,
            longitude: result.location.longitude,
            city: result.city?.names?.en || result.subdivisions?.[0]?.names?.en || 'Unknown city',
            country: result.country?.names?.en || 'Unknown country',
            countryCode: result.country?.iso_code || '',
            source: 'DB-IP City Lite',
          }),
        )
      },
    )
  }
  return { name: 'graphwan-demo-geoip', configureServer: install, configurePreviewServer: install }
}

export default defineConfig({
  root: 'demo',
  plugins: [react(), geoip()],
  build: {
    outDir: '../node_modules/.cache/graphwan-globe-demo',
    emptyOutDir: true,
    license: true,
  },
})
