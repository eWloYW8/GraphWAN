import { mkdir, rename, rm } from 'node:fs/promises'
import { createWriteStream } from 'node:fs'
import { homedir } from 'node:os'
import { dirname, join } from 'node:path'
import { Readable } from 'node:stream'
import { pipeline } from 'node:stream/promises'
import { createGunzip } from 'node:zlib'

const destination =
  process.env.GRAPHWAN_GEOIP_DB || join(homedir(), '.cache/graphwan-demo/city.mmdb')
const month = new Date().toISOString().slice(0, 7)
const url = `https://download.db-ip.com/free/dbip-city-lite-${month}.mmdb.gz`
await mkdir(dirname(destination), { recursive: true })
const temporary = `${destination}.${process.pid}.tmp`
try {
  console.log(`Downloading DB-IP City Lite ${month} (CC BY 4.0)…`)
  const response = await fetch(url, { signal: AbortSignal.timeout(300_000) })
  if (!response.ok || !response.body) throw new Error(`Download failed: HTTP ${response.status}`)
  await pipeline(Readable.fromWeb(response.body), createGunzip(), createWriteStream(temporary))
  await rename(temporary, destination)
  console.log(`GeoIP database ready: ${destination}`)
} finally {
  await rm(temporary, { force: true })
}
