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
const composeProjectName = process.env.SHADOW_CLIENT_COMPOSE_PROJECT_NAME
const composeFile = process.env.SHADOW_CLIENT_COMPOSE_FILE
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

function execSql(sql) {
  execFileSync('psql', ['-v', 'ON_ERROR_STOP=1', databaseUrl, '-c', sql], {
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

function requireComposeControl(label) {
  if (!composeProjectName || !composeFile) {
    throw new Error(`${label} requires SHADOW_CLIENT_COMPOSE_PROJECT_NAME and SHADOW_CLIENT_COMPOSE_FILE`)
  }
}

function compose(args, label) {
  requireComposeControl(label)
  execFileSync('docker', ['compose', '-p', composeProjectName, '-f', composeFile, ...args], {
    stdio: 'pipe',
  })
}

async function waitForHealthState(expected, label, ms = timeoutMs) {
  const deadline = Date.now() + ms
  let lastError
  while (Date.now() < deadline) {
    try {
      const response = await fetch(new URL('/v1/health', baseUrl))
      const body = await response.json()
      if (body.status === expected) {
        return body
      }
      lastError = new Error(`${label}: expected health ${expected}, got ${body.status}`)
    } catch (err) {
      lastError = err
    }
    await sleep(500)
  }
  throw lastError ?? new Error(`${label}: timed out waiting for health ${expected}`)
}

async function restartSyncService(label) {
  compose(['restart', 'postgres-sync-go'], label)
  await waitForHealthState('active', `${label} active health`)
}

async function stopPostgres(label) {
  compose(['stop', 'postgres'], label)
}

async function startPostgres(label) {
  compose(['start', 'postgres'], label)
  await waitForHealthState('active', `${label} active health`)
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

await runShapeScenario('columns_live_update', async () => {
  const client = createShape({
    table: 'items',
    columns: ['id', 'value'],
  })
  try {
    await withTimeout(client.shape.rows, 'columns live initial rows')
    execSql(`
      UPDATE items
      SET value = 'gamma-columns-live'
      WHERE id = '00000000-0000-0000-0000-000000000003'
    `)
    const rows = await waitForRows(
      client.shape,
      (currentRows) => {
        const row = rowsByKey(currentRows).get('00000000-0000-0000-0000-000000000003')
        return row?.value === 'gamma-columns-live'
      },
      'columns live update'
    )
    assert(
      rows.every((row) => Object.keys(row).sort().join(',') === 'id,value'),
      'columns_live_update: unexpected columns'
    )
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

await runShapeScenario('reconnect_after_postgres_restart', async () => {
  const client = createShape({
    table: 'items',
    where: "category = 'shadow-reconnect'",
  })
  try {
    await withTimeout(client.shape.rows, 'reconnect initial rows')
    const handleBefore = client.stream.shapeHandle
    assert(handleBefore, 'reconnect_after_postgres_restart: missing initial shape handle')

    await stopPostgres('reconnect_after_postgres_restart stop postgres')
    await waitForHealthState('waiting', 'reconnect waiting health')
    await startPostgres('reconnect_after_postgres_restart start postgres')

    execSql(`
      INSERT INTO items (id, value, priority, archived, category, inserted_at)
      VALUES ('00000000-0000-0000-0000-000000000030', 'shadow-reconnect', 30, FALSE, 'shadow-reconnect', '2025-01-30T00:00:00Z')
    `)
    const rows = await waitForRows(
      client.shape,
      (currentRows) => rowsByKey(currentRows).has('00000000-0000-0000-0000-000000000030'),
      'reconnect after postgres restart'
    )
    assert(
      client.stream.shapeHandle === handleBefore,
      'reconnect_after_postgres_restart: shape handle changed across replication reconnect'
    )
    return { handle: client.stream.shapeHandle, rows }
  } finally {
    await client.close()
  }
})

await runShapeScenario('service_restart_disk_continuity', async () => {
  const client = createShape({
    table: 'items',
    where: "category = 'shadow-service-restart'",
  })
  try {
    await withTimeout(client.shape.rows, 'service restart initial rows')
    execSql(`
      INSERT INTO items (id, value, priority, archived, category, inserted_at)
      VALUES ('00000000-0000-0000-0000-000000000040', 'shadow-service-before', 40, FALSE, 'shadow-service-restart', '2025-02-01T00:00:00Z')
    `)
    await waitForRows(
      client.shape,
      (currentRows) => rowsByKey(currentRows).has('00000000-0000-0000-0000-000000000040'),
      'service restart pre-restart row'
    )
    const handleBefore = client.stream.shapeHandle
    assert(handleBefore, 'service_restart_disk_continuity: missing initial shape handle')

    await restartSyncService('service_restart_disk_continuity restart postgres-sync-go')

    execSql(`
      INSERT INTO items (id, value, priority, archived, category, inserted_at)
      VALUES ('00000000-0000-0000-0000-000000000041', 'shadow-service-after', 41, FALSE, 'shadow-service-restart', '2025-02-02T00:00:00Z')
    `)
    const rows = await waitForRows(
      client.shape,
      (currentRows) =>
        rowsByKey(currentRows).has('00000000-0000-0000-0000-000000000040') &&
        rowsByKey(currentRows).has('00000000-0000-0000-0000-000000000041'),
      'service restart disk continuity'
    )
    assert(
      client.stream.shapeHandle === handleBefore,
      'service_restart_disk_continuity: disk mode should preserve shape handle across process restart'
    )
    return { handle: client.stream.shapeHandle, rows }
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

await runShapeScenario('mixed_concurrent_shapes', async () => {
  const allItems = createShape({ table: 'items' })
  const filtered = createShape({
    table: 'items',
    where: "category = 'shadow-concurrent'",
  })
  const projected = createShape({
    table: 'items',
    columns: ['id', 'value'],
    where: "category = 'shadow-concurrent'",
  })
  const dependent = createShape({
    table: 'items',
    where: 'id IN (SELECT item_id FROM item_flags WHERE enabled = true)',
  })
  const clients = [allItems, filtered, projected, dependent]
  try {
    await Promise.all(clients.map((client) => withTimeout(client.shape.rows, 'mixed concurrent initial rows')))
    execSql(`
      BEGIN;
      INSERT INTO items (id, value, priority, archived, category, inserted_at) VALUES
        ('00000000-0000-0000-0000-000000000020', 'shadow-concurrent-a', 20, FALSE, 'shadow-concurrent', '2025-01-20T00:00:00Z'),
        ('00000000-0000-0000-0000-000000000021', 'shadow-concurrent-b', 21, FALSE, 'other-shadow', '2025-01-21T00:00:00Z');
      INSERT INTO item_flags (item_id, enabled) VALUES
        ('00000000-0000-0000-0000-000000000020', TRUE),
        ('00000000-0000-0000-0000-000000000021', FALSE);
      COMMIT;
    `)

    const [allRows, filteredRows, projectedRows, dependentRows] = await Promise.all([
      waitForRows(
        allItems.shape,
        (rows) =>
          rowsByKey(rows).has('00000000-0000-0000-0000-000000000020') &&
          rowsByKey(rows).has('00000000-0000-0000-0000-000000000021'),
        'mixed all-items shape'
      ),
      waitForRows(
        filtered.shape,
        (rows) =>
          rowsByKey(rows).has('00000000-0000-0000-0000-000000000020') &&
          !rowsByKey(rows).has('00000000-0000-0000-0000-000000000021'),
        'mixed filtered shape'
      ),
      waitForRows(
        projected.shape,
        (rows) => rowsByKey(rows).get('00000000-0000-0000-0000-000000000020')?.value === 'shadow-concurrent-a',
        'mixed projected shape'
      ),
      waitForRows(
        dependent.shape,
        (rows) =>
          rowsByKey(rows).has('00000000-0000-0000-0000-000000000020') &&
          !rowsByKey(rows).has('00000000-0000-0000-0000-000000000021'),
        'mixed dependent shape'
      ),
    ])

    assert(
      projectedRows.every((row) => Object.keys(row).sort().join(',') === 'id,value'),
      'mixed_concurrent_shapes: projected shape returned unexpected columns'
    )

    return {
      all_count: allRows.length,
      filtered_rows: filteredRows,
      projected_rows: projectedRows,
      dependent_count: dependentRows.length,
    }
  } finally {
    await Promise.all(clients.map((client) => client.close()))
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

await runShapeScenario('client_refetch_after_invalidation', async () => {
  const client = createShape({ table: 'items' })
  try {
    const initialRows = normalizeRows(await withTimeout(client.shape.rows, 'invalidation initial rows'))
    assert(initialRows.length > 0, 'client_refetch_after_invalidation: expected initial rows before truncate')
    const handleBefore = client.stream.shapeHandle
    assert(handleBefore, 'client_refetch_after_invalidation: missing initial shape handle')

    runSql('truncate_items.sql')
    const rows = await waitForRows(
      client.shape,
      (currentRows) => currentRows.length === 0,
      'client refetch after invalidation'
    )
    assert(
      client.stream.shapeHandle && client.stream.shapeHandle !== handleBefore,
      'client_refetch_after_invalidation: expected handle rotation after must-refetch'
    )
    return { previous_handle: handleBefore, current_handle: client.stream.shapeHandle, rows }
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
