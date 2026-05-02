#!/usr/bin/env node

import { execFileSync } from 'node:child_process'
import { pathToFileURL } from 'node:url'
import path from 'node:path'
import process from 'node:process'
import { writeFileSync } from 'node:fs'

const rootDir = process.env.ROOT_DIR ?? process.cwd()
const e2eDir = process.env.E2E_DIR ?? path.join(rootDir, 'test/e2e')
const baseUrl = process.env.SYNC_GO_BASE_URL ?? process.env.BASE_URL
const databaseUrl = process.env.DATABASE_URL
const secret = process.env.SECRET ?? 'test-secret'
const clientImport = process.env.SHADOW_CLIENT_IMPORT
const timeoutMs = Number(process.env.SHADOW_CLIENT_TIMEOUT_MS ?? 15000)
const resultFile = process.env.SHADOW_CLIENT_RESULT_FILE
const requestedScenarios = new Set(
  (process.env.SHADOW_CLIENT_SCENARIOS ?? process.argv.slice(2).join(',') ?? '')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
)

if (!baseUrl) {
  throw new Error('BASE_URL or SYNC_GO_BASE_URL is required')
}
if (!databaseUrl) {
  throw new Error('DATABASE_URL is required')
}
if (!clientImport) {
  throw new Error('SHADOW_CLIENT_IMPORT must point to the client ESM entrypoint')
}

const importSpec =
  clientImport.startsWith('file:') || clientImport.startsWith('node:')
    ? clientImport
    : pathToFileURL(path.resolve(clientImport)).href

const { Shape, ShapeStream } = await import(importSpec)
const shapeUrl = new URL('/v1/shape', baseUrl).toString()
const results = []

function scenarioEnabled(name) {
  return requestedScenarios.size === 0 || requestedScenarios.has(name)
}

function sqlFile(name) {
  return path.join(e2eDir, 'sql', name)
}

function runSql(name) {
  execFileSync('psql', ['-v', 'ON_ERROR_STOP=1', databaseUrl, '-f', sqlFile(name)], {
    stdio: 'pipe',
  })
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

function withTimeout(promise, label, ms = timeoutMs) {
  let timer
  const timeout = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error(`${label} timed out after ${ms}ms`)), ms)
  })
  return Promise.race([promise, timeout]).finally(() => clearTimeout(timer))
}

function normalizeRows(rows) {
  return rows
    .map((row) => {
      const normalized = {}
      for (const key of Object.keys(row).sort()) {
        const value = row[key]
        normalized[key] = value instanceof Date ? value.toISOString() : value
      }
      return normalized
    })
    .sort((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b)))
}

function rowKey(row) {
  if ('id' in row) return String(row.id)
  if ('tenant_id' in row && 'seq' in row) return `${row.tenant_id}:${row.seq}`
  return JSON.stringify(row)
}

function rowsByKey(rows) {
  return new Map(rows.map((row) => [rowKey(row), row]))
}

function assert(condition, message) {
  if (!condition) {
    throw new Error(message)
  }
}

function assertIds(rows, expectedIds, label) {
  const actual = normalizeRows(rows)
    .map((row) => row.id)
    .sort()
  const expected = [...expectedIds].sort()
  assert(
    JSON.stringify(actual) === JSON.stringify(expected),
    `${label}: expected ids ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`
  )
}

function assertPartitionKeys(rows, expectedKeys, label) {
  const actual = normalizeRows(rows)
    .map((row) => `${row.tenant_id}:${row.seq}`)
    .sort()
  const expected = [...expectedKeys].sort()
  assert(
    JSON.stringify(actual) === JSON.stringify(expected),
    `${label}: expected partition keys ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`
  )
}

function normalizeMessages(messages) {
  return messages.map((message) => JSON.parse(JSON.stringify(message))).sort((a, b) => {
    const left = JSON.stringify(a)
    const right = JSON.stringify(b)
    return left.localeCompare(right)
  })
}

function createShape(params, options = {}) {
  const controller = new AbortController()
  const stream = new ShapeStream({
    url: shapeUrl,
    params: { ...params, secret },
    subscribe: options.subscribe ?? true,
    liveSse: options.liveSse ?? false,
    signal: controller.signal,
  })
  const shape = new Shape(stream)
  return {
    stream,
    shape,
    async close() {
      shape.unsubscribeAll()
      stream.unsubscribeAll()
      controller.abort('shadow-client-scenario-complete')
      await sleep(25)
    },
  }
}

async function waitForStreamMessage(stream, predicate, label) {
  return withTimeout(
    new Promise((resolve, reject) => {
      const unsubscribe = stream.subscribe(
        (messages) => {
          try {
            if (predicate(messages)) {
              unsubscribe()
              resolve(normalizeMessages(messages))
            }
          } catch (err) {
            unsubscribe()
            reject(err)
          }
        },
        (err) => {
          unsubscribe()
          reject(err)
        }
      )
    }),
    label
  )
}

async function waitForRows(shape, predicate, label) {
  if (predicate(shape.currentRows)) {
    return normalizeRows(shape.currentRows)
  }
  return withTimeout(
    new Promise((resolve, reject) => {
      const unsubscribe = shape.subscribe(({ rows }) => {
        try {
          if (predicate(rows)) {
            unsubscribe()
            resolve(normalizeRows(rows))
          }
        } catch (err) {
          unsubscribe()
          reject(err)
        }
      })
    }),
    label
  )
}

async function runShapeScenario(name, fn) {
  if (!scenarioEnabled(name)) return
  const startedAt = Date.now()
  const result = await fn()
  results.push({
    scenario: name,
    elapsed_ms: Date.now() - startedAt,
    ...result,
  })
}

await runShapeScenario('snapshot', async () => {
  const client = createShape({ table: 'items' }, { subscribe: false })
  try {
    const rows = normalizeRows(await withTimeout(client.shape.rows, 'snapshot rows'))
    assertIds(rows, [
      '00000000-0000-0000-0000-000000000001',
      '00000000-0000-0000-0000-000000000002',
      '00000000-0000-0000-0000-000000000003',
    ], 'snapshot')
    return { rows }
  } finally {
    await client.close()
  }
})

await runShapeScenario('filtered_snapshot', async () => {
  const client = createShape(
    { table: 'items', where: 'priority >= $1', params: { 1: '2' } },
    { subscribe: false }
  )
  try {
    const rows = normalizeRows(await withTimeout(client.shape.rows, 'filtered rows'))
    assertIds(rows, [
      '00000000-0000-0000-0000-000000000002',
      '00000000-0000-0000-0000-000000000003',
    ], 'filtered_snapshot')
    return { rows }
  } finally {
    await client.close()
  }
})

await runShapeScenario('columns_snapshot', async () => {
  const client = createShape(
    { table: 'items', columns: ['id', 'value'] },
    { subscribe: false }
  )
  try {
    const rows = normalizeRows(await withTimeout(client.shape.rows, 'columns rows'))
    assert(rows.every((row) => Object.keys(row).sort().join(',') === 'id,value'), 'columns_snapshot: unexpected columns')
    assertIds(rows, [
      '00000000-0000-0000-0000-000000000001',
      '00000000-0000-0000-0000-000000000002',
      '00000000-0000-0000-0000-000000000003',
    ], 'columns_snapshot')
    return { rows }
  } finally {
    await client.close()
  }
})

await runShapeScenario('live_longpoll_insert', async () => {
  const client = createShape({ table: 'items' })
  try {
    await withTimeout(client.shape.rows, 'live long-poll initial rows')
    runSql('insert_item.sql')
    const rows = await waitForRows(
      client.shape,
      (currentRows) => rowsByKey(currentRows).has('00000000-0000-0000-0000-000000000010'),
      'live long-poll insert'
    )
    assertIds(rows, [
      '00000000-0000-0000-0000-000000000001',
      '00000000-0000-0000-0000-000000000002',
      '00000000-0000-0000-0000-000000000003',
      '00000000-0000-0000-0000-000000000010',
    ], 'live_longpoll_insert')
    return { rows }
  } finally {
    await client.close()
  }
})

await runShapeScenario('live_sse_update', async () => {
  const client = createShape({ table: 'items' }, { liveSse: true })
  try {
    await withTimeout(client.shape.rows, 'live SSE initial rows')
    runSql('update_item.sql')
    const rows = await waitForRows(
      client.shape,
      (currentRows) => {
        const row = rowsByKey(currentRows).get('00000000-0000-0000-0000-000000000001')
        return row?.value === 'alpha-updated' && row?.priority === 10
      },
      'live SSE update'
    )
    const updated = rowsByKey(rows).get('00000000-0000-0000-0000-000000000001')
    assert(updated?.value === 'alpha-updated', 'live_sse_update: updated value missing')
    assert(updated?.priority === 10, 'live_sse_update: updated priority missing')
    return { updated }
  } finally {
    await client.close()
  }
})

await runShapeScenario('subquery_move_in_out', async () => {
  const client = createShape({
    table: 'items',
    where: 'id IN (SELECT item_id FROM item_flags WHERE enabled = true)',
  })
  try {
    const initialRows = normalizeRows(await withTimeout(client.shape.rows, 'subquery initial rows'))
    assertIds(initialRows, [
      '00000000-0000-0000-0000-000000000001',
      '00000000-0000-0000-0000-000000000003',
    ], 'subquery initial')

    runSql('update_item_flag.sql')
    const movedInMessages = await waitForStreamMessage(
      client.stream,
      (messages) =>
        messages.some(
          (message) =>
            message?.headers?.operation === 'insert' &&
            message?.headers?.is_move_in === true &&
            message?.value?.id === '00000000-0000-0000-0000-000000000002'
        ),
      'subquery move-in stream message'
    )
    const movedInRows = await waitForRows(
      client.shape,
      (currentRows) => rowsByKey(currentRows).has('00000000-0000-0000-0000-000000000002'),
      'subquery move-in'
    )
    assertIds(movedInRows, [
      '00000000-0000-0000-0000-000000000001',
      '00000000-0000-0000-0000-000000000002',
      '00000000-0000-0000-0000-000000000003',
    ], 'subquery move-in')

    runSql('update_item_flag_false.sql')
    const movedOutMessages = await waitForStreamMessage(
      client.stream,
      (messages) => messages.some((message) => message?.headers?.event === 'move-out'),
      'subquery move-out stream message'
    )
    return {
      initial_rows: initialRows,
      moved_in_messages: movedInMessages,
      moved_in_rows: movedInRows,
      moved_out_messages: movedOutMessages,
    }
  } finally {
    await client.close()
  }
})

await runShapeScenario('partition_root_live_insert', async () => {
  const client = createShape({ table: 'partitioned_items' })
  try {
    await withTimeout(client.shape.rows, 'partition initial rows')
    runSql('insert_partition_item.sql')
    const rows = await waitForRows(
      client.shape,
      (currentRows) => rowsByKey(currentRows).has('1:130'),
      'partition root live insert'
    )
    assertPartitionKeys(rows, ['1:10', '1:20', '1:130', '2:120'], 'partition_root_live_insert')
    return { rows }
  } finally {
    await client.close()
  }
})

const output = JSON.stringify(
  {
    client_import: clientImport,
    base_url: baseUrl,
    scenarios: results,
  },
  null,
  2
)

if (resultFile) {
  writeFileSync(resultFile, `${output}\n`)
}

console.log(output)
process.exit(0)
