import { test, expect, type Page, type APIRequestContext } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import AxeBuilder from '@axe-core/playwright'
import type { State } from '../src/model'
const password = 'graphwan-browser-test-password'
async function login(page: Page) {
  await page.goto('/')
  await page.getByLabel('Administrator password').fill(password)
  await page.getByRole('button', { name: 'Sign in', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Networks', exact: true })).toBeVisible()
}
async function csrf(request: APIRequestContext) {
  return (await (await request.get('/api/v1/session')).json()).csrf_token as string
}
async function enroll(request: APIRequestContext, name: string) {
  const token = (
    await (
      await request.post('/api/v1/enrollment-tokens', {
        headers: { 'X-CSRF-Token': await csrf(request) },
        data: { ttl_seconds: 60 },
      })
    ).json()
  ).token
  const temp = mkdtempSync(join(tmpdir(), 'graphwan-csr-'))
  try {
    execFileSync(
      'openssl',
      [
        'req',
        '-new',
        '-newkey',
        'ed25519',
        '-nodes',
        '-keyout',
        join(temp, 'key.pem'),
        '-out',
        join(temp, 'csr.der'),
        '-outform',
        'DER',
        '-subj',
        '/CN=browser-test',
      ],
      { stdio: 'pipe' },
    )
    const response = await request.post('/api/v1/enroll', {
      headers: { Authorization: `Bearer ${token}` },
      data: { name, csr: readFileSync(join(temp, 'csr.der')).toString('base64') },
    })
    expect(response.status()).toBe(201)
  } finally {
    rmSync(temp, { recursive: true, force: true })
  }
}
async function currentState(request: APIRequestContext): Promise<State> {
  return (await request.get('/api/v1/state')).json()
}
test('real controller: enrollment, graph edits, conflict protection, agent settings and responsive view', async ({
  page,
}) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await page.goto('/')
  await page.getByLabel('Administrator password').fill('wrong-password')
  await page.getByRole('button', { name: 'Sign in', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('invalid password')
  await page.getByLabel('Administrator password').fill(password)
  await page.getByRole('button', { name: 'Sign in', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Networks', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Agents', exact: false }).first().click()
  await page.getByRole('button', { name: 'Enroll agent', exact: true }).click()
  await page.getByRole('button', { name: 'Create enrollment token' }).click()
  await expect(page.getByLabel('Enrollment token')).not.toHaveValue('')
  await page.getByRole('button', { name: 'Done', exact: true }).click()
  await enroll(page.request, 'Paris')
  await enroll(page.request, 'Tokyo')
  await expect(page.getByRole('button', { name: 'Manage Paris' })).toBeVisible()
  await page.getByRole('button', { name: 'Create network', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Network name').fill('Backbone')
  await dialog.getByRole('button', { name: 'Create network', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Backbone', exact: true, level: 1 })).toBeVisible()
  await page.getByRole('button', { name: 'Edit', exact: true }).click()
  for (const [name, ip] of [
    ['Paris', '10.42.0.1'],
    ['Tokyo', '10.42.0.2'],
  ]) {
    await page.getByRole('button', { name: 'Add node', exact: true }).click()
    await dialog.getByLabel('Agent', { exact: true }).selectOption({ label: name })
    await dialog.getByLabel('Virtual IP').fill(ip)
    await dialog.getByRole('button', { name: 'Add to draft' }).click()
  }
  await page.getByRole('button', { name: 'Add edge', exact: true }).click()
  await dialog.getByRole('button', { name: 'Add to draft' }).click()
  await page.getByLabel('Routing weight').fill('7')
  await page.getByLabel('NAT hole punching').check()
  await page.getByRole('button', { name: 'Save changes' }).click()
  await expect(page.getByRole('status')).toContainText('configuration saved')
  let state = await currentState(page.request)
  expect(state.networks[0].edges[0].weight).toBe(7)
  expect(state.networks[0].edges[0].methods.hole_punch).toBe(true)
  await page.getByRole('button', { name: 'Edit', exact: true }).click()
  const graphNode = page.locator('.react-flow__node').first()
  const rect = await graphNode.boundingBox()
  expect(rect).toBeTruthy()
  await page.mouse.move(rect!.x + 35, rect!.y + 25)
  await page.mouse.down()
  await page.mouse.move(rect!.x + 100, rect!.y + 105, { steps: 10 })
  await page.mouse.up()
  await page.getByRole('button', { name: 'Save changes' }).click()
  await expect(page.getByRole('status')).toContainText('configuration saved')
  const moved = await currentState(page.request)
  expect(moved.networks[0].nodes[0].position).not.toEqual(state.networks[0].nodes[0].position)
  await page.getByRole('button', { name: 'Back to network details' }).click()
  await page.getByRole('button', { name: 'Edit', exact: true }).click()
  await page.getByLabel('Network name').fill('Unsaved draft')
  state = await currentState(page.request)
  const remote = { ...state.networks[0], name: 'External edit' }
  expect(
    (
      await page.request.put(`/api/v1/networks/${remote.id}`, {
        headers: { 'X-CSRF-Token': await csrf(page.request), 'If-Match': String(state.revision) },
        data: remote,
      })
    ).ok(),
  ).toBe(true)
  await expect(page.getByRole('alert')).toContainText('Your draft is preserved')
  await expect(page.getByRole('button', { name: 'Save changes' })).toBeDisabled()
  await expect(page.getByLabel('Network name')).toHaveValue('Unsaved draft')
  page.once('dialog', (d) => d.accept())
  await page.getByRole('button', { name: 'Discard', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'External edit', level: 1 })).toBeVisible()
  await page.evaluate(() => scrollTo(0, 0))
  const audit = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
  expect(audit.violations).toEqual([])
  await page.screenshot({ path: 'test-results/topology-desktop.png', fullPage: true })
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.getByRole('heading', { name: 'External edit', level: 1 })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await expect
    .poll(async () => {
      const canvas = await page.locator('.canvas').boundingBox()
      const node = await page.locator('.react-flow__node').first().boundingBox()
      return (
        !!canvas && !!node && node.x >= canvas.x && node.x + node.width <= canvas.x + canvas.width
      )
    })
    .toBe(true)
  const mobileAudit = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21aa'])
    .analyze()
  expect(mobileAudit.violations).toEqual([])
  await page.screenshot({ path: 'test-results/topology-mobile.png', fullPage: true })
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.getByRole('button', { name: 'Agents', exact: false }).first().click()
  await page.getByRole('button', { name: 'Manage Paris' }).click()
  await dialog.getByLabel('Listen port').fill('25000')
  await dialog
    .getByLabel('STUN servers')
    .fill('stun.example.test:3478\n[2001:db8::1]:3478\ntcp://stun.example.test:3478')
  await dialog.getByRole('button', { name: 'Add endpoint' }).click()
  await dialog.getByLabel('Endpoint 1 transport').selectOption('wss')
  await dialog.getByLabel('Endpoint 1 URL').fill('wss://paris.example.test:443/overlay')
  await dialog.getByRole('button', { name: 'Save agent' }).click()
  await expect(dialog).not.toBeVisible()
  state = await currentState(page.request)
  expect(state.agents.find((a) => a.name === 'Paris')?.stun_servers).toEqual([
    'stun.example.test:3478',
    '[2001:db8::1]:3478',
    'tcp://stun.example.test:3478',
  ])
  expect(state.agents.find((a) => a.name === 'Paris')?.endpoints?.[0].url).toBe(
    'wss://paris.example.test:443/overlay',
  )
  await page.getByRole('button', { name: 'Create network', exact: true }).click()
  await dialog.getByLabel('Network name').fill('Second network')
  await dialog.getByLabel('Subnet (CIDR)').fill('10.43.0.0/24')
  await dialog.getByRole('button', { name: 'Create network', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Second network', level: 1 })).toBeVisible()
  expect((await currentState(page.request)).networks).toHaveLength(2)
  await page.getByRole('button', { name: 'External edit', exact: true }).click()
  await page.getByRole('button', { name: 'Edit', exact: true }).click()
  await page.getByRole('button', { name: 'Paris 10.42.0.1', exact: true }).click()
  await page.getByRole('button', { name: 'Remove node and its edges' }).click()
  await page.getByRole('button', { name: 'Save changes' }).click()
  await expect(page.getByRole('status')).toContainText('configuration saved')
  state = await currentState(page.request)
  expect(state.networks[0].nodes).toHaveLength(1)
  expect(state.networks[0].edges).toHaveLength(0)
  page.once('dialog', (d) => d.accept())
  await page.getByRole('button', { name: 'Delete network', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Networks', exact: true })).toBeVisible()
  expect((await currentState(page.request)).networks.map((n) => n.name)).toEqual(['Second network'])
  await page.getByRole('button', { name: 'Second network', exact: true }).click()
  page.once('dialog', (d) => d.accept())
  await page.getByRole('button', { name: 'Delete network', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Networks', exact: true })).toBeVisible()
  expect((await currentState(page.request)).networks).toHaveLength(0)
  await page.getByRole('button', { name: 'Sign out' }).click()
  await expect(page.getByLabel('Administrator password')).toBeVisible()
  expect(errors).toEqual([])
})

test('live view: agreeing paths, report-time rates, preference editing and connection loss', async ({
  page,
}) => {
  const fixture = {
    at: '2026-01-01T00:00:00Z',
    revision: 1,
    state: {
      schema: 1,
      revision: 1,
      agents: ['Paris', 'Tokyo'].map((name, i) => ({
        id: `agent${i}`,
        name,
        public_key: '',
        listen_port: 24752,
        revoked: false,
        endpoints: [],
      })),
      networks: [
        {
          id: 'network',
          name: 'Production backbone',
          cidr: '10.42.0.0/24',
          mtu: 1280,
          cipher: 'chacha20-poly1305',
          nodes: ['Paris', 'Tokyo'].map((name, i) => ({
            id: `node${i}`,
            agent_id: `agent${i}`,
            name,
            address: `10.42.0.${i + 1}`,
            position: { x: i * 350, y: i * 80 },
          })),
          edges: [
            {
              id: 'edge',
              a: 'node0',
              b: 'node1',
              weight: 10,
              enabled: true,
              transports: ['udp', 'tcp'],
              methods: { ipv4_direct: true, ipv6_direct: true, hole_punch: false },
            },
          ],
        },
      ],
    },
    agents: [0, 1].map((i) => ({
      agent_id: `agent${i}`,
      connected: true,
      last_seen: '2026-01-01T00:00:00Z',
      version: 'browser-fixture',
      resources: {
        cpu_percent: 12.5 as number | undefined,
        logical_cpus: 4,
        go_memory_bytes: 48000000,
        heap_bytes: 24000000,
        goroutines: 25,
        uptime_seconds: 3661,
      },
      runtime_error: '',
      applied_revision: 1,
      links: [
        {
          network_id: 'network',
          edge_id: 'edge',
          link_id: 'session',
          candidate_id: 'candidate',
          transport: 'udp',
          remote: '192.0.2.1:24752',
          healthy: true,
          active: true,
          rtt_ms: 17.5,
          loss: 0.01,
          rx_bytes: 100,
          tx_bytes: 200,
        },
      ],
    })),
  }
  await page.addInitScript(
    ({ sample }) => {
      class Source extends EventTarget {
        onerror: (() => void) | null = null
        closed = false
        constructor() {
          super()
          ;(window as unknown as { source: Source }).source = this
          queueMicrotask(() => {
            if (!this.closed)
              this.dispatchEvent(new MessageEvent('snapshot', { data: JSON.stringify(sample) }))
          })
        }
        close() {
          this.closed = true
        }
      }
      Object.defineProperty(window, 'EventSource', { value: Source })
    },
    { sample: fixture },
  )
  await login(page)
  await page.getByRole('button', { name: 'Production backbone', exact: true }).click()
  await expect(page.getByText('1 connected', { exact: true })).toBeVisible()
  const update = structuredClone(fixture)
  for (const status of update.agents) {
    status.last_seen = '2026-01-01T00:00:02Z'
    status.links[0].tx_bytes = 250200
    status.links[0].rx_bytes = 125100
  }
  await page.evaluate(
    (sample) =>
      (window as unknown as { source: EventTarget }).source.dispatchEvent(
        new MessageEvent('snapshot', { data: JSON.stringify(sample) }),
      ),
    update,
  )
  await expect(page.getByText('UDP · 17.5 ms · 1.0 Mbps', { exact: true })).toBeVisible()
  // Repeated browser snapshots retain rates until the Agent report advances.
  await page.evaluate(
    (sample) =>
      (window as unknown as { source: EventTarget }).source.dispatchEvent(
        new MessageEvent('snapshot', { data: JSON.stringify(sample) }),
      ),
    update,
  )
  await expect(page.getByText('UDP · 17.5 ms · 1.0 Mbps', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Agents', exact: false }).first().click()
  const agentRow = page.getByRole('row').filter({ hasText: 'Paris' })
  await expect(agentRow).toContainText('CPU 12.5%')
  await expect(agentRow).toContainText('Go memory 48.0 MB')
  await page.getByRole('button', { name: 'Production backbone', exact: true }).click()
  update.agents[0].runtime_error = 'network Production backbone: TUN device unavailable'
  await page.evaluate(
    (sample) =>
      (window as unknown as { source: EventTarget }).source.dispatchEvent(
        new MessageEvent('snapshot', { data: JSON.stringify(sample) }),
      ),
    update,
  )
  await page.locator('.react-flow__node').filter({ hasText: 'Paris' }).click()
  await expect(page.getByRole('alert')).toContainText('TUN device unavailable')
  await expect(
    page.locator('.react-flow__node').filter({ hasText: 'Paris' }).getByTitle('Error'),
  ).toBeVisible()
  update.agents[0].runtime_error = ''
  await page.evaluate(
    (sample) =>
      (window as unknown as { source: EventTarget }).source.dispatchEvent(
        new MessageEvent('snapshot', { data: JSON.stringify(sample) }),
      ),
    update,
  )
  await expect(page.getByRole('alert')).toHaveCount(0)
  await expect(
    page.locator('.react-flow__node').filter({ hasText: 'Paris' }).getByTitle('Online'),
  ).toBeVisible()
  const resourceDetails = page.locator('dl[aria-label="Agent resources"]')
  await expect(resourceDetails).toContainText('12.5%')
  await expect(resourceDetails).toContainText('48.0 MB')
  await expect(resourceDetails).toContainText('24.0 MB')
  await expect(resourceDetails).toContainText('1h 1m')
  await page.screenshot({ path: 'test-results/node-resources-desktop.png', fullPage: true })
  update.agents[0].connected = false
  await page.evaluate(
    (sample) =>
      (window as unknown as { source: EventTarget }).source.dispatchEvent(
        new MessageEvent('snapshot', { data: JSON.stringify(sample) }),
      ),
    update,
  )
  await expect(resourceDetails).toContainText('Unavailable')
  await expect(resourceDetails).not.toContainText('48.0 MB')
  update.agents[0].connected = true
  update.agents[0].resources.cpu_percent = undefined
  await page.evaluate(
    (sample) =>
      (window as unknown as { source: EventTarget }).source.dispatchEvent(
        new MessageEvent('snapshot', { data: JSON.stringify(sample) }),
      ),
    update,
  )
  await expect(resourceDetails).toContainText('Unavailable')
  await expect(resourceDetails).toContainText('48.0 MB')
  update.agents[0].resources.cpu_percent = 0
  await page.evaluate(
    (sample) =>
      (window as unknown as { source: EventTarget }).source.dispatchEvent(
        new MessageEvent('snapshot', { data: JSON.stringify(sample) }),
      ),
    update,
  )
  await expect(resourceDetails).toContainText('0.0%')
  await page.getByRole('button', { name: 'Back to network details' }).click()
  await page.getByRole('button', { name: 'Paris ↔ Tokyo Weight 10' }).click()
  await expect(page.getByText('17.5 ms RTT')).toHaveCount(2)
  await page.evaluate(() => scrollTo(0, 0))
  await page.screenshot({ path: 'test-results/live-topology-desktop.png', fullPage: true })
  await page.clock.install()
  await page.clock.fastForward(6000)
  await expect(page.getByText('Reconnecting', { exact: true })).toBeVisible()
  await page.evaluate(
    (sample) =>
      (window as unknown as { source: EventTarget }).source.dispatchEvent(
        new MessageEvent('snapshot', { data: JSON.stringify(sample) }),
      ),
    update,
  )
  await expect(page.getByText('Live updates', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Edit', exact: true }).click()
  await page.getByLabel('Preferred path').selectOption('candidate')
  await expect(page.getByRole('button', { name: 'Save changes' })).toBeEnabled()
  await page.evaluate(() =>
    (window as unknown as { source: { onerror: () => void } }).source.onerror(),
  )
  await expect(page.getByText('Reconnecting', { exact: true })).toBeVisible()
  await expect(page.getByText('Unknown', { exact: true })).toHaveCount(3)
  await expect(page.getByText('0 online', { exact: true })).toHaveCount(0)
  page.once('dialog', (dialog) => dialog.accept())
  await page.getByRole('button', { name: 'Agents', exact: false }).first().click()
  await expect(agentRow).toContainText('Unavailable')
  await expect(agentRow).not.toContainText('48.0 MB')
})
