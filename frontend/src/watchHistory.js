const DEVICE_ID_COOKIE_NAME = 'evd_device_id'
const HISTORY_STORAGE_PREFIX = 'evd_watch_history'
const HISTORY_LIMIT = 200

const DEFAULT_COOKIE_MAX_AGE_SECONDS = 60 * 60 * 24 * 365 * 3

const safeNow = () => Date.now()

const readCookie = (name) => {
  if (typeof document === 'undefined') return ''
  const encodedName = `${encodeURIComponent(name)}=`
  const chunks = document.cookie ? document.cookie.split(';') : []
  for (const chunk of chunks) {
    const entry = chunk.trim()
    if (!entry.startsWith(encodedName)) continue
    try {
      return decodeURIComponent(entry.slice(encodedName.length))
    } catch (err) {
      return ''
    }
  }
  return ''
}

const writeCookie = (name, value, maxAgeSeconds = DEFAULT_COOKIE_MAX_AGE_SECONDS) => {
  if (typeof document === 'undefined') return
  const encodedName = encodeURIComponent(name)
  const encodedValue = encodeURIComponent(value)
  document.cookie = `${encodedName}=${encodedValue}; Max-Age=${maxAgeSeconds}; Path=/; SameSite=Lax`
}

const generateDeviceId = () => {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  const randomPart = Math.random().toString(36).slice(2)
  return `dev-${safeNow().toString(36)}-${randomPart}`
}

const toStorageKey = (deviceId) => `${HISTORY_STORAGE_PREFIX}:${deviceId}`

const coerceEntry = (value) => {
  if (!value || typeof value !== 'object') return null

  const path = typeof value.path === 'string' ? value.path : ''
  if (!path) return null

  const title = typeof value.title === 'string' && value.title
    ? value.title
    : path

  const currentTime = Number.isFinite(value.currentTime) && value.currentTime >= 0
    ? value.currentTime
    : 0

  const duration = Number.isFinite(value.duration) && value.duration >= 0
    ? value.duration
    : 0

  const lastWatchedAt = Number.isFinite(value.lastWatchedAt) && value.lastWatchedAt > 0
    ? value.lastWatchedAt
    : safeNow()

  return {
    path,
    title,
    currentTime,
    duration,
    lastWatchedAt
  }
}

const trimHistory = (entries) => {
  return entries
    .filter(Boolean)
    .sort((a, b) => b.lastWatchedAt - a.lastWatchedAt)
    .slice(0, HISTORY_LIMIT)
}

export const getOrCreateDeviceId = () => {
  const existing = readCookie(DEVICE_ID_COOKIE_NAME)
  if (existing) return existing

  const created = generateDeviceId()
  writeCookie(DEVICE_ID_COOKIE_NAME, created)
  return created
}

export const loadHistoryForDevice = (deviceId) => {
  if (!deviceId || typeof localStorage === 'undefined') return []

  try {
    const raw = localStorage.getItem(toStorageKey(deviceId))
    if (!raw) return []

    const parsed = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []

    const normalized = parsed.map(coerceEntry)
    return trimHistory(normalized)
  } catch (err) {
    return []
  }
}

export const saveHistoryForDevice = (deviceId, entries) => {
  if (!deviceId || typeof localStorage === 'undefined') return

  try {
    const normalized = trimHistory(Array.isArray(entries) ? entries.map(coerceEntry) : [])
    localStorage.setItem(toStorageKey(deviceId), JSON.stringify(normalized))
  } catch (err) {
    // ignore storage errors
  }
}

export const mergeHistoryEntry = (entries, nextEntry) => {
  const normalizedEntry = coerceEntry(nextEntry)
  if (!normalizedEntry) return trimHistory(Array.isArray(entries) ? entries.map(coerceEntry) : [])

  const source = Array.isArray(entries) ? entries.map(coerceEntry).filter(Boolean) : []
  const withoutCurrent = source.filter((entry) => entry.path !== normalizedEntry.path)

  return trimHistory([normalizedEntry, ...withoutCurrent])
}
