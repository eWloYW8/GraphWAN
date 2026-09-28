import { execFileSync, spawn } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
const temp = mkdtempSync(join(tmpdir(), 'graphwan-browser-'))
const binary = join(temp, 'graphwan')
execFileSync('go', ['build', '-o', binary, '../cmd/graphwan'], { stdio: 'inherit' })
const server = spawn(
  binary,
  ['server', '--http', '--listen', '127.0.0.1:18543', '--data-dir', join(temp, 'data')],
  {
    env: { ...process.env, GRAPHWAN_ADMIN_PASSWORD: 'graphwan-browser-test-password' },
    stdio: 'inherit',
  },
)
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => server.kill(signal))
server.on('exit', (code) => {
  rmSync(temp, { recursive: true, force: true })
  process.exit(code ?? 0)
})
